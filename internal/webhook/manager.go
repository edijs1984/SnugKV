// Package webhook turns SnugKV streams into HTTP and back: inbound webhooks
// publish incoming requests into a stream, outbound webhooks deliver a
// stream's messages to a URL with retries and a dead-letter topic.
//
// The module is optional and off by default. It talks to the server over its
// own loopback connection, so it needs no special access to the engine and
// everything it stores is journaled and replicated like any other data.
package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configure the module.
type Options struct {
	// KVAddr is the SnugKV address the module connects to (the server's own listen address).
	KVAddr, KVUser, KVPass string
	// PublicListen serves POST /hook/<id>. Empty disables inbound webhooks.
	PublicListen string
	// APIListen serves the management API. Empty disables it (webhooks can then only be changed with the store directly).
	APIListen string
	// APIToken is the bearer token the management API requires. It is required unless APIListen is a loopback address.
	APIToken string
	// Key encrypts stored secrets. Without it, webhooks cannot hold secrets.
	Key string
	// AllowPrivate lets outbound webhooks reach private and loopback addresses.
	AllowPrivate bool
	// PublicURL is the externally visible base URL of PublicListen, used to show full inbound URLs.
	PublicURL string
	// Logf receives log lines. Default: discard.
	Logf func(format string, args ...any)
}

type workerState struct {
	cancel context.CancelFunc
	done   chan struct{}
	sig    string
	errMsg atomic.Value // string
}

// Manager runs the webhooks.
type Manager struct {
	opts   Options
	kvAddr string
	kv     *kvClient
	store  *store
	sealer *sealer
	http   *http.Client

	mu       sync.Mutex
	workers  map[string]*workerState
	inbounds map[string]*inboundState
	primary  atomic.Bool
	retiring map[string]chan struct{} // workers told to stop but possibly still finishing

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	srvs   []*http.Server
	addrs  []string
	kick   chan struct{}
}

func (m *Manager) logf(format string, args ...any) {
	if m.opts.Logf != nil {
		m.opts.Logf(format, args...)
	}
}

// Start launches the module. Close stops it.
func Start(opts Options) (*Manager, error) {
	if opts.PublicListen == "" && opts.APIListen == "" {
		return nil, errors.New("webhooks: nothing to start")
	}
	if opts.APIListen != "" && opts.APIToken == "" && !isLoopback(opts.APIListen) {
		return nil, errors.New("webhooks: a management API on a non-loopback address needs -webhooks-api-token")
	}
	sl, err := newSealer(opts.Key)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		opts:     opts,
		kvAddr:   opts.KVAddr,
		kv:       newKVClient(opts.KVAddr, opts.KVUser, opts.KVPass, 16, 5*time.Second),
		sealer:   sl,
		http:     newHTTPClient(opts.AllowPrivate, 60*time.Second),
		workers:  map[string]*workerState{},
		inbounds: map[string]*inboundState{},
		kick:     make(chan struct{}, 1),
		retiring: map[string]chan struct{}{},
	}
	m.store = &store{kv: m.kv}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.primary.Store(true)

	if opts.PublicListen != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/hook/", m.handleHook)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
		if err := m.serve(opts.PublicListen, mux); err != nil {
			m.cancel()
			return nil, err
		}
	}
	if opts.APIListen != "" {
		if err := m.serve(opts.APIListen, m.apiHandler()); err != nil {
			m.Close()
			return nil, err
		}
	}
	m.wg.Add(1)
	go m.loop()
	return m, nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (m *Manager) serve(addr string, h http.Handler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	m.srvs = append(m.srvs, srv)
	m.addrs = append(m.addrs, ln.Addr().String())
	go srv.Serve(ln)
	return nil
}

// Addrs returns the bound address of each listener in start order: the public one (when enabled), then the API one.
func (m *Manager) Addrs() []string { return append([]string(nil), m.addrs...) }

// Close stops listeners and workers.
func (m *Manager) Close() {
	m.cancel()
	for _, s := range m.srvs {
		ctx, c := context.WithTimeout(context.Background(), 2*time.Second)
		s.Shutdown(ctx)
		c()
	}
	m.wg.Wait()
	m.mu.Lock()
	ws := m.workers
	m.workers = map[string]*workerState{}
	m.mu.Unlock()
	for _, w := range ws {
		w.cancel()
	}
	for _, w := range ws {
		<-w.done
	}
	m.kv.close()
}

