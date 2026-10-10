package rpccache

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AuthConfig configures the API-key middleware.
type AuthConfig struct {
	// Store holds keys, plans, rate-limit buckets and usage. Rate limiting
	// needs SnugKV (the snug_rate_limit built-in function).
	Store *Client
	// RecordTTL is how long a key or plan record is trusted before it is read
	// again; it bounds how fast a revocation or plan change takes effect.
	RecordTTL time.Duration
	// FlushEvery is how often buffered usage is written to the store.
	FlushEvery time.Duration
	// MaxRequestBytes should match the proxy's limit.
	MaxRequestBytes int64
	// Chain selects the method table used to split usage by class.
	Chain Chain
}

// Auth is an http.Handler that checks API keys, applies each key's plan and
// meters usage before handing the request to the wrapped handler.
//
// Failure behaviour: a key the store cannot confirm is refused with 503 unless
// a record was loaded earlier (then the old record is used). Rate limiting and
// usage metering fail open, so a store outage costs accuracy, not availability.
type Auth struct {
	cfg  AuthConfig
	next http.Handler
	now  func() time.Time

	mu      sync.RWMutex
	entries map[string]*keyEntry

	pmu     sync.Mutex
	pending map[string]*usageAcc // id|day

	stop chan struct{}
	done chan struct{}

	nOK, nUnauthorized, nRateLimited, nQuota, nStoreErrors, nBadPlan atomic.Uint64
}

type snapshot struct {
	found   bool
	rec     KeyRecord
	plan    Plan
	planOK  bool
	loaded  time.Time
	day     string
	base    int64 // calls today as of load
	failure bool  // the load failed and this is a stand-in
}

type keyEntry struct {
	snap  atomic.Pointer[snapshot]
	local atomic.Int64 // calls admitted since the snapshot
	load  sync.Mutex
}

const maxEntries = 10000

type usageAcc struct{ n [8]int64 }

func fieldIndex(name string) int {
	for i, f := range usageFields {
		if f == name {
			return i
		}
	}
	return 0
}

// NewAuth wraps next. Call Close to flush buffered usage.
func NewAuth(cfg AuthConfig, next http.Handler) (*Auth, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("auth store is required")
	}
	if cfg.RecordTTL <= 0 {
		cfg.RecordTTL = 5 * time.Second
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = time.Second
	}
	if cfg.MaxRequestBytes <= 0 {
		cfg.MaxRequestBytes = 1 << 20
	}
	if cfg.Chain == "" {
		cfg.Chain = Solana
	}
	a := &Auth{
		cfg: cfg, next: next, now: time.Now,
		entries: map[string]*keyEntry{},
		pending: map[string]*usageAcc{},
		stop:    make(chan struct{}), done: make(chan struct{}),
	}
	go a.flushLoop()
	return a, nil
}

// Close stops the background flush and writes what is buffered.
func (a *Auth) Close() {
	close(a.stop)
	<-a.done
}

func (a *Auth) flushLoop() {
	defer close(a.done)
	t := time.NewTicker(a.cfg.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			a.Flush()
		case <-a.stop:
			a.Flush()
			return
		}
	}
}

// Flush writes buffered usage to the store. On failure the counts are kept
// for the next attempt.
func (a *Auth) Flush() {
	a.pmu.Lock()
	batch := a.pending
	a.pending = map[string]*usageAcc{}
	a.pmu.Unlock()
	if len(batch) == 0 {
		return
	}
	var cmds [][]string
	ttl := strconv.FormatInt(int64(usageTTL/time.Second), 10)
	for k, acc := range batch {
		id, day, _ := strings.Cut(k, "|")
		key := usageKey(id, day)
		any := false
		for i, n := range acc.n {
			if n != 0 {
				cmds = append(cmds, []string{"HINCRBY", key, usageFields[i], strconv.FormatInt(n, 10)})
				any = true
			}
		}
		if any {
			cmds = append(cmds, []string{"EXPIRE", key, ttl})
		}
	}
	if len(cmds) == 0 {
		return
	}
	if _, err := a.cfg.Store.Pipeline(cmds); err != nil {
		a.nStoreErrors.Add(1)
		a.pmu.Lock()
		for k, acc := range batch {
			dst := a.pending[k]
			if dst == nil {
				dst = &usageAcc{}
				a.pending[k] = dst
			}
			for i, n := range acc.n {
				dst.n[i] += n
			}
		}
		a.pmu.Unlock()
	}
}

