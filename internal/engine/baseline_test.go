package engine

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShardValidation(t *testing.T) {
	for _, n := range []int{-1, 0, 3} {
		if _, err := NewWithShards(n); err == nil {
			t.Fatalf("accepted %d shards", n)
		}
	}
	for _, n := range []int{1, 4, 256} {
		s, err := NewWithShards(n)
		if err != nil || len(s.shards) != n {
			t.Fatalf("shards %d: %v", n, err)
		}
	}
}
func TestOwnershipAndOverwrite(t *testing.T) {
	s := New()
	value := []byte{0, 255, '\r', '\n'}
	if err := s.Set("k", value, 0); err != nil {
		t.Fatal(err)
	}
	value[0] = 1
	got, ok := s.Get("k")
	if !ok || got[0] != 0 {
		t.Fatal("input aliases stored bytes")
	}
	got[0] = 2
	got, _ = s.Get("k")
	if got[0] != 0 {
		t.Fatal("output aliases stored bytes")
	}
	s.Set("k", nil, 0)
	got, ok = s.Get("k")
	if !ok || len(got) != 0 {
		t.Fatal("empty value lost")
	}
}
func TestExpirationBoundariesAndAccounting(t *testing.T) {
	s := New()
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	s.Set("a", []byte("123"), 1500)
	if s.TTL("a", false) != 2 || s.TTL("a", true) != 1500 {
		t.Fatal("initial TTL")
	}
	if got := s.Stats(); got != (DatasetStats{1, 1, 3}) {
		t.Fatalf("stats: %+v", got)
	}
	now = now.Add(1500 * time.Millisecond)
	if _, ok := s.Get("a"); ok || s.TTL("a", true) != -2 {
		t.Fatal("expiration boundary")
	}
	if s.Stats().Keys != 0 || s.CleanupExpired() != 1 || s.CleanupExpired() != 0 {
		t.Fatal("cleanup/accounting")
	}
	s.Set("a", []byte("7"), 1)
	now = now.Add(time.Millisecond)
	if s.Delete("a") {
		t.Fatal("expired DEL counted")
	}
	s.Set("a", []byte("7"), 1)
	now = now.Add(time.Millisecond)
	if n, err := s.Incr("a"); n != 1 || err != nil || s.TTL("a", true) != -1 {
		t.Fatalf("expired INCR: %d %v", n, err)
	}
	s.Set("a", []byte("7"), 100)
	s.Incr("a")
	if s.TTL("a", true) != 100 {
		t.Fatal("counter lost TTL")
	}
	s.Set("a", []byte("new"), 0)
	if s.TTL("a", true) != -1 || s.CleanupExpired() != 0 {
		t.Fatal("overwrite lost")
	}
	if err := s.Set("a", nil, math.MaxInt64); err == nil {
		t.Fatal("TTL overflow accepted")
	}
	if v, _ := s.Get("a"); string(v) != "new" {
		t.Fatal("failed write mutated value")
	}
}
func TestCounterValidation(t *testing.T) {
	s := New()
	for _, v := range []string{"", "00", "+1", "-0", " 1", "1 ", "1.0", "9223372036854775808"} {
		s.Set("n", []byte(v), 0)
		if _, err := s.Incr("n"); err == nil {
			t.Errorf("accepted %q", v)
		}
		if got, _ := s.Get("n"); string(got) != v {
			t.Errorf("changed rejected %q", v)
		}
	}
	for _, tc := range []struct {
		value string
		delta int64
	}{{"9223372036854775807", 1}, {"-9223372036854775808", -1}, {"-1", math.MinInt64}, {"1", math.MaxInt64}} {
		s.Set("n", []byte(tc.value), 0)
		if _, err := s.Add("n", tc.delta); err == nil {
			t.Fatal("overflow accepted")
		}
		if got, _ := s.Get("n"); string(got) != tc.value {
			t.Fatal("overflow mutated value")
		}
	}
	s.Delete("n")
	if n, err := s.Decr("n"); n != -1 || err != nil {
		t.Fatalf("DECR: %d %v", n, err)
	}
}
func TestConcurrentCountersAndValues(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprint(id)
			for j := 0; j < 500; j++ {
				if _, err := s.Incr("counter"); err != nil {
					t.Error(err)
				}
				s.Set(key, []byte("value"), 0)
				s.Get(key)
				s.Get("shared")
				s.Set("shared", []byte("complete"), 0)
				s.Delete(key)
				if j%100 == 0 {
					s.CleanupExpired()
					s.Stats()
				}
			}
		}(i)
	}
	wg.Wait()
	if got, _ := s.Get("counter"); string(got) != "8000" {
		t.Fatalf("counter: %s", got)
	}
}
func BenchmarkRawStore(b *testing.B) {
	s := New()
	value := make([]byte, 256)
	s.Set("key", value, 0)
	b.ReportAllocs()
	b.SetBytes(int64(len(value)))
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Set("key", value, 0)
			s.Get("key")
		}
	})
}

func TestConditionalAndMultiKeyAtomicity(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	var wins int64
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.SetConditional("only", []byte("v"), SetOptions{NX: true})
			if err != nil {
				t.Error(err)
			}
			if ok {
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("NX winners %d", wins)
	}
	s.MSet([]string{"a", "b"}, [][]byte{[]byte("0"), []byte("0")})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				v := []byte(fmt.Sprint(i))
				s.MSet([]string{"a", "b"}, [][]byte{v, v})
				values, found := s.MGet([]string{"a", "b"})
				if !found[0] || !found[1] || string(values[0]) != string(values[1]) {
					t.Error("partial MSET")
				}
			}
		}(i)
	}
	wg.Wait()
}


func TestSetReplicaFreshPlainInsertsAndFallsBackOnOverwrite(t *testing.T) {
	s := New()

	handled, err := s.SetReplicaFreshPlain("replica:fresh", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("fresh replicated SET was not handled")
	}
	got, ok := s.Get("replica:fresh")
	if !ok || string(got) != "first" {
		t.Fatalf("fresh value=%q ok=%v", got, ok)
	}

	handled, err = s.SetReplicaFreshPlain("replica:fresh", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("existing key must fall back to ordinary SET")
	}
	if err := s.SetPlain("replica:fresh", []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, ok = s.Get("replica:fresh")
	if !ok || string(got) != "second" {
		t.Fatalf("fallback value=%q ok=%v", got, ok)
	}
}

func TestSetReplicaFreshPlainPreservesMemoryAccounting(t *testing.T) {
	s := New()
	before := s.Memory()

	value := make([]byte, 256)
	handled, err := s.SetReplicaFreshPlain("replica:memory", value)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("fresh replicated SET was not handled")
	}

	after := s.Memory()
	if after.AccountedBytes <= before.AccountedBytes {
		t.Fatalf("accounted bytes did not grow: before=%d after=%d", before.AccountedBytes, after.AccountedBytes)
	}
	if after.ArenaPayloadBytes-before.ArenaPayloadBytes != uint64(len(value)) {
		t.Fatalf("arena payload delta=%d want=%d", after.ArenaPayloadBytes-before.ArenaPayloadBytes, len(value))
	}
}