// refresh asks the background loop to reconcile now.
func (m *Manager) refresh() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) loop() {
	defer m.wg.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	flush := time.NewTicker(2 * time.Second)
	defer flush.Stop()
	m.reconcile()
	for {
		select {
		case <-m.ctx.Done():
			m.flushCounters()
			return
		case <-t.C:
			m.reconcile()
		case <-m.kick:
			m.reconcile()
		case <-flush.C:
			m.flushCounters()
		}
	}
}

func (m *Manager) isPrimary() bool { return m.primary.Load() }

func (m *Manager) checkRole() {
	v, err := m.kv.do("INFO", "replication")
	if err != nil {
		return // keep the last known role
	}
	role := "master"
	for _, line := range strings.Split(v.str, "\n") {
		if strings.HasPrefix(line, "role:") {
			role = strings.TrimSpace(strings.TrimPrefix(line, "role:"))
		}
	}
	m.primary.Store(role == "master")
}

func (m *Manager) inbound(id string) *inboundState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inbounds[id]
}

func (m *Manager) flushCounters() {
	m.mu.Lock()
	var list []*inboundState
	for _, st := range m.inbounds {
		list = append(list, st)
	}
	m.mu.Unlock()
	for _, st := range list {
		if n := st.okPending.Swap(0); n > 0 {
			m.kv.do("HINCRBY", keyStats+st.def.ID, "ok", strconv.FormatInt(n, 10))
			m.kv.do("HSET", keyStats+st.def.ID, "last", strconv.FormatInt(time.Now().UnixMilli(), 10))
		}
	}
}

func sig(d Definition) string {
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// reconcile makes the running set match the stored definitions.
func (m *Manager) reconcile() {
	m.checkRole()
	defs, err := m.store.list()
	if err != nil {
		m.logf("webhooks: cannot read definitions: %v", err)
		return
	}
	want := map[string]Definition{}
	for _, d := range defs {
		want[d.ID] = d
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Inbound endpoints.
	next := map[string]*inboundState{}
	for id, d := range want {
		if d.Kind != KindInbound || !d.Enabled || d.Inbound == nil {
			continue
		}
		if old := m.inbounds[id]; old != nil && sig(old.def) == sig(d) {
			next[id] = old
			continue
		}
		next[id] = newInboundState(d, m.sealer)
	}
	m.inbounds = next
	// Outbound workers.
	for id, w := range m.workers {
		d, ok := want[id]
		if ok && d.Kind == KindOutbound && d.Enabled && m.primary.Load() && w.sig == sig(d) {
			continue
		}
		w.cancel()
		delete(m.workers, id)
		m.retiring[id] = w.done
	}
	if !m.primary.Load() {
		return
	}
	for id, d := range want {
		if d.Kind != KindOutbound || !d.Enabled || m.workers[id] != nil {
			continue
		}
		oc, err := resolveOutbound(d, m.sealer)
		ctx, cancel := context.WithCancel(m.ctx)
		w := &workerState{cancel: cancel, done: make(chan struct{}), sig: sig(d)}
		w.errMsg.Store("")
		m.workers[id] = w
		if err != nil {
			w.errMsg.Store(err.Error())
			close(w.done)
			continue
		}
		prior := m.retiring[id]
		delete(m.retiring, id)
		go func() {
			defer close(w.done)
			if prior != nil { // never run two consumers of one webhook at once
				select {
				case <-prior:
				case <-ctx.Done():
					return
				}
			}
			m.runOutbound(ctx, oc, func(s string) { w.errMsg.Store(s) })
		}()
	}
}

// status reports whether a webhook is live and its last error.
func (m *Manager) status(d Definition) (running bool, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch d.Kind {
	case KindInbound:
		st := m.inbounds[d.ID]
		if st == nil {
			return false, ""
		}
		if st.secErr != nil {
			return false, st.secErr.Error()
		}
		return true, ""
	case KindOutbound:
		w := m.workers[d.ID]
		if w == nil {
			return false, ""
		}
		e, _ := w.errMsg.Load().(string)
		return e == "", e
	}
	return false, ""
}
