package index

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func packTestKeys() []string {
	h64 := strings.Repeat("a1b2c3d4", 8)
	return []string{
		"",
		"a",
		"bench:000000001",
		h64,
		strings.ToUpper(h64),
		"0x" + h64,
		"0x" + strings.ToUpper(h64),
		"0X" + h64,
		"tx:" + h64,
		"tx:0x" + h64,
		"tx:" + strings.ToUpper(h64),
		"0xAbCdEf0123456789aBcDeF0123456789AbCdEf01",
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		"balance:0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		strings.Repeat("0", 16),
		strings.Repeat("0", 15),
		strings.Repeat("f", 17),
		"cafe" + h64,
		"cafebabe" + strings.Repeat("0", 63),
		strings.Repeat("ab", 64),
		strings.Repeat("ab", 65),
		strings.Repeat("x", 300) + h64,
		strings.Repeat("x", 255) + h64,
		strings.Repeat("x", 256) + h64,
		"12345678",
		"user:1234567890123456",
		h64 + ":suffix",
		"héllo" + h64,
	}
}

func TestPackKeyRoundTrip(t *testing.T) {
	for _, key := range packTestKeys() {
		tbl := New[uint32]()
		tbl.Set(key, 7)
		if v, ok := tbl.Get(key); !ok || v != 7 {
			t.Fatalf("%q: get after set = %v %v", key, v, ok)
		}
		var got []string
		for k := range tbl.All() {
			got = append(got, k)
		}
		if len(got) != 1 || got[0] != key {
			t.Fatalf("%q: All = %q", key, got)
		}
		if size := KeyRecordSize(key); size != len(tbl.log) {
			t.Fatalf("%q: KeyRecordSize %d but log holds %d", key, size, len(tbl.log))
		}
		tbl.Delete(key)
		if _, ok := tbl.Get(key); ok || tbl.Len() != 0 {
			t.Fatalf("%q: still present after delete", key)
		}
	}
}

func TestPackKeySavesSpace(t *testing.T) {
	h64 := strings.Repeat("a1b2c3d4", 8)
	for _, tc := range []struct {
		key string
		max int
	}{
		{h64, 35},
		{"0x" + h64, 35},
		{"0x" + strings.Repeat("a1b2c3d4", 5), 23},
		{"tx:" + h64, 39},
	} {
		if got := KeyRecordSize(tc.key); got > tc.max {
			t.Errorf("%q: record %d bytes, want at most %d", tc.key, got, tc.max)
		}
	}
	for _, key := range []string{"bench:000000001", "12345678", strings.Repeat("0", 15)} {
		if got, raw := KeyRecordSize(key), 1+len(key); got != raw {
			t.Errorf("%q: size %d, want raw %d", key, got, raw)
		}
	}
}

func TestPackKeysNeverCollide(t *testing.T) {
	// Keys that differ only in case or prefix must stay distinct.
	h := strings.Repeat("ab", 16)
	keys := []string{h, strings.ToUpper(h), "0x" + h, "0x" + strings.ToUpper(h), "p" + h, "pp" + h, "0Xab" + h}
	tbl := New[uint32]()
	for i, k := range keys {
		tbl.Set(k, uint32(i))
	}
	for i, k := range keys {
		if v, ok := tbl.Get(k); !ok || v != uint32(i) {
			t.Fatalf("%q: got %v %v want %d", k, v, ok, i)
		}
	}
	if _, ok := tbl.Get("0x" + h + "0"); ok {
		t.Fatal("unexpected hit")
	}
	if _, ok := tbl.Get(h[:len(h)-1]); ok {
		t.Fatal("unexpected hit on truncated key")
	}
}

func randomKey(r *rand.Rand) string {
	const hexLower, hexUpper = "0123456789abcdef", "0123456789ABCDEF"
	switch r.Intn(6) {
	case 0:
		return fmt.Sprintf("bench:%09d", r.Intn(1e9))
	case 1:
		return ""
	}
	digits := 16 + 2*r.Intn(40)
	if r.Intn(5) == 0 {
		digits++
	}
	var b strings.Builder
	switch r.Intn(4) {
	case 1:
		b.WriteString("tx:")
	case 2:
		b.WriteString("0x")
	case 3:
		b.WriteString("k" + fmt.Sprint(r.Intn(1000)) + "_0x")
	}
	mode := r.Intn(3)
	for i := 0; i < digits; i++ {
		switch mode {
		case 0:
			b.WriteByte(hexLower[r.Intn(16)])
		case 1:
			b.WriteByte(hexUpper[r.Intn(16)])
		default:
			if r.Intn(2) == 0 {
				b.WriteByte(hexLower[r.Intn(16)])
			} else {
				b.WriteByte(hexUpper[r.Intn(16)])
			}
		}
	}
	return b.String()
}

