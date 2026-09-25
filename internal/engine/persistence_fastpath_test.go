package engine

import (
	"testing"
	"time"

	"snugkv/internal/index"
	"snugkv/internal/persistence"
)

func TestRestoreSingleRecordDoesNotLockUnrelatedShard(t *testing.T) {
	s := New()

	keyA := "restore-fast:a"
	shardA := s.shardFor(keyA)

	keyB := ""
	for i := 0; i < 10000; i++ {
		candidate := "restore-fast:b:" + string(rune(i))
		if s.shardForHash(index.Hash(candidate)) != shardA {
			keyB = candidate
			break
		}
	}
	if keyB == "" {
		t.Fatal("could not find key on a different shard")
	}

	shardA.mu.Lock()
	defer shardA.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- s.Restore([]persistence.Record{{
			Key:   []byte(keyB),
			Value: []byte("value"),
		}}, true)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("single-record Restore blocked on unrelated shard")
	}

	value, found, wrong := s.GetString(keyB)
	if !found || wrong || string(value) != "value" {
		t.Fatalf("restored value found=%v wrong=%v value=%q", found, wrong, value)
	}
}
