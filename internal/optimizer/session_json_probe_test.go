package optimizer

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func probeSessionJSON(i, size int) []byte {
	p := fmt.Sprintf("{\"user_id\":%d,\"role\":\"user\",\"authenticated\":true,\"expires_in\":3600,\"csrf\":\"%08x\",\"state\":\"", i, uint32(uint64(1)^uint64(i)*2654435761))
	s := "\"}"
	salt := i + 17
	v := make([]byte, 0, size)
	v = append(v, p...)
	for len(v)+len(s) < size {
		v = append(v, byte('a'+(salt+len(v))%23))
	}
	return append(v, s...)
}

// TestSessionJSONMemoryProbe reports per-key memory components for the
// session-json benchmark shape. Run with SNUG_PROBE=1.
func TestSessionJSONMemoryProbe(t *testing.T) {
	if os.Getenv("SNUG_PROBE") == "" {
		t.Skip("set SNUG_PROBE=1")
	}
	n := 100000
	if v, err := strconv.Atoi(os.Getenv("SNUG_PROBE_N")); err == nil {
		n = v
	}
	store, err := engine.NewWithOptions(engine.Options{Shards: 256, Encoding: true, ShapeEncoding: true, Compression: true})
	if err != nil {
		t.Fatal(err)
	}
	o, err := New(store, Default())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	report := func(label string) {
		m := store.Memory()
		l := store.Layout()
		per := func(x uint64) float64 { return float64(x) / float64(n) }
		fmt.Printf("%-10s used=%.1f idx=%.1f ent=%.1f meta=%.1f arena=%.1f live=%.1f payload=%.1f | entcap=%d entcnt=%d\n",
			label, per(m.AccountedBytes), per(m.IndexReservedBytes), per(m.EntryBytes), per(m.MetaBytes), per(m.ArenaBytes), per(m.ArenaLiveBlockBytes), per(m.ArenaPayloadBytes), l.EntryCapacity, l.EntryCount)
	}
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("bench:%09d", i)
		if err := store.Set(k, probeSessionJSON(i, 384), 0); err != nil {
			t.Fatal(err)
		}
		o.Queue(k)
	}
	report("loaded")
	codecs := map[string]int{}
	converge := func() {
		deadline := time.Now().Add(200 * time.Second)
		var last uint64
		stable := 0
		for time.Now().Before(deadline) {
			time.Sleep(time.Second)
			if len(o.queue) <= cap(o.queue)/4 {
				o.Sample(20000)
			}
			cur := o.Stats().Rewritten
			if cur == last && len(o.queue) == 0 {
				stable++
			} else {
				stable = 0
			}
			last = cur
			if stable >= 6 {
				break
			}
		}
	}
	converge()
	report("rewritten")
	fmt.Println("compact1 freed:", store.Compact(256<<20))
	report("compacted")
	fmt.Println("compact2 freed:", store.Compact(256<<20))
	report("compacted2")
	for i := 0; i < n; i += 97 {
		name, _, _, _ := store.Encoding(fmt.Sprintf("bench:%09d", i))
		codecs[name]++
	}
	fmt.Println("codecs:", codecs)
	if rep, ok := store.CandidateDiagnostics("bench:000000123"); ok {
		fmt.Printf("diag: %+v\n", rep)
	}
	if os.Getenv("SNUG_PROBE_SECOND") != "" {
		for i := 0; i < n; i++ {
			k := fmt.Sprintf("bench:%09d", i)
			_ = store.Set(k, probeSessionJSON(i, 384), 0)
			o.Queue(k)
		}
		converge()
		store.Compact(256 << 20)
		report("second")
	}
}
