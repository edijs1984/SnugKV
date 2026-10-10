package rpccache

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures a Proxy.
type Config struct {
	Chain    Chain
	Upstream string
	// Headers are added to every upstream request (for example an API key).
	Headers http.Header
	TTLs    TTLs
	Cache   Cache
	Client  *http.Client
	// MaxRequestBytes bounds a client request body. MaxEntryBytes bounds a
	// result that is stored; larger results are served but not cached.
	MaxRequestBytes int64
	MaxEntryBytes   int
	// BatchParallelism bounds concurrent upstream calls for one batch.
	BatchParallelism int
}

// Proxy is an http.Handler.
type Proxy struct {
	cfg Config

	mu      sync.Mutex
	flights map[string]*flight

	cacheDownUntil atomic.Int64 // unix nanos; cache calls are skipped until then

	hits, misses, bypass, coalesced, upstreamErrors, cacheErrors, stored, tooLarge atomic.Uint64
	methods                                                                        sync.Map // string -> *methodStats
}

type methodStats struct{ hit, miss, bypass, coalesced atomic.Uint64 }

// New returns a Proxy. It fills defaults for unset fields.
func New(cfg Config) (*Proxy, error) {
	if cfg.Upstream == "" {
		return nil, fmt.Errorf("upstream URL is required")
	}
	if cfg.Cache == nil {
		return nil, fmt.Errorf("cache is required")
	}
	if cfg.Chain == "" {
		cfg.Chain = Solana
	}
	if cfg.Chain != Solana && cfg.Chain != EVM {
		return nil, fmt.Errorf("unknown chain %q", cfg.Chain)
	}
	if cfg.TTLs == (TTLs{}) {
		cfg.TTLs = DefaultTTLs()
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        512,
				MaxIdleConnsPerHost: 256,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}
	if cfg.MaxRequestBytes <= 0 {
		cfg.MaxRequestBytes = 1 << 20
	}
	if cfg.MaxEntryBytes <= 0 {
		cfg.MaxEntryBytes = 1 << 20
	}
	if cfg.BatchParallelism <= 0 {
		cfg.BatchParallelism = 16
	}
	return &Proxy{cfg: cfg, flights: make(map[string]*flight)}, nil
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

// outcome of resolving one request.
type outcome struct {
	status int
	body   []byte
	cache  string // HIT, MISS, COALESCED, BYPASS
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		w.Write([]byte("ok\n"))
		return
	case r.Method == http.MethodGet && r.URL.Path == "/metrics":
		p.writeMetrics(w)
		return
	case r.Method != http.MethodPost:
		http.Error(w, "POST a JSON-RPC request", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.cfg.MaxRequestBytes))
	if err != nil {
		http.Error(w, "request too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	trimmed := bytes.TrimSpace(body)

	w.Header().Set("Content-Type", "application/json")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		out := p.handleBatch(trimmed)
		w.Header().Set("X-Cache", out.cache)
		w.WriteHeader(out.status)
		w.Write(out.body)
		return
	}
	out := p.handleOne(trimmed)
	w.Header().Set("X-Cache", out.cache)
	w.WriteHeader(out.status)
	w.Write(out.body)
}

func (p *Proxy) handleOne(raw []byte) outcome {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil || req.Method == "" {
		return p.passthrough(raw, "")
	}
	class := Classify(p.cfg.Chain, req.Method, req.Params)
	if class == Bypass {
		return p.passthrough(raw, req.Method)
	}

	key := p.cacheKey(req.Method, req.Params)
	ms := p.methodStats(req.Method)

	if v, ok := p.cacheGet(key); ok {
		p.hits.Add(1)
		ms.hit.Add(1)
		return outcome{status: http.StatusOK, body: respond(req.ID, v), cache: "HIT"}
	}

	res, leader := p.fetch(key, req, class)
	label := "MISS"
	if leader {
		p.misses.Add(1)
		ms.miss.Add(1)
	} else {
		p.coalesced.Add(1)
		ms.coalesced.Add(1)
		label = "COALESCED"
	}
	if res.raw != nil {
		return outcome{status: res.status, body: res.raw, cache: label}
	}
	if res.rpcError != nil {
		return outcome{status: http.StatusOK, body: respondError(req.ID, res.rpcError), cache: label}
	}
	return outcome{status: http.StatusOK, body: respond(req.ID, res.result), cache: label}
}

// flight is one in-progress upstream call shared by identical requests.
type flight struct {
	done chan struct{}
	res  upstreamResult
}

type upstreamResult struct {
	result   json.RawMessage
	rpcError json.RawMessage
	status   int
	raw      []byte // set when the upstream reply must be relayed untouched
}

func (p *Proxy) fetch(key string, req request, class Class) (upstreamResult, bool) {
	p.mu.Lock()
	if f, ok := p.flights[key]; ok {
		p.mu.Unlock()
		<-f.done
		return f.res, false
	}
	f := &flight{done: make(chan struct{})}
	p.flights[key] = f
	p.mu.Unlock()

	f.res = p.callUpstream(req)
	if f.res.raw == nil && f.res.rpcError == nil && !isNull(f.res.result) {
		p.cacheSet(key, f.res.result, p.cfg.TTLs.For(class))
	}

	p.mu.Lock()
	delete(p.flights, key)
	p.mu.Unlock()
	close(f.done)
	return f.res, true
}

// callUpstream sends a normalized request (fixed id) so its answer can be
// shared by every caller.
func (p *Proxy) callUpstream(req request) upstreamResult {
	params := req.Params
	if len(bytes.TrimSpace(params)) == 0 {
		params = json.RawMessage("[]")
	}
	body, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", 1, req.Method, params})

	status, reply, err := p.post(body)
	if err != nil {
		p.upstreamErrors.Add(1)
		return upstreamResult{status: http.StatusBadGateway, raw: errorBody(nil, -32603, "upstream unavailable")}
	}
	if status != http.StatusOK {
		p.upstreamErrors.Add(1)
		return upstreamResult{status: status, raw: reply}
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(reply, &resp) != nil || (resp.Result == nil && resp.Error == nil) {
		return upstreamResult{status: status, raw: reply}
	}
	if resp.Error != nil && !isNull(resp.Error) {
		return upstreamResult{status: status, rpcError: resp.Error}
	}
	return upstreamResult{status: status, result: resp.Result}
}

func (p *Proxy) post(body []byte) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, p.cfg.Upstream, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range p.cfg.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	return resp.StatusCode, reply, err
}

func (p *Proxy) passthrough(raw []byte, method string) outcome {
	p.bypass.Add(1)
	if method != "" {
		p.methodStats(method).bypass.Add(1)
	}
	status, reply, err := p.post(raw)
	if err != nil {
		p.upstreamErrors.Add(1)
		return outcome{status: http.StatusBadGateway, body: errorBody(nil, -32603, "upstream unavailable"), cache: "BYPASS"}
	}
	return outcome{status: status, body: reply, cache: "BYPASS"}
}

func (p *Proxy) handleBatch(raw []byte) outcome {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return outcome{status: http.StatusOK, body: errorBody(nil, -32600, "Invalid Request"), cache: "BYPASS"}
	}
	results := make([]outcome, len(items))
	sem := make(chan struct{}, p.cfg.BatchParallelism)
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, item json.RawMessage) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = p.handleOne(bytes.TrimSpace(item))
		}(i, item)
	}
	wg.Wait()

	var buf bytes.Buffer
	buf.WriteByte('[')
	hit, miss, byp := 0, 0, 0
	for i, r := range results {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(bytes.TrimSpace(r.body))
		switch r.cache {
		case "HIT":
			hit++
		case "BYPASS":
			byp++
		default:
			miss++
		}
	}
	buf.WriteByte(']')
	return outcome{
		status: http.StatusOK,
		body:   buf.Bytes(),
		cache:  fmt.Sprintf("BATCH hit=%d miss=%d bypass=%d", hit, miss, byp),
	}
}

