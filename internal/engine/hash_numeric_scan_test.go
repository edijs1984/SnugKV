package engine

import (
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHashIncrByAtomicAndOverflowSafe(t *testing.T) {
	s := New()

	const workers = 64
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if _, err := s.HashIncrBy("h", []byte("n"), 1); err != nil {
				t.Errorf("HashIncrBy: %v", err)
			}
		}()
	}
	wg.Wait()

	value, found, err := s.HashGet("h", []byte("n"))
	if err != nil || !found || string(value) != "64" {
		t.Fatalf("value=%q found=%t err=%v", value, found, err)
	}

	if _, err := s.HashSet("max", [][]byte{[]byte("n")}, [][]byte{[]byte("9223372036854775807")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HashIncrBy("max", []byte("n"), 1); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflow error=%v", err)
	}
	value, _, _ = s.HashGet("max", []byte("n"))
	if string(value) != "9223372036854775807" {
		t.Fatalf("overflow mutated value: %q", value)
	}
}

func TestHashNumericPreservesTTLAndRejectsInvalidValues(t *testing.T) {
	s := New()
	if _, err := s.HashSet("h", [][]byte{[]byte("i"), []byte("f")}, [][]byte{[]byte("10"), []byte("1.5")}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("h", time.Minute) {
		t.Fatal("Expire failed")
	}

	if got, err := s.HashIncrBy("h", []byte("i"), -3); err != nil || got != 7 {
		t.Fatalf("HashIncrBy got=%d err=%v", got, err)
	}
	if got, err := s.HashIncrByFloat("h", []byte("f"), 0.25); err != nil || got != "1.75" {
		t.Fatalf("HashIncrByFloat got=%q err=%v", got, err)
	}
	if ttl := s.TTL("h", true); ttl <= 0 {
		t.Fatalf("TTL lost: %d", ttl)
	}

	if _, err := s.HashSet("bad", [][]byte{[]byte("i"), []byte("f")}, [][]byte{[]byte("x"), []byte("NaN")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HashIncrBy("bad", []byte("i"), 1); err == nil {
		t.Fatal("accepted invalid integer")
	}
	if _, err := s.HashIncrByFloat("bad", []byte("f"), 1); err == nil {
		t.Fatal("accepted invalid float")
	}
	if _, err := s.HashIncrByFloat("h", []byte("f"), math.Inf(1)); err == nil {
		t.Fatal("accepted infinite increment")
	}
}

func TestHashScanCursorMatchAndGlob(t *testing.T) {
	s := New()
	fields := [][]byte{[]byte("aa"), []byte("ab"), []byte("ba"), []byte("bb")}
	values := [][]byte{[]byte("1"), []byte("2"), []byte("3"), []byte("4")}
	if _, err := s.HashSet("h", fields, values); err != nil {
		t.Fatal(err)
	}

	next, pairs, err := s.HashScan("h", 0, 2, nil)
	if err != nil || next != 2 || len(pairs) != 2 || string(pairs[0].Field) != "aa" || string(pairs[1].Field) != "ab" {
		t.Fatalf("first scan next=%d pairs=%v err=%v", next, pairs, err)
	}

	next, pairs, err = s.HashScan("h", next, 2, []byte("b?"))
	if err != nil || next != 0 || len(pairs) != 2 || string(pairs[0].Field) != "ba" || string(pairs[1].Field) != "bb" {
		t.Fatalf("second scan next=%d pairs=%v err=%v", next, pairs, err)
	}

	for _, tc := range []struct {
		pattern string
		value   string
		want    bool
	}{
		{"a*", "abc", true},
		{"a?c", "abc", true},
		{"[ab]1", "a1", true},
		{"[^a]1", "b1", true},
		{"x\\*", "x*", true},
		{"b[0-9]", "b7", true},
		{"b[0-9]", "ba", false},
	} {
		if got := hashGlobMatch([]byte(tc.pattern), []byte(tc.value)); got != tc.want {
			t.Fatalf("glob %q %q got=%t want=%t", tc.pattern, tc.value, got, tc.want)
		}
	}
}
