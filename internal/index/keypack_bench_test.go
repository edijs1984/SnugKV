package index

import (
	"fmt"
	"testing"
)

func benchKeys(n int, hex bool) []string {
	keys := make([]string, n)
	for i := range keys {
		x := uint64(i)*0x9E3779B97F4A7C15 + 1
		k := fmt.Sprintf("%016x%016x%016x%016x", x, x*31, x*131, x*977)
		if !hex {
			k = k[:63] + "z"
		}
		keys[i] = k
	}
	return keys
}

func benchGet(b *testing.B, hex bool) {
	keys := benchKeys(200000, hex)
	tbl := New[uint32]()
	for i, k := range keys {
		tbl.Set(k, uint32(i))
	}
	hashes := make([]uint64, len(keys))
	for i, k := range keys {
		hashes[i] = Hash(k)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j := (i * 7919) % len(keys)
		if _, ok := tbl.GetHashed(keys[j], hashes[j]); !ok {
			b.Fatal("miss")
		}
	}
	b.ReportMetric(float64(tbl.KeyLogBytes())/float64(len(keys)), "logB/key")
}

func BenchmarkGetPackedHex(b *testing.B) { benchGet(b, true) }
func BenchmarkGetRawKey(b *testing.B)    { benchGet(b, false) }

func benchSet(b *testing.B, hex bool) {
	keys := benchKeys(200000, hex)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tbl := New[uint32]()
		for j, k := range keys {
			tbl.Set(k, uint32(j))
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(keys)), "ns/insert")
}

func BenchmarkSetPackedHex(b *testing.B) { benchSet(b, true) }
func BenchmarkSetRawKey(b *testing.B)    { benchSet(b, false) }