// ---- cache access ----

func (p *Proxy) cacheKey(method string, params json.RawMessage) string {
	var compact bytes.Buffer
	if len(bytes.TrimSpace(params)) == 0 || json.Compact(&compact, params) != nil {
		compact.Reset()
		compact.WriteString("[]")
	}
	h := sha256.New()
	h.Write([]byte(p.cfg.Chain))
	h.Write([]byte{0})
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write(compact.Bytes())
	sum := h.Sum(nil)
	// 'r' plus 128 bits of the digest: a 17-byte binary key.
	return "r" + string(sum[:16])
}

func (p *Proxy) cacheUsable() bool {
	return time.Now().UnixNano() >= p.cacheDownUntil.Load()
}

func (p *Proxy) cacheFailed() {
	p.cacheErrors.Add(1)
	// Skip the cache for a moment instead of paying a timeout on every request.
	p.cacheDownUntil.Store(time.Now().Add(time.Second).UnixNano())
}

func (p *Proxy) cacheGet(key string) ([]byte, bool) {
	if !p.cacheUsable() {
		return nil, false
	}
	v, ok, err := p.cfg.Cache.Get([]byte(key))
	if err != nil {
		p.cacheFailed()
		return nil, false
	}
	return v, ok
}

func (p *Proxy) cacheSet(key string, result []byte, ttl time.Duration) {
	if ttl <= 0 || !p.cacheUsable() {
		return
	}
	if len(result) > p.cfg.MaxEntryBytes {
		p.tooLarge.Add(1)
		return
	}
	if err := p.cfg.Cache.Set([]byte(key), result, ttl); err != nil {
		p.cacheFailed()
		return
	}
	p.stored.Add(1)
}