func (a *Auth) record(id, day string, deltas map[string]int64) {
	a.pmu.Lock()
	defer a.pmu.Unlock()
	k := id + "|" + day
	acc := a.pending[k]
	if acc == nil {
		acc = &usageAcc{}
		a.pending[k] = acc
	}
	for f, n := range deltas {
		acc.n[fieldIndex(f)] += n
	}
}

func (a *Auth) unflushedCalls(id, day string) int64 {
	a.pmu.Lock()
	defer a.pmu.Unlock()
	if acc := a.pending[id+"|"+day]; acc != nil {
		return acc.n[0]
	}
	return 0
}

// secretFrom reads the API key from the request.
func secretFrom(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if h := r.Header.Get("X-API-Key"); h != "" {
		return strings.TrimSpace(h)
	}
	return r.URL.Query().Get("api-key")
}

func (a *Auth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/healthz":
			a.next.ServeHTTP(w, r)
			return
		case "/metrics":
			a.next.ServeHTTP(w, r)
			a.writeMetrics(w)
			return
		}
	}
	if r.Method != http.MethodPost {
		a.next.ServeHTTP(w, r) // the proxy answers 405
		return
	}

	secret := secretFrom(r)
	if secret == "" {
		a.reject(w, http.StatusUnauthorized, -32001, "API key required", 0)
		a.nUnauthorized.Add(1)
		return
	}
	id := KeyID(secret)
	now := a.now()
	day := dayOf(now)
	e := a.lookup(id, day)
	snap := e.snap.Load()
	switch {
	case snap.failure && !snap.found:
		a.reject(w, http.StatusServiceUnavailable, -32603, "authentication unavailable", 1)
		return
	case !snap.found || snap.rec.Revoked:
		a.nUnauthorized.Add(1)
		a.reject(w, http.StatusUnauthorized, -32001, "invalid API key", 0)
		return
	case !snap.planOK:
		a.nBadPlan.Add(1)
		a.reject(w, http.StatusForbidden, -32001, "API key has no valid plan", 0)
		return
	}

	// Read the body to count and classify calls, then hand it on unchanged.
	limit := a.cfg.MaxRequestBytes
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		http.Error(w, "unreadable request", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > limit {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	calls := a.classify(body)
	n := int64(0)
	for _, c := range calls {
		n += c.n
	}
	plan := snap.plan

	if int64(plan.Burst) < n {
		a.nRateLimited.Add(1)
		a.record(id, day, map[string]int64{"denied": n})
		a.reject(w, http.StatusTooManyRequests, -32005,
			fmt.Sprintf("batch of %d calls exceeds the plan burst of %d", n, plan.Burst), 1)
		return
	}
	if plan.Daily > 0 && snap.base+e.local.Load()+n > plan.Daily {
		a.nQuota.Add(1)
		a.record(id, day, map[string]int64{"denied": n})
		secs := int(time.Until(nextMidnight(now)).Seconds()) + 1
		a.reject(w, http.StatusTooManyRequests, -32005, "daily quota exceeded", secs)
		return
	}
	if ok, retry := a.takeTokens(id, plan, n); !ok {
		a.nRateLimited.Add(1)
		a.record(id, day, map[string]int64{"denied": n})
		secs := int((retry + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		a.reject(w, http.StatusTooManyRequests, -32005, "rate limit exceeded", secs)
		return
	}

	e.local.Add(n)
	deltas := map[string]int64{"calls": n}
	for _, c := range calls {
		deltas[classField(c.class)] += c.n
	}
	a.record(id, day, deltas)
	a.nOK.Add(1)

	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	a.next.ServeHTTP(w, r)
}

type classCount struct {
	class Class
	n     int64
}

// classify splits a request body into calls by cache class. A body that does
// not parse is one bypass call, which the proxy forwards untouched.
func (a *Auth) classify(body []byte) []classCount {
	t := bytes.TrimSpace(body)
	var items []json.RawMessage
	if len(t) > 0 && t[0] == '[' {
		if json.Unmarshal(t, &items) != nil || len(items) == 0 {
			return []classCount{{Bypass, 1}}
		}
	} else {
		items = []json.RawMessage{t}
	}
	var counts [8]int64
	for _, it := range items {
		var req request
		if json.Unmarshal(it, &req) != nil || req.Method == "" {
			counts[Bypass]++
			continue
		}
		counts[Classify(a.cfg.Chain, req.Method, req.Params)]++
	}
	var out []classCount
	for c, n := range counts {
		if n > 0 {
			out = append(out, classCount{Class(c), n})
		}
	}
	return out
}

func nextMidnight(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
}

// lookup returns the entry for id, refreshing it when its record is stale or
// the UTC day changed.
func (a *Auth) lookup(id, day string) *keyEntry {
	a.mu.RLock()
	e := a.entries[id]
	a.mu.RUnlock()
	if e == nil {
		a.mu.Lock()
		if e = a.entries[id]; e == nil {
			if len(a.entries) >= maxEntries {
				// Bound memory against floods of made-up keys. Valid keys reload on
				// their next request.
				a.entries = map[string]*keyEntry{}
			}
			e = &keyEntry{}
			a.entries[id] = e
		}
		a.mu.Unlock()
	}
	s := e.snap.Load()
	ttl := a.cfg.RecordTTL
	if s != nil && s.failure && ttl > time.Second {
		ttl = time.Second // retry a failed load sooner
	}
	if s != nil && s.day == day && a.now().Sub(s.loaded) < ttl {
		return e
	}
	if s == nil {
		e.load.Lock() // first load: everyone waits for it
	} else if !e.load.TryLock() {
		return e // someone else is refreshing; serve the old snapshot
	}
	defer e.load.Unlock()
	if cur := e.snap.Load(); cur != nil && cur != s {
		return e
	}
	ns := a.load(id, day)
	if ns.failure && s != nil && s.found {
		// Keep trusting the last good record while the store is unreachable.
		keep := *s
		keep.loaded = a.now()
		keep.failure = true
		ns = &keep
	} else if !ns.failure {
		// The store count lags our own un-flushed calls; add them back.
		ns.base += a.unflushedCalls(id, day)
	}
	e.snap.Store(ns)
	if !ns.failure {
		e.local.Store(0)
	}
	return e
}

func (a *Auth) load(id, day string) *snapshot {
	s := &snapshot{loaded: a.now(), day: day}
	vs, err := a.cfg.Store.Pipeline([][]string{
		{"GET", keyPrefix + id},
		{"HGET", usageKey(id, day), "calls"},
	})
	if err != nil {
		a.nStoreErrors.Add(1)
		s.failure = true
		return s
	}
	if vs[0].Nil {
		return s
	}
	if json.Unmarshal([]byte(vs[0].Str), &s.rec) != nil {
		return s
	}
	s.found = true
	s.base, _ = vs[1].AsInt()
	pv, err := a.cfg.Store.Do("GET", planPrefix+s.rec.Plan)
	if err != nil {
		a.nStoreErrors.Add(1)
		s.failure = true
		return s
	}
	if !pv.Nil && json.Unmarshal([]byte(pv.Str), &s.plan) == nil && s.plan.validate() == nil {
		s.planOK = true
	}
	return s
}

// takeTokens charges n calls to the key's bucket. A store failure allows the
// request.
func (a *Auth) takeTokens(id string, p Plan, n int64) (bool, time.Duration) {
	v, err := a.cfg.Store.Do("FCALL", "snug_rate_limit", "1", limitPrefix+id,
		strconv.Itoa(p.Burst), strconv.FormatFloat(p.RPS, 'g', -1, 64), strconv.FormatInt(n, 10))
	if err != nil || len(v.Array) < 3 {
		a.nStoreErrors.Add(1)
		return true, 0
	}
	allowed, _ := v.Array[0].AsInt()
	retryMs, _ := v.Array[2].AsInt()
	return allowed == 1, time.Duration(retryMs) * time.Millisecond
}

func (a *Auth) reject(w http.ResponseWriter, status, code int, msg string, retryAfter int) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	w.Write(errorBody(nil, code, msg))
}

func (a *Auth) writeMetrics(w http.ResponseWriter) {
	var b strings.Builder
	rows := []struct {
		result string
		v      uint64
	}{
		{"ok", a.nOK.Load()}, {"unauthorized", a.nUnauthorized.Load()},
		{"rate_limited", a.nRateLimited.Load()}, {"quota_exceeded", a.nQuota.Load()},
		{"bad_plan", a.nBadPlan.Load()},
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].result < rows[j].result })
	for _, r := range rows {
		fmt.Fprintf(&b, "rpcauth_requests_total{result=%q} %d\n", r.result, r.v)
	}
	fmt.Fprintf(&b, "rpcauth_store_errors_total %d\n", a.nStoreErrors.Load())
	w.Write([]byte(b.String()))
}
