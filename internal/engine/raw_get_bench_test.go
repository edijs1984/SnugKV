package engine

import (
	"testing"

	"snugkv/internal/codec"
	"snugkv/internal/index"
)

func benchmarkVisitRawStringOldStyle(b *testing.B, store *Store, keyBytes []byte) {
	b.Helper()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := string(keyBytes)
		hash := index.Hash(key)
		sh := store.shardForHash(hash)
		sh.mu.RLock()

		e, ok := sh.getHashed(key, hash)
		if !ok || sh.expired(key, e, store.now()) || isNativeContainerType(e.valueType) || e.codecID != codec.Raw {
			sh.mu.RUnlock()
			b.Fatal("raw string lookup unexpectedly missed")
		}
		_ = sh.encoded(e)
		sh.mu.RUnlock()
	}
}

func benchmarkVisitRawStringBytesStyle(b *testing.B, store *Store, keyBytes []byte) {
	b.Helper()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		hash := index.HashBytes(keyBytes)
		sh := store.shardForHash(hash)
		sh.mu.RLock()

		e, ok := sh.getHashedBytes(keyBytes, hash)
		if !ok {
			sh.mu.RUnlock()
			b.Fatal("raw byte lookup unexpectedly missed")
		}
		if e.hasExpiry && sh.expired(string(keyBytes), e, store.now()) {
			sh.mu.RUnlock()
			b.Fatal("raw byte lookup unexpectedly expired")
		}
		if isNativeContainerType(e.valueType) || e.codecID != codec.Raw {
			sh.mu.RUnlock()
			b.Fatal("raw byte lookup unexpectedly rejected value")
		}
		_ = sh.encoded(e)
		sh.mu.RUnlock()
	}
}

func BenchmarkRawGetKeyLookup(b *testing.B) {
	store := New()
	keyBytes := []byte("bench:000123456")
	value := make([]byte, 256)
	if err := store.Set(string(keyBytes), value, 0); err != nil {
		b.Fatal(err)
	}

	b.Run("string", func(b *testing.B) {
		benchmarkVisitRawStringOldStyle(b, store, keyBytes)
	})
	b.Run("bytes", func(b *testing.B) {
		benchmarkVisitRawStringBytesStyle(b, store, keyBytes)
	})
}