// ---- responses ----

func respond(id, result []byte) []byte {
	if len(bytes.TrimSpace(id)) == 0 {
		id = []byte("null")
	}
	out := make([]byte, 0, len(result)+len(id)+40)
	out = append(out, `{"jsonrpc":"2.0","result":`...)
	out = append(out, result...)
	out = append(out, `,"id":`...)
	out = append(out, id...)
	return append(out, '}')
}

func respondError(id, rpcErr []byte) []byte {
	if len(bytes.TrimSpace(id)) == 0 {
		id = []byte("null")
	}
	out := make([]byte, 0, len(rpcErr)+len(id)+40)
	out = append(out, `{"jsonrpc":"2.0","error":`...)
	out = append(out, rpcErr...)
	out = append(out, `,"id":`...)
	out = append(out, id...)
	return append(out, '}')
}

func errorBody(id []byte, code int, msg string) []byte {
	e, _ := json.Marshal(map[string]any{"code": code, "message": msg})
	return respondError(id, e)
}

func isNull(b []byte) bool { return string(bytes.TrimSpace(b)) == "null" }

// ---- metrics ----

func (p *Proxy) methodStats(m string) *methodStats {
	if v, ok := p.methods.Load(m); ok {
		return v.(*methodStats)
	}
	v, _ := p.methods.LoadOrStore(m, &methodStats{})
	return v.(*methodStats)
}

// Stats is a snapshot of the proxy counters.
type Stats struct {
	Hits, Misses, Bypass, Coalesced, UpstreamErrors, CacheErrors, Stored, TooLarge uint64
}

func (p *Proxy) Stats() Stats {
	return Stats{p.hits.Load(), p.misses.Load(), p.bypass.Load(), p.coalesced.Load(),
		p.upstreamErrors.Load(), p.cacheErrors.Load(), p.stored.Load(), p.tooLarge.Load()}
}

func (p *Proxy) writeMetrics(w http.ResponseWriter) {
	s := p.Stats()
	var b strings.Builder
	line := func(name string, v uint64) { fmt.Fprintf(&b, "%s %d\n", name, v) }
	line("rpccache_hits_total", s.Hits)
	line("rpccache_misses_total", s.Misses)
	line("rpccache_coalesced_total", s.Coalesced)
	line("rpccache_bypass_total", s.Bypass)
	line("rpccache_stored_total", s.Stored)
	line("rpccache_too_large_total", s.TooLarge)
	line("rpccache_upstream_errors_total", s.UpstreamErrors)
	line("rpccache_cache_errors_total", s.CacheErrors)
	var names []string
	p.methods.Range(func(k, _ any) bool { names = append(names, k.(string)); return true })
	sort.Strings(names)
	for _, m := range names {
		ms := p.methodStats(m)
		fmt.Fprintf(&b, "rpccache_method_hits_total{method=%q} %d\n", m, ms.hit.Load())
		fmt.Fprintf(&b, "rpccache_method_misses_total{method=%q} %d\n", m, ms.miss.Load())
		fmt.Fprintf(&b, "rpccache_method_coalesced_total{method=%q} %d\n", m, ms.coalesced.Load())
		fmt.Fprintf(&b, "rpccache_method_bypass_total{method=%q} %d\n", m, ms.bypass.Load())
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Write([]byte(b.String()))
}
