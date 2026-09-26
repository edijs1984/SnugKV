package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func BenchmarkConcurrentGetHandlers256(b *testing.B) {
	store := engine.New()
	key := []byte("bench:000123456")
	value := make([]byte, 256)
	for i := range value {
		value[i] = byte(i)
	}
	if err := store.Set(string(key), value, 0); err != nil {
		b.Fatal(err)
	}
	serv := New(store)

	b.Run("raw-zero-copy", func(b *testing.B) {
		args := [][]byte{[]byte("GET"), key}
		var n int
		writeBulk := func(v []byte) error {
			n += len(v)
			return nil
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			handled, err := serv.executeAuthorizedConcurrentRawGet(args, writeBulk)
			if err != nil || !handled {
				b.Fatalf("handled=%t err=%v", handled, err)
			}
		}
		if n == 0 {
			b.Fatal("zero bytes observed")
		}
	})

	b.Run("decode-into", func(b *testing.B) {
		dst := make([]byte, 0, 256)
		now := time.Now()

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got, found, handled, err :=
				serv.executeAuthorizedConcurrentKnownGetIntoAt(key, dst, now)
			if err != nil || !handled || !found {
				b.Fatalf("found=%t handled=%t err=%v", found, handled, err)
			}
			dst = got[:0]
		}
	})
}
