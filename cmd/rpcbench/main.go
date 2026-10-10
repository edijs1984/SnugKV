// rpcbench measures rpccache. It has two modes:
//
//	rpcbench upstream -listen 127.0.0.1:9100 -delay 20ms
//	    a fake Solana node that answers getAccountInfo for any address with a
//	    token-account response shaped like the real one
//	rpcbench load -url http://127.0.0.1:8899 -accounts 100000 -requests 1000000
//	    a Zipf-distributed read workload against the proxy; with -cache it
//	    also reports the memory the cache server used
//	rpcbench wallets -url http://127.0.0.1:8899 -users 200 -duration 30s
//	    simulated wallet users refreshing their balances; reports savings,
//	    how stale cached answers are, and (with keys) whether limits and
//	    metering match what was sent
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: rpcbench upstream|load|wallets [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "upstream":
		runUpstream(os.Args[2:])
	case "load":
		runLoad(os.Args[2:])
	case "wallets":
		runWallets(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: rpcbench upstream|load|wallets [flags]")
		os.Exit(2)
	}
}

// ---- fake node ----

func runUpstream(args []string) {
	fs := flag.NewFlagSet("upstream", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:9100", "listen address")
	delay := fs.Duration("delay", 20*time.Millisecond, "simulated node latency")
	slot := fs.Uint64("slot", 300_000_000, "slot reported in every response")
	advance := fs.Bool("advance", false, "let the slot advance with the clock (400 ms per slot) and let some accounts change as it does")
	fs.Parse(args)
	started := time.Now()
	curSlot := func() uint64 {
		if !*advance {
			return *slot
		}
		return *slot + uint64(time.Since(started)/(400*time.Millisecond))
	}

	var calls atomic.Uint64
	http.HandleFunc("/calls", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, calls.Load()) })
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(*delay)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     json.RawMessage   `json:"id"`
		}
		json.Unmarshal(body, &req)
		sl := curSlot()
		first := func() string {
			var addr string
			if len(req.Params) > 0 {
				json.Unmarshal(req.Params[0], &addr)
			}
			return addr
		}
		var result string
		switch req.Method {
		case "getSlot", "getBlockHeight":
			result = strconv.FormatUint(sl, 10)
		case "getLatestBlockhash":
			h := sha256.Sum256([]byte("blockhash-" + strconv.FormatUint(sl/150, 10)))
			result = fmt.Sprintf(`{"context":{"slot":%d},"value":{"blockhash":"%s","lastValidBlockHeight":%d}}`, sl, base58(h[:]), sl+150)
		case "getBalance":
			h := sha256.Sum256([]byte(first()))
			result = fmt.Sprintf(`{"context":{"slot":%d},"value":%d}`, sl, 1_000_000+binary.LittleEndian.Uint64(h[:])%5_000_000_000+accountVersion(first(), sl, *advance))
		case "getMultipleAccounts":
			var addrs []string
			if len(req.Params) > 0 {
				json.Unmarshal(req.Params[0], &addrs)
			}
			var b strings.Builder
			fmt.Fprintf(&b, `{"context":{"slot":%d},"value":[`, sl)
			for i, a := range addrs {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(accountValue(a, sl, *advance))
			}
			b.WriteString(`]}`)
			result = b.String()
		default:
			result = fmt.Sprintf(`{"context":{"apiVersion":"2.1.0","slot":%d},"value":%s}`, sl, accountValue(first(), sl, *advance))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","result":%s,"id":%s}`, result, req.ID)
	})
	fmt.Fprintln(os.Stderr, "rpcbench: fake node on", *listen)
	if err := http.ListenAndServe(*listen, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// accountVersion says how many times an account has changed by a slot. About
// a tenth of accounts change every 2 slots, a fifth every 150, the rest never.
// With advance off nothing changes, so earlier runs reproduce exactly.
func accountVersion(addr string, slot uint64, advance bool) uint64 {
	if !advance {
		return 0
	}
	h := sha256.Sum256([]byte(addr))
	switch {
	case h[1]%10 == 0:
		return slot / 2
	case h[1]%10 < 3:
		return slot / 150
	}
	return 0
}

// accountValue is the "value" object of a getAccountInfo reply for a token
// account whose contents are derived from the address: one of 64 mints, an
// address-specific owner and amount.
func accountValue(addr string, slot uint64, advance bool) string {
	h := sha256.Sum256([]byte(addr))
	acct := make([]byte, 165)
	mint := sha256.Sum256([]byte{h[0] % 64})
	copy(acct[0:32], mint[:])
	copy(acct[32:64], h[:])
	binary.LittleEndian.PutUint64(acct[64:], (binary.LittleEndian.Uint64(h[8:])>>(h[16]%50))+accountVersion(addr, slot, advance))
	acct[108] = 1
	data := base64.StdEncoding.EncodeToString(acct)
	return fmt.Sprintf(`{"data":["%s","base64"],"executable":false,"lamports":2039280,"owner":"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA","rentEpoch":18446744073709551615,"space":165}`, data)
}

// accountInfo builds a getAccountInfo result.
func accountInfo(addr string, slot uint64) string {
	return fmt.Sprintf(`{"context":{"apiVersion":"2.1.0","slot":%d},"value":%s}`, slot, accountValue(addr, slot, false))
}

// ---- load ----

func runLoad(args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:8899", "proxy URL")
	accounts := fs.Int("accounts", 100_000, "distinct account addresses")
	requests := fs.Int("requests", 500_000, "total requests")
	workers := fs.Int("workers", 64, "concurrent clients")
	skew := fs.Float64("skew", 1.2, "Zipf exponent (>1); higher means more repeats of popular accounts")
	cacheAddr := fs.String("cache", "", "cache server address, to report its memory")
	wait := fs.Duration("wait", 0, "wait this long after the load before reading cache memory (lets SnugKV's optimizer finish)")
	upstreamURL := fs.String("upstream-stats", "", "fake node base URL, to report how many requests reached it")
	fs.Parse(args)

	before := usedMemory(*cacheAddr)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: *workers * 2}}

	var next atomic.Int64
	var hits, misses, coalesced, other, failures atomic.Uint64
	lat := make([][]time.Duration, *workers)
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + 1))
			zipf := rand.NewZipf(rng, *skew, 1, uint64(*accounts-1))
			for next.Add(1) <= int64(*requests) {
				addr := accountAddress(int(zipf.Uint64()))
				body := `{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["` + addr + `",{"encoding":"base64"}]}`
				t0 := time.Now()
				resp, err := client.Post(*url, "application/json", strings.NewReader(body))
				if err != nil {
					failures.Add(1)
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				lat[w] = append(lat[w], time.Since(t0))
				if resp.StatusCode != 200 {
					failures.Add(1)
					continue
				}
				switch resp.Header.Get("X-Cache") {
				case "HIT":
					hits.Add(1)
				case "MISS":
					misses.Add(1)
				case "COALESCED":
					coalesced.Add(1)
				default:
					other.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	var all []time.Duration
	for _, l := range lat {
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	pct := func(p float64) time.Duration {
		if len(all) == 0 {
			return 0
		}
		return all[int(float64(len(all)-1)*p)]
	}
	total := hits.Load() + misses.Load() + coalesced.Load() + other.Load()
	out := map[string]any{
		"requests":         total,
		"failures":         failures.Load(),
		"seconds":          elapsed.Seconds(),
		"requests_per_sec": float64(total) / elapsed.Seconds(),
		"hit_rate":         float64(hits.Load()) / float64(max(total, 1)),
		"hits":             hits.Load(),
		"misses":           misses.Load(),
		"coalesced":        coalesced.Load(),
		"p50_ms":           float64(pct(0.50)) / 1e6,
		"p99_ms":           float64(pct(0.99)) / 1e6,
		"accounts":         *accounts,
		"skew":             *skew,
	}
	if *upstreamURL != "" {
		if resp, err := http.Get(*upstreamURL + "/calls"); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			n, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
			out["upstream_calls"] = n
		}
	}
	if *cacheAddr != "" {
		time.Sleep(*wait)
		after := usedMemory(*cacheAddr)
		out["cache_used_bytes"] = after - before
		if misses.Load() > 0 {
			out["cache_bytes_per_entry"] = float64(after-before) / float64(dbSize(*cacheAddr))
		}
		out["cache_entries"] = dbSize(*cacheAddr)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

func accountAddress(i int) string {
	h := sha256.Sum256([]byte("account-" + strconv.Itoa(i)))
	return base58(h[:])
}

func base58(b []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	n := new(big.Int).SetBytes(b)
	base, mod := big.NewInt(58), new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// ---- cache server probes ----

func resp(addr string, args ...string) (string, error) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	var b bytes.Buffer
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	c.Write(b.Bytes())
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if line[0] == '$' {
		n, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}
	return strings.TrimSpace(line[1:]), nil
}

func usedMemory(addr string) int64 {
	if addr == "" {
		return 0
	}
	info, err := resp(addr, "INFO", "memory")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "used_memory:"); ok {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	return 0
}

func dbSize(addr string) int64 {
	s, err := resp(addr, "DBSIZE")
	if err != nil {
		return 1
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	if n < 1 {
		n = 1
	}
	return n
}
