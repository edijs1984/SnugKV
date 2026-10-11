package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// outboundConfig is a definition with its secrets decrypted, held in memory
// only while the worker runs.
type outboundConfig struct {
	def     Definition
	headers [][2]string
	signing string
}

func resolveOutbound(d Definition, s *sealer) (*outboundConfig, error) {
	oc := &outboundConfig{def: d}
	for _, h := range d.Outbound.Headers {
		v := h.Value
		if h.Secret {
			var err error
			if v, err = s.open(h.Enc); err != nil {
				return nil, fmt.Errorf("header %s: %w", h.Name, err)
			}
		}
		oc.headers = append(oc.headers, [2]string{h.Name, v})
	}
	if d.Outbound.SigningSecret.Enc != "" {
		var err error
		if oc.signing, err = s.open(d.Outbound.SigningSecret.Enc); err != nil {
			return nil, fmt.Errorf("signing secret: %w", err)
		}
	}
	return oc, nil
}

// delivery is one outcome of one attempt.
type delivery struct {
	Status     int
	Err        string
	Retryable  bool
	RetryAfter time.Duration
	Body       string
	Millis     int64
}

func (d delivery) ok() bool { return d.Err == "" }

// buildBody turns a stream entry into the request body.
func buildBody(def Definition, e streamEntry) ([]byte, string, string) {
	f := e.fields
	mid := f["mid"]
	if mid == "" {
		mid = e.id
	}
	event := f["event"]
	raw, hasPayload := f["payload"]
	var payload json.RawMessage
	if hasPayload {
		if json.Valid([]byte(raw)) {
			payload = json.RawMessage(raw)
		} else {
			payload, _ = json.Marshal(raw)
		}
	}
	if def.Outbound.Body == "payload" {
		if hasPayload {
			return payload, mid, event
		}
		b, _ := json.Marshal(f)
		return b, mid, event
	}
	env := map[string]any{"id": e.id, "topic": def.Topic, "mid": mid}
	if event != "" {
		env["event"] = event
	}
	if ts, err := strconv.ParseInt(f["ts"], 10, 64); err == nil {
		env["ts"] = ts
	}
	if p := f["producer"]; p != "" {
		env["producer"] = p
	}
	if h := f["headers"]; h != "" && json.Valid([]byte(h)) {
		env["headers"] = json.RawMessage(h)
	}
	if hasPayload {
		env["payload"] = payload
	} else {
		env["fields"] = f
	}
	b, _ := json.Marshal(env)
	return b, mid, event
}

// signature is hex(HMAC-SHA256(secret, timestamp + "." + body)). The receiver
// recomputes it from the X-Snugkv-Timestamp header and the raw body, and
// should reject timestamps that are too old.
func signature(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// attempt sends one request.
func (m *Manager) attempt(ctx context.Context, oc *outboundConfig, body []byte, mid, event string, n int) delivery {
	o := oc.def.Outbound
	ctx, cancel := context.WithTimeout(ctx, time.Duration(o.TimeoutMS)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, o.Method, o.URL, bytes.NewReader(body))
	if err != nil {
		return delivery{Err: err.Error()}
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "snugkv-webhooks/1")
	for _, h := range oc.headers {
		req.Header.Set(h[0], h[1])
	}
	req.Header.Set("X-Snugkv-Webhook-Id", oc.def.ID)
	req.Header.Set("X-Snugkv-Message-Id", mid)
	req.Header.Set("X-Snugkv-Delivery-Attempt", strconv.Itoa(n))
	req.Header.Set("X-Snugkv-Timestamp", ts)
	if event != "" {
		req.Header.Set("X-Snugkv-Event", event)
	}
	if oc.signing != "" {
		req.Header.Set("X-Snugkv-Signature", signature(oc.signing, ts, body))
	}
	start := time.Now()
	resp, err := m.http.Do(req)
	d := delivery{Millis: time.Since(start).Milliseconds()}
	if err != nil {
		d.Err = shortErr(err)
		d.Retryable = !strings.Contains(d.Err, "is not allowed") && ctx.Err() != context.Canceled
		return d
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	d.Status = resp.StatusCode
	d.Body = strings.TrimSpace(string(snippet))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return d
	}
	d.Err = "HTTP " + strconv.Itoa(resp.StatusCode)
	switch {
	case resp.StatusCode == 408 || resp.StatusCode == 425 || resp.StatusCode == 429 || resp.StatusCode >= 500:
		d.Retryable = true
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			d.RetryAfter = min(time.Duration(s)*time.Second, 2*time.Minute)
		}
	}
	return d
}