func TestPackedTableAgainstMap(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	tbl := New[uint32]()
	ref := map[string]uint32{}
	for step := 0; step < 60000; step++ {
		k := randomKey(r)
		switch r.Intn(4) {
		case 0, 1:
			v := uint32(r.Intn(1 << 20))
			tbl.Set(k, v)
			ref[k] = v
		case 2:
			tbl.Delete(k)
			delete(ref, k)
		default:
			got, ok := tbl.Get(k)
			want, wok := ref[k]
			if ok != wok || got != want {
				t.Fatalf("step %d key %q: got %v %v want %v %v", step, k, got, ok, want, wok)
			}
		}
		if step%9000 == 8999 {
			tbl.Compact()
		}
	}
	if tbl.Len() != len(ref) {
		t.Fatalf("len %d want %d", tbl.Len(), len(ref))
	}
	seen := map[string]bool{}
	for k, v := range tbl.All() {
		if want, ok := ref[k]; !ok || want != v {
			t.Fatalf("All yielded %q=%d, reference %v %v", k, v, want, ok)
		}
		seen[k] = true
	}
	if len(seen) != len(ref) {
		t.Fatalf("All yielded %d keys, want %d", len(seen), len(ref))
	}
	for k, want := range ref {
		if got, ok := tbl.Get(k); !ok || got != want {
			t.Fatalf("final get %q: %v %v want %d", k, got, ok, want)
		}
	}
	var live uint64
	for k := range ref {
		live += uint64(KeyRecordSize(k))
	}
	tbl.Compact()
	if uint64(len(tbl.log)) != live {
		t.Fatalf("log %d bytes after compact, live records need %d", len(tbl.log), live)
	}
}

func TestPackedSample(t *testing.T) {
	tbl := New[uint32]()
	want := map[string]bool{}
	for i := 0; i < 500; i++ {
		k := fmt.Sprintf("%064x", i*7919+1)
		tbl.Set(k, uint32(i))
		want[k] = true
	}
	cursor := 0
	seen := map[string]bool{}
	for n := 0; n < 100 && len(seen) < len(want); n++ {
		var keys []string
		keys, cursor = tbl.Sample(cursor, 64, 16)
		for _, k := range keys {
			if !want[k] {
				t.Fatalf("sample returned unknown key %q", k)
			}
			seen[k] = true
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("sampled %d of %d keys", len(seen), len(want))
	}
}

func FuzzPackKey(f *testing.F) {
	for _, k := range packTestKeys() {
		f.Add(k)
	}
	f.Fuzz(func(t *testing.T, key string) {
		tbl := New[uint32]()
		tbl.Set(key, 1)
		tbl.Set("other-"+key, 2)
		if v, ok := tbl.Get(key); !ok || v != 1 {
			t.Fatalf("lost key %q", key)
		}
		for k := range tbl.All() {
			if k != key && k != "other-"+key {
				t.Fatalf("decoded %q from %q", k, key)
			}
		}
		tbl.Compact()
		if v, ok := tbl.Get(key); !ok || v != 1 {
			t.Fatalf("lost key %q after compact", key)
		}
	})
}

func TestAllRefsMatchAll(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	tbl := New[uint32]()
	for i := 0; i < 5000; i++ {
		tbl.Set(randomKey(r), uint32(i))
	}
	want := map[string]uint32{}
	for k, v := range tbl.All() {
		want[k] = v
	}
	n := 0
	for ref, v := range tbl.AllRefs() {
		k := tbl.Key(ref)
		if want[k] != v || tbl.KeyLen(ref) != len(k) {
			t.Fatalf("ref for %q: value %d len %d, want %d len %d", k, v, tbl.KeyLen(ref), want[k], len(k))
		}
		n++
	}
	if n != len(want) {
		t.Fatalf("AllRefs yielded %d, All %d", n, len(want))
	}
}
