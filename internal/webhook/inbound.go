package webhook

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxInboundBody = 1 << 20

// inboundState is the live part of an inbound webhook: its secret in the
// clear (decrypted once), a rate limiter and counters that are flushed to the
// store in batches so a busy endpoint does not turn every request into extra
// writes.
type inboundState struct {
	def    Definition
	secret string
	secErr error

	mu     sync.Mutex
	tokens float64
	last   time.Time

	okPending atomic.Int64
	lastLog   atomic.Int64 // unix ms of the last logged success
}

func newInboundState(d Definition, s *sealer) *inboundState {
	st := &inboundState{def: d, tokens: float64(d.Inbound.MaxPerSecond), last: time.Now()}
	if d.Inbound.Auth.Mode != "none" {
		st.secret, st.secErr = s.open(d.Inbound.Auth.Secret.Enc)
	}
	return st
}

func (st *inboundState) allow() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	rate := float64(st.def.Inbound.MaxPerSecond)
	st.tokens = min(rate*2, st.tokens+now.Sub(st.last).Seconds()*rate)
	st.last = now
	if st.tokens < 1 {
		return false
	}
	st.tokens--
	return true
}

// verify checks the sender's proof. It returns a short reason on failure.
func (st *inboundState) verify(r *http.Request, body []byte) string {
	a := st.def.Inbound.Auth
	switch a.Mode {
	case "none":
		return ""
	case "token":
		got := r.Header.Get(a.Header)
		if strings.EqualFold(a.Header, "Authorization") {
			got = strings.TrimPrefix(got, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(st.secret)) != 1 {
			return "bad token"
		}
		return ""
	case "hmac":
		var h func() hash.Hash = sha256.New
		if a.Algo == "sha1" {
			h = sha1.New
		}
		mac := hmac.New(h, []byte(st.secret))
		mac.Write(body)
		want := mac.Sum(nil)
		got := strings.TrimPrefix(strings.TrimSpace(r.Header.Get(a.Header)), a.Prefix)
		gotBytes, err := hex.DecodeString(got)
		if err != nil || !hmac.Equal(gotBytes, want) {
			return "bad signature"
		}
		return ""
	}
	return "unknown auth mode"
}

var sensitiveHeader = []string{"auth", "token", "secret", "key", "signature", "cookie", "password"}

// keptHeaders picks the request headers worth passing on: content type, user
// agent and X-* headers, never anything that looks like a credential.
func keptHeaders(r *http.Request, authHeader string) map[string]string {
	out := map[string]string{}
	for name, vals := range r.Header {
		l := strings.ToLower(name)
		if len(vals) == 0 || strings.EqualFold(name, authHeader) {
			continue
		}
		if l != "content-type" && l != "user-agent" && !strings.HasPrefix(l, "x-") {
			continue
		}
		bad := false
		for _, s := range sensitiveHeader {
			if strings.Contains(l, s) {
				bad = true
			}
		}
		if bad || len(out) >= 20 {
			continue
		}
		v := vals[0]
		if len(v) > 512 {
			v = v[:512]
		}
		out[l] = v
	}
	return out
}

func (m *Manager) handleHook(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/hook/")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		httpJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}
	st := m.inbound(id)
	if st == nil {
		httpJSON(w, http.StatusNotFound, map[string]string{"error": "no such webhook"})
		return
	}
	if !m.isPrimary() {
		httpJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this node is a replica"})
		return
	}
	if !st.allow() {
		w.Header().Set("Retry-After", "1")
		httpJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInboundBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body larger than 1 MiB"})
		} else {
			httpJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read body"})
		}
		return
	}
	if st.secErr != nil {
		m.logf("webhook %s: %v", id, st.secErr)
		httpJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook secret is unavailable"})
		return
	}
	if reason := st.verify(r, body); reason != "" {
		m.store.log(id, LogEntry{Direction: "in", Result: "rejected", Status: http.StatusUnauthorized, Error: reason})
		httpJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if len(body) == 0 {
		httpJSON(w, http.StatusBadRequest, map[string]string{"error": "empty body"})
		return
	}
	in := st.def.Inbound
	event := in.Event
	if event == "" && in.EventHeader != "" {
		event = r.Header.Get(in.EventHeader)
	}
	payload := json.RawMessage(body)
	if !json.Valid(body) {
		payload, _ = json.Marshal(string(body))
	}
	now := time.Now()
	args := []string{"XADD", st.def.Topic}
	if in.MaxLen > 0 {
		args = append(args, "MAXLEN", "~", strconv.Itoa(in.MaxLen))
	}
	mid := newID() + newID()[:8]
	args = append(args, "*", "payload", string(payload), "mid", mid, "ts", strconv.FormatInt(now.UnixMilli(), 10), "producer", "webhook:"+st.def.Name)
	if event != "" {
		args = append(args, "event", event)
	}
	if hs := keptHeaders(r, in.Auth.Header); len(hs) > 0 {
		hb, _ := json.Marshal(hs)
		args = append(args, "headers", string(hb))
	}
	start := time.Now()
	v, err := m.kv.do(args...)
	if err != nil {
		m.store.log(id, LogEntry{Direction: "in", Result: "failed", Status: http.StatusBadGateway, Error: err.Error(), Millis: time.Since(start).Milliseconds()})
		httpJSON(w, http.StatusBadGateway, map[string]string{"error": "could not publish"})
		return
	}
	st.okPending.Add(1)
	if last := st.lastLog.Load(); now.UnixMilli()-last >= 1000 && st.lastLog.CompareAndSwap(last, now.UnixMilli()) {
		m.store.logOnly(id, LogEntry{TimeMS: now.UnixMilli(), Direction: "in", Result: "ok", Status: http.StatusAccepted, MessageID: v.str, Millis: time.Since(start).Milliseconds()})
	}
	httpJSON(w, http.StatusAccepted, map[string]string{"id": v.str, "mid": mid})
}

func httpJSON(w http.ResponseWriter, status int, body any) {
	b, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}