func shortErr(err error) string {
	var ue interface{ Unwrap() error }
	msg := err.Error()
	if errors.As(err, &ue) {
		if inner := errors.Unwrap(err); inner != nil && len(msg) > 200 {
			msg = inner.Error()
		}
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

func backoff(n int) time.Duration {
	d := time.Second << min(n-1, 6)
	d = min(d, time.Minute)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// deliverEntry tries one entry up to MaxAttempts times. It returns false when
// ctx ended first, in which case the entry must stay pending.
func (m *Manager) deliverEntry(ctx context.Context, oc *outboundConfig, e streamEntry) bool {
	def := oc.def
	body, mid, event := buildBody(def, e)
	o := def.Outbound
	var last delivery
	for n := 1; n <= o.MaxAttempts; n++ {
		if ctx.Err() != nil {
			return false
		}
		last = m.attempt(ctx, oc, body, mid, event, n)
		entry := LogEntry{Direction: "out", Status: last.Status, Attempt: n, MessageID: e.id, Millis: last.Millis, Error: last.Err}
		if last.ok() {
			entry.Result = "ok"
			m.store.log(def.ID, entry)
			return true
		}
		if !last.Retryable || n == o.MaxAttempts {
			break
		}
		entry.Result = "retry"
		m.store.log(def.ID, entry)
		wait := backoff(n)
		if last.RetryAfter > wait {
			wait = last.RetryAfter
		}
		if !sleepCtx(ctx, wait) {
			return false
		}
	}
	if ctx.Err() != nil {
		return false
	}
	m.store.log(def.ID, LogEntry{Direction: "out", Result: "failed", Status: last.Status, Attempt: o.MaxAttempts, MessageID: e.id, Millis: last.Millis, Error: last.Err})
	if def.deadLetter() {
		args := []string{"XADD", def.Topic + ".dlq", "MAXLEN", "~", "10000", "*"}
		for k, v := range e.fields {
			args = append(args, k, v)
		}
		args = append(args, "dlq_error", last.Err, "dlq_webhook", def.ID, "dlq_source_id", e.id)
		if _, err := m.kv.do(args...); err != nil {
			m.logf("webhook %s: dead letter failed: %v", def.ID, err)
			return false // keep it pending rather than lose it
		}
	}
	return true
}

// runOutbound consumes the topic through a consumer group until ctx ends.
// Entries are delivered one at a time so order is kept; the group makes
// restarts safe: unacknowledged entries are delivered again.
func (m *Manager) runOutbound(ctx context.Context, oc *outboundConfig, setErr func(string)) {
	def := oc.def
	group := def.group()
	const consumer = "snugkv-webhook"
	kv := newKVClient(m.kvAddr, m.opts.KVUser, m.opts.KVPass, 1, 15*time.Second)
	defer kv.close()
	pendingFirst := true
	failures := 0
	for ctx.Err() == nil {
		_, err := kv.do("XGROUP", "CREATE", def.Topic, group, def.Outbound.From, "MKSTREAM")
		if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
			failures++
			setErr(err.Error())
			sleepCtx(ctx, backoff(failures))
			continue
		}
		setErr("")
		failures = 0
		if err := m.consumeLoop(ctx, kv, oc, group, consumer, &pendingFirst); err != nil && ctx.Err() == nil {
			failures++
			setErr(err.Error())
			sleepCtx(ctx, backoff(failures))
		}
	}
}

func (m *Manager) consumeLoop(ctx context.Context, kv *kvClient, oc *outboundConfig, group, consumer string, pendingFirst *bool) error {
	def := oc.def
	for ctx.Err() == nil {
		id := ">"
		args := []string{"XREADGROUP", "GROUP", group, consumer, "COUNT", "10"}
		if *pendingFirst {
			id = "0" // entries delivered to us earlier but never acknowledged
		} else {
			args = append(args, "BLOCK", "2000")
		}
		args = append(args, "STREAMS", def.Topic, id)
		v, err := kv.doTimeout(15*time.Second, args...)
		if err != nil {
			return err
		}
		var entries []streamEntry
		if len(v.arr) > 0 && len(v.arr[0].arr) == 2 {
			entries = parseEntries(v.arr[0].arr[1])
		}
		if *pendingFirst && len(entries) == 0 {
			*pendingFirst = false
			continue
		}
		for _, e := range entries {
			if len(e.fields) == 0 { // the entry was trimmed or deleted after delivery
				if _, err := kv.do("XACK", def.Topic, group, e.id); err != nil {
					return err
				}
				continue
			}
			if !m.deliverEntry(ctx, oc, e) {
				return ctx.Err()
			}
			if _, err := kv.do("XACK", def.Topic, group, e.id); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

// TestResult is what the "send a test" button reports.
type TestResult struct {
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Millis int64  `json:"ms"`
	Error  string `json:"error,omitempty"`
	Body   string `json:"response,omitempty"`
}

// sendTest delivers one sample message once, without touching the stream.
func (m *Manager) sendTest(ctx context.Context, d Definition) (TestResult, error) {
	oc, err := resolveOutbound(d, m.sealer)
	if err != nil {
		return TestResult{}, err
	}
	sample := streamEntry{id: strconv.FormatInt(time.Now().UnixMilli(), 10) + "-0", fields: map[string]string{
		"payload": `{"test":true,"message":"Hello from SnugKV webhooks"}`, "event": "webhook.test",
		"mid": "test-" + newID(), "ts": strconv.FormatInt(time.Now().UnixMilli(), 10), "producer": "snugkv-webhooks",
	}}
	body, mid, event := buildBody(d, sample)
	r := m.attempt(ctx, oc, body, mid, event, 1)
	return TestResult{OK: r.ok(), Status: r.Status, Millis: r.Millis, Error: r.Err, Body: r.Body}, nil
}
