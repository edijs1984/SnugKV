// rpccache is a caching JSON-RPC proxy for Solana and EVM nodes. It serves
// repeated reads from a Redis-protocol cache (SnugKV or Redis) and forwards
// everything else to the upstream provider.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"snugkv/internal/rpccache"
)

type headerFlags []string

func (h *headerFlags) String() string     { return strings.Join(*h, ", ") }
func (h *headerFlags) Set(v string) error { *h = append(*h, v); return nil }

func main() {
	d := rpccache.DefaultTTLs()
	listen := flag.String("listen", "127.0.0.1:8899", "address to serve JSON-RPC on")
	upstream := flag.String("upstream", os.Getenv("RPC_UPSTREAM"), "upstream JSON-RPC URL (or RPC_UPSTREAM)")
	chain := flag.String("chain", "solana", "method table: solana or evm")
	cacheAddr := flag.String("cache", "127.0.0.1:6379", "Redis-protocol cache address (SnugKV or Redis)")
	pool := flag.Int("cache-pool", 32, "cache connections")
	cacheTimeout := flag.Duration("cache-timeout", 100*time.Millisecond, "per-command cache timeout; a failing cache is skipped, never fatal")
	maxEntry := flag.Int("max-entry-bytes", 1<<20, "largest result to cache")
	ttlImmutable := flag.Duration("ttl-immutable", d.Immutable, "finalized, permanent results")
	ttlStatic := flag.Duration("ttl-static", d.Static, "rarely changing results")
	ttlRecent := flag.Duration("ttl-recent", d.Recent, "results at a past but reorgable height")
	ttlState := flag.Duration("ttl-state", d.State, "account and state reads")
	ttlTip := flag.Duration("ttl-tip", d.Tip, "chain head reads")
	var headers headerFlags
	flag.Var(&headers, "upstream-header", `extra upstream header "Name: value" (repeatable)`)
	flag.Parse()

	if *upstream == "" {
		log.Fatal("rpccache: -upstream (or RPC_UPSTREAM) is required")
	}
	h := http.Header{}
	for _, kv := range headers {
		name, value, ok := strings.Cut(kv, ":")
		if !ok {
			log.Fatalf("rpccache: bad -upstream-header %q, want \"Name: value\"", kv)
		}
		h.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}

	cache := rpccache.NewRESPCache(*cacheAddr, *pool, *cacheTimeout)
	defer cache.Close()
	proxy, err := rpccache.New(rpccache.Config{
		Chain:         rpccache.Chain(*chain),
		Upstream:      *upstream,
		Headers:       h,
		Cache:         cache,
		MaxEntryBytes: *maxEntry,
		TTLs: rpccache.TTLs{
			Immutable: *ttlImmutable, Static: *ttlStatic, Recent: *ttlRecent,
			State: *ttlState, Tip: *ttlTip,
		},
	})
	if err != nil {
		log.Fatalf("rpccache: %v", err)
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           proxy,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	fmt.Fprintf(os.Stderr, "rpccache: %s on %s -> %s, cache %s\n", *chain, *listen, redact(*upstream), *cacheAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// redact hides a path or query that usually carries an API key.
func redact(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if j := strings.IndexAny(rest, "/?"); j >= 0 {
			return u[:i+3] + rest[:j] + "/…"
		}
	}
	return u
}
