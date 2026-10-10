package engine

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
)

func TestBlockchainIdentifiersEncodeOnWrite(t *testing.T) {
	s := newEncodingStore(t)
	raw := make([]byte, 32)
	rand.Read(raw)
	hexLower := fmt.Sprintf("0x%x", raw)
	hexUpper := "0x" + strings.ToUpper(fmt.Sprintf("%x", raw))
	sig := "5VERv8NMvzbJMEkV8xnrLkEaWRtSz9CosKDYjCJjBRnbJLgp8uirBgmQpjKhoR4tjF3ZpRzrFmBV6UjKdiSZkQUW"
	pub := "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	balance := "115792089237316195423570985008687907853269984665640564039457584007913129639935"

	for _, tc := range []struct{ key, val, codec string }{
		{"h1", hexLower, "hex"},
		{"h2", hexUpper, "hex"},
		{"h3", hexLower[2:], "hex"},
		{"sig", sig, "base58"},
		{"pub", pub, "base58"},
		{"bal", balance, "bigdecimal"},
		{"bal2", "1000000000000000000000000", "bigdecimal"},
	} {
		if err := s.Set(tc.key, []byte(tc.val), 0); err != nil {
			t.Fatal(err)
		}
		got, ok := s.Get(tc.key)
		if !ok || !bytes.Equal(got, []byte(tc.val)) {
			t.Fatalf("%s: got %q ok=%v", tc.key, got, ok)
		}
		name, rawLen, stored, ok := s.Encoding(tc.key)
		if !ok || name != tc.codec || rawLen != len(tc.val) || stored >= len(tc.val) {
			t.Fatalf("%s: encoding=%q raw=%d stored=%d ok=%v, want %s", tc.key, name, rawLen, stored, ok, tc.codec)
		}
	}

	// Overwriting an identifier with ordinary text must not keep the old codec.
	if err := s.Set("h1", []byte("hello world, this is plain text now"), 0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("h1")
	if string(got) != "hello world, this is plain text now" {
		t.Fatalf("overwrite returned %q", got)
	}
	if name, _, _, _ := s.Encoding("h1"); name != "raw" {
		t.Fatalf("plain text encoding = %q", name)
	}
}

func TestIdentifierLookalikesStayRaw(t *testing.T) {
	s := newEncodingStore(t)
	for i, v := range []string{
		strings.Repeat("a", 40) + "g", // not hex
		"0X" + strings.Repeat("ab", 20),
		"user:" + strings.Repeat("0", 40),
		"The quick brown fox jumps over the lazy dog",
	} {
		k := fmt.Sprintf("k%d", i)
		s.Set(k, []byte(v), 0)
		got, _ := s.Get(k)
		if string(got) != v {
			t.Fatalf("%q round trip = %q", v, got)
		}
		if name, _, _, _ := s.Encoding(k); name == "hex" || name == "base58" || name == "bigdecimal" {
			t.Fatalf("%q encoded as %s", v, name)
		}
	}
}

func newEncodingStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewWithOptions(Options{Shards: 4, Encoding: true, Compression: true, ShapeEncoding: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
