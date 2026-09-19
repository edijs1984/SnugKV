package server

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Metrics uses a separate loopback-only HTTP listener and exports no keys/values.
func (s *TCPServer) Metrics(addr string) (*http.Server, error) {
	atomic.StoreUint32(&s.server.metricsEnabled, 1)
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("metrics address must be a loopback IP")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		s.server.metrics.WritePrometheus(w)
		m := s.server.store.Memory()
		st := s.server.store.Stats()
		s.mu.Lock()
		connections := len(s.connections)
		s.mu.Unlock()
		fmt.Fprintf(w, "snugkv_active_connections %d\nsnugkv_keys %d\nsnugkv_memory_accounted_bytes %d\nsnugkv_memory_index_reserved_bytes %d\nsnugkv_logical_value_bytes %d\nsnugkv_input_bytes_total %d\nsnugkv_output_bytes_total %d\n", connections, st.Keys, m.AccountedBytes, m.IndexReservedBytes, st.ValueBytes, atomic.LoadUint64(&s.inputBytes), atomic.LoadUint64(&s.outputBytes))
		details := s.server.store.Inspect()
		fmt.Fprintf(w, "snugkv_encoded_value_bytes %d\nsnugkv_expired_keys_total %d\nsnugkv_evicted_keys_total %d\nsnugkv_schema_bytes %d\nsnugkv_dictionary_bytes %d\nsnugkv_schema_reserved_bytes %d\nsnugkv_arena_capacity_bytes %d\n", details.EncodedBytes, details.Expired, details.Evicted, details.SchemaBytes, details.DictionaryBytes, m.SchemaBytes, m.ArenaBytes)
		for name, count := range details.Codecs {
			fmt.Fprintf(w, "snugkv_codec_keys{codec=%q} %d\n", name, count)
		}
		if s.server.optimizer != nil {
			o := s.server.optimizer.Stats()
			fmt.Fprintf(w, "snugkv_optimizer_queue_depth %d\nsnugkv_optimizer_rewrites_total %d\nsnugkv_optimizer_stale_total %d\nsnugkv_optimizer_skipped_total %d\n", o.QueueDepth, o.Rewritten, o.Stale, o.Skipped)
		}
	})
	h := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go h.Serve(ln)
	return h, nil
}

type countedConn struct {
	net.Conn
	input, output *uint64
}

func (c countedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	atomic.AddUint64(c.input, uint64(n))
	return n, err
}
func (c countedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	atomic.AddUint64(c.output, uint64(n))
	return n, err
}
func isAdminCommand(args [][]byte) bool {
	return len(args) > 0 && strings.HasPrefix(strings.ToUpper(string(args[0])), "SNUG.")
}
