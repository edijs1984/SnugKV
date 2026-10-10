package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"snugkv/internal/rpccache"
)

// methodTally counts single calls of one method by the proxy's X-Cache label.
type methodTally struct{ calls, hit, miss, coalesced, bypass atomic.Uint64 }

type keyTally struct {
	secret         string
	sent, denied   atomic.Uint64 // calls the proxy admitted / refused, as seen by the client
	requests, r429 atomic.Uint64
}

// runWallets simulates users of a wallet or dashboard. Each user watches a set
// of accounts and refreshes them every few seconds: the chain head, one
// getMultipleAccounts for the whole set, the owner's balance, and a few reads
// of popular accounts that many users share. It reports what the cache saved,
// how stale cached answers were against the node, and whether the proxy's
// per-key counts match what was sent.
func runWallets(args []string) {
	fs := flag.NewFlagSet("wallets", flag.ExitOnError)
	chain := fs.String("chain", "solana", "workload: solana or evm (must match the proxy's -chain and the node's -chain)")
	cacheAddr := fs.String("cache", "", "cache server address, to report its memory")
	wait := fs.Duration("wait", 0, "wait this long after the run before reading cache memory (lets SnugKV's optimizer finish)")
	url := fs.String("url", "http://127.0.0.1:8899", "proxy URL")
	direct := fs.String("direct", "", "the node itself, to measure staleness (a sample of reads is repeated there)")
	checkRate := fs.Float64("check-rate", 0.2, "share of popular-account reads repeated against -direct")
	upstreamStats := fs.String("upstream-stats", "", "fake node base URL, to count the calls that reached it")
	users := fs.Int("users", 200, "simulated users")
	duration := fs.Duration("duration", 30*time.Second, "how long to run")
	refresh := fs.Duration("refresh", 3*time.Second, "time between a user's refreshes")
	watched := fs.Int("watched", 15, "accounts each user watches")
	popular := fs.Int("popular", 200, "size of the pool of popular accounts shared by all users")
	popularReads := fs.Int("popular-reads", 5, "popular-account reads per refresh")
	overlap := fs.Float64("overlap", 0.5, "share of a user's watched accounts taken from the popular pool")
	skew := fs.Float64("skew", 1.2, "Zipf exponent for popular accounts (>1)")
	keys := fs.String("keys", "", "comma-separated API keys; users take them in turn")
	store := fs.String("store", "", "SnugKV address holding key usage, to compare its counts with what was sent")
	seed := fs.Int64("seed", 1, "random seed")
	fs.Parse(args)
	evm := *chain == "evm"
	if !evm && *chain != "solana" {
		fmt.Fprintln(os.Stderr, "rpcbench: -chain must be solana or evm")
		os.Exit(2)
	}
	memBefore := usedMemory(*cacheAddr)

	var tallies []*keyTally
	for _, k := range strings.Split(*keys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			tallies = append(tallies, &keyTally{secret: k})
		}
	}

	// Usage is a per-day total, so note what each key already holds and compare
	// only the change made by this run.
	var admin *rpccache.Admin
	before := map[string]map[string]int64{}
	if *store != "" && len(tallies) > 0 {
		c := rpccache.NewClient(*store, 2, 2*time.Second)
		defer c.Close()
		admin = rpccache.NewAdmin(c)
		for _, kt := range tallies {
			if u, err := admin.Usage(rpccache.KeyID(kt.secret), 1); err == nil {
				before[kt.secret] = u[0].Counters
			}
		}
	}

	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *users + 8}}
	methods := map[string]*methodTally{}
	var mmu sync.Mutex
	tally := func(m string) *methodTally {
		mmu.Lock()
		defer mmu.Unlock()
		t := methods[m]
		if t == nil {
			t = &methodTally{}
			methods[m] = t
		}
		return t
	}

	var httpReqs, calls, failures atomic.Uint64
	var checked, stale, directCalls atomic.Uint64
	var failStatus atomic.Int64 // first unexpected HTTP status
	var latMu sync.Mutex
	var lats []time.Duration
	var lagMu sync.Mutex
	var lags []int64 // slots the cached answer was behind, for stale answers

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	// post sends one JSON-RPC call (n counts calls in a batch) and returns the
	// reply body and X-Cache label. ok is false when the proxy did not admit it.
	post := func(kt *keyTally, body string, n int) (reply []byte, label string, ok bool) {
		// Requests are not cancelled when the run ends, so every call the proxy
		// admitted is also counted here and the totals can be compared exactly.
		req, _ := http.NewRequest(http.MethodPost, *url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if kt != nil {
			req.Header.Set("Authorization", "Bearer "+kt.secret)
		}
		httpReqs.Add(1)
		t0 := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			failures.Add(1)
			return nil, "", false
		}
		defer resp.Body.Close()
		reply, _ = io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK {
			d := time.Since(t0)
			latMu.Lock()
			lats = append(lats, d)
			latMu.Unlock()
		}
		if kt != nil {
			kt.requests.Add(1)
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			if kt != nil {
				kt.r429.Add(1)
				kt.denied.Add(uint64(n))
			}
			return reply, "", false
		case resp.StatusCode != http.StatusOK:
			failures.Add(1)
			failStatus.CompareAndSwap(0, int64(resp.StatusCode))
			return reply, "", false
		}
		calls.Add(uint64(n))
		if kt != nil {
			kt.sent.Add(uint64(n))
		}
		return reply, resp.Header.Get("X-Cache"), true
	}
	one := func(kt *keyTally, method, params string) ([]byte, bool) {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
		reply, label, ok := post(kt, body, 1)
		if ok {
			t := tally(method)
			t.calls.Add(1)
			switch label {
			case "HIT":
				t.hit.Add(1)
			case "MISS":
				t.miss.Add(1)
			case "COALESCED":
				t.coalesced.Add(1)
			default:
				t.bypass.Add(1)
			}
		}
		return reply, ok
	}

	// batch sends several calls of one method as a JSON-RPC batch.
	batch := func(kt *keyTally, method string, paramSets []string) {
		var b strings.Builder
		b.WriteByte('[')
		for i, p := range paramSets {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"jsonrpc":"2.0","id":%d,"method":"%s","params":%s}`, i+1, method, p)
		}
		b.WriteByte(']')
		_, label, ok := post(kt, b.String(), len(paramSets))
		if !ok {
			return
		}
		t := tally(method + " (batch)")
		t.calls.Add(uint64(len(paramSets)))
		var h, m, by uint64
		fmt.Sscanf(label, "BATCH hit=%d miss=%d bypass=%d", &h, &m, &by)
		t.hit.Add(h)
		t.miss.Add(m)
		t.bypass.Add(by)
	}

	popAddr := func(i int) string { return accountAddress(i) }
	if evm {
		popAddr = func(i int) string { return evmAddress(i) }
	}
	// Cached entries expire in seconds, so the end of the run says little about
	// memory. Sample while it runs and keep the point with the most entries.
	var peakEntries, peakBytes int64
	sampleDone := make(chan struct{})
	var sampleWG sync.WaitGroup
	if *cacheAddr != "" {
		sampleWG.Add(1)
		go func() {
			defer sampleWG.Done()
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					if n, ok := dbCount(*cacheAddr); ok && n > peakEntries {
						peakEntries, peakBytes = n, usedMemory(*cacheAddr)-memBefore
					}
				case <-sampleDone:
					return
				}
			}
		}()
	}

	var wg sync.WaitGroup
	start := time.Now()
	for u := 0; u < *users; u++ {
		wg.Add(1)
		go func(u int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(*seed*1_000_003 + int64(u)))
			zipf := rand.NewZipf(rng, *skew, 1, uint64(*popular-1))
			var kt *keyTally
			if len(tallies) > 0 {
				kt = tallies[u%len(tallies)]
			}
			// The user's own accounts: some from the popular pool, the rest private.
			set := make([]string, *watched)
			for i := range set {
				if rng.Float64() < *overlap {
					set[i] = popAddr(int(zipf.Uint64()))
				} else {
					set[i] = accountAddress(1_000_000 + u*1000 + i)
					if evm {
						set[i] = evmAddress(1_000_000 + u*1000 + i)
					}
				}
			}
			multi, _ := json.Marshal([]any{set, map[string]string{"encoding": "base64"}})
			owner := accountAddress(2_000_000 + u)
			if evm {
				owner = evmAddress(2_000_000 + u)
			}
			var tokenCalls []string // eth_call balanceOf(owner) for each watched token
			if evm {
				for _, token := range set {
					tokenCalls = append(tokenCalls, `[{"to":"`+token+`","data":"0x70a08231`+strings.Repeat("0", 24)+owner[2:]+`"},"latest"]`)
				}
			}

			// Spread the first refreshes so users do not move in lockstep.
			select {
			case <-time.After(time.Duration(rng.Int63n(int64(*refresh)))):
			case <-ctx.Done():
				return
			}
			for ctx.Err() == nil {
				if evm {
					one(kt, "eth_blockNumber", `[]`)
					one(kt, "eth_gasPrice", `[]`)
					one(kt, "eth_getBalance", `["`+owner+`","latest"]`)
					batch(kt, "eth_call", tokenCalls)
				} else {
					one(kt, "getSlot", `[]`)
					one(kt, "getMultipleAccounts", string(multi))
					one(kt, "getBalance", `["`+owner+`"]`)
				}
				for i := 0; i < *popularReads; i++ {
					addr := popAddr(int(zipf.Uint64()))
					method, params := "getAccountInfo", `["`+addr+`",{"encoding":"base64"}]`
					if evm {
						// A popular contract's view function: the same call for everyone.
						method, params = "eth_call", `[{"to":"`+addr+`","data":"0xfeaf968c"},"latest"]`
					}
					reply, ok := one(kt, method, params)
					if !ok || *direct == "" || rng.Float64() >= *checkRate {
						continue
					}
					directCalls.Add(1)
					fresh, err := directCall(client, *direct, method, params)
					if err != nil {
						continue
					}
					got, gotSlot := splitReply(reply, evm)
					want, wantSlot := splitReply(fresh, evm)
					if got == nil || want == nil {
						continue
					}
					checked.Add(1)
					if !bytes.Equal(got, want) {
						stale.Add(1)
						if !evm {
							lagMu.Lock()
							lags = append(lags, int64(wantSlot)-int64(gotSlot))
							lagMu.Unlock()
						}
					}
				}
				select {
				case <-time.After(*refresh + time.Duration(rng.Int63n(int64(*refresh)/5+1))):
				case <-ctx.Done():
					return
				}
			}
		}(u)
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(sampleDone)
	sampleWG.Wait()

	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	pct := func(p float64) float64 {
		if len(lats) == 0 {
			return 0
		}
		return float64(lats[int(float64(len(lats)-1)*p)]) / 1e6
	}
	out := map[string]any{
		"chain": *chain, "p50_ms": pct(0.50), "p99_ms": pct(0.99),
		"users": *users, "seconds": elapsed.Seconds(),
		"http_requests": httpReqs.Load(), "rpc_calls": calls.Load(), "failures": failures.Load(),
		"calls_per_sec": float64(calls.Load()) / elapsed.Seconds(),
	}
	if st := failStatus.Load(); st != 0 {
		out["first_failure_status"] = st
	}
	perMethod := map[string]any{}
	var singles, hits uint64
	names := make([]string, 0, len(methods))
	for m := range methods {
		names = append(names, m)
	}
	sort.Strings(names)
	for _, m := range names {
		t := methods[m]
		c := t.calls.Load()
		singles += c
		hits += t.hit.Load() + t.coalesced.Load()
		perMethod[m] = map[string]any{
			"calls": c, "hit": t.hit.Load(), "miss": t.miss.Load(), "coalesced": t.coalesced.Load(), "bypass": t.bypass.Load(),
			"answered_without_node": float64(t.hit.Load()+t.coalesced.Load()) / float64(max(c, 1)),
		}
	}
	out["methods"] = perMethod
	out["answered_without_node"] = float64(hits) / float64(max(singles, 1))
	if *upstreamStats != "" {
		if resp, err := http.Get(*upstreamStats + "/calls"); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var n uint64
			fmt.Sscan(strings.TrimSpace(string(b)), &n)
			// The staleness checks also reach the node; they are not the proxy's.
			n -= min(n, directCalls.Load())
			out["node_calls_from_proxy"] = n
			if c := calls.Load(); c > 0 {
				out["node_calls_saved"] = 1 - float64(n)/float64(c)
			}
		}
	}
	if *cacheAddr != "" {
		out["cache_peak_entries"], out["cache_peak_bytes"] = peakEntries, peakBytes
		if peakEntries > 0 {
			out["cache_bytes_per_entry_at_peak"] = float64(peakBytes) / float64(peakEntries)
		}
		time.Sleep(*wait)
		out["cache_bytes_after_wait"] = usedMemory(*cacheAddr) - memBefore
		if n, ok := dbCount(*cacheAddr); ok {
			out["cache_entries_after_wait"] = n
		}
	}
	if *direct != "" {
		s := map[string]any{"checked": checked.Load(), "stale": stale.Load()}
		if c := checked.Load(); c > 0 {
			s["stale_share"] = float64(stale.Load()) / float64(c)
		}
		if len(lags) > 0 {
			sort.Slice(lags, func(i, j int) bool { return lags[i] < lags[j] })
			s["lag_slots_p50"] = lags[len(lags)/2]
			s["lag_slots_p99"] = lags[int(float64(len(lags)-1)*0.99)]
			s["lag_slots_max"] = lags[len(lags)-1]
		}
		out["staleness"] = s
	}
	if len(tallies) > 0 {
		time.Sleep(2500 * time.Millisecond) // the proxy writes usage once a second
		var rows []map[string]any
		for i, kt := range tallies {
			row := map[string]any{
				"key": i + 1, "http_requests": kt.requests.Load(), "http_429": kt.r429.Load(),
				"calls_admitted": kt.sent.Load(), "calls_refused": kt.denied.Load(),
			}
			if admin != nil {
				if u, err := admin.Usage(rpccache.KeyID(kt.secret), 1); err == nil {
					c, b := u[0].Counters, before[kt.secret]
					calls, denied := c["calls"]-b["calls"], c["denied"]-b["denied"]
					row["counted_calls"], row["counted_denied"] = calls, denied
					row["counts_match"] = uint64(calls) == kt.sent.Load() && uint64(denied) == kt.denied.Load()
				} else {
					row["usage_error"] = err.Error()
				}
			}
			rows = append(rows, row)
		}
		out["keys"] = rows
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

// directCall sends one call straight to the node.
func directCall(c *http.Client, url, method, params string) ([]byte, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// splitReply returns the part of a reply to compare and, for Solana, its slot.
// For EVM it is the whole result.
func splitReply(b []byte, evm bool) (value []byte, slot uint64) {
	if evm {
		var r struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(b, &r) != nil || len(r.Result) == 0 {
			return nil, 0
		}
		return r.Result, 0
	}
	var r struct {
		Result struct {
			Context struct {
				Slot uint64 `json:"slot"`
			} `json:"context"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &r) != nil || len(r.Result.Value) == 0 {
		return nil, 0
	}
	return r.Result.Value, r.Result.Context.Slot
}

// evmAddress is a made-up 20-byte address.
func evmAddress(i int) string {
	h := sha256.Sum256([]byte("evm-account-" + strconv.Itoa(i)))
	return fmt.Sprintf("0x%x", h[:20])
}

// dbCount is the number of keys in the cache server.
func dbCount(addr string) (int64, bool) {
	s, err := resp(addr, "DBSIZE")
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}
