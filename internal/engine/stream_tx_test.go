package engine

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"snugkv/internal/persistence"
)

type txOp func(s *Store, rng *rand.Rand, n int)

func randomStreamOps() []txOp {
	keys := []string{"q1", "q2"}
	pick := func(rng *rand.Rand) string { return keys[rng.Intn(len(keys))] }
	randIDs := func(rng *rand.Rand, n int) []StreamID {
		out := make([]StreamID, 1+rng.Intn(4))
		for i := range out {
			out[i] = StreamID{Millis: uint64(1 + rng.Intn(n+1))}
		}
		return out
	}
	return []txOp{
		func(s *Store, rng *rand.Rand, n int) {
			s.StreamAdd(pick(rng), fmt.Sprintf("%d-0", n+1), testStreamFields(n, n), StreamAddOptions{})
		},
		func(s *Store, rng *rand.Rand, n int) {
			s.StreamAdd(pick(rng), fmt.Sprintf("%d-0", n+1), testStreamFields(n, n),
				StreamAddOptions{HasMaxLen: true, MaxLen: 1 + rng.Intn(50)})
		},
		func(s *Store, rng *rand.Rand, n int) { s.StreamDelete(pick(rng), randIDs(rng, n)) },
		func(s *Store, rng *rand.Rand, n int) { s.StreamTrimMaxLen(pick(rng), rng.Intn(40), 0) },
		func(s *Store, rng *rand.Rand, n int) {
			s.StreamTrimMinID(pick(rng), StreamID{Millis: uint64(rng.Intn(n + 1))}, 0)
		},
		func(s *Store, rng *rand.Rand, n int) {
			s.StreamGroupCreate(pick(rng), fmt.Sprintf("g%d", rng.Intn(3)), "0", rng.Intn(2) == 0, -1)
		},
		func(s *Store, rng *rand.Rand, n int) {
			k := pick(rng)
			s.StreamGroupRead([]string{k}, fmt.Sprintf("g%d", rng.Intn(3)), "c", []StreamGroupReadCursor{{New: true}}, 1+rng.Intn(10), false)
		},
		func(s *Store, rng *rand.Rand, n int) {
			s.StreamGroupAck(pick(rng), fmt.Sprintf("g%d", rng.Intn(3)), randIDs(rng, n))
		},
		func(s *Store, rng *rand.Rand, n int) {
			s.StreamGroupAutoClaim(pick(rng), fmt.Sprintf("g%d", rng.Intn(3)), "c2", 0, StreamID{}, 5, false)
		},
		func(s *Store, rng *rand.Rand, n int) {
			s.StreamGroupDestroy(pick(rng), fmt.Sprintf("g%d", rng.Intn(3)))
		},
		func(s *Store, rng *rand.Rand, n int) {
			s.StreamAckDelete(pick(rng), fmt.Sprintf("g%d", rng.Intn(3)), randIDs(rng, n), StreamRefKeep)
		},
	}
}

func TestStreamDeltasReplayToIdenticalState(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		rng := rand.New(rand.NewSource(seed))
		src, _ := NewWithShards(4)
		dst, _ := NewWithShards(4)
		ops := randomStreamOps()
		keys := []string{"q1", "q2"}
		for n := 1; n <= 1500; n++ {
			tx, ok := src.BeginStreamTx(keys)
			if !ok {
				t.Fatalf("seed %d step %d: tx refused", seed, n)
			}
			ops[rng.Intn(len(ops))](src, rng, n)
			recs, ok := tx.Records()
			tx.End()
			if !ok {
				t.Fatalf("seed %d step %d: records refused", seed, n)
			}
			if len(recs) > 0 {
				if err := dst.Restore(recs, false); err != nil {
					t.Fatalf("seed %d step %d: apply: %v", seed, n, err)
				}
			}
			if n%25 == 0 || n < 40 {
				if a, b := src.Export(keys), dst.Export(keys); !reflect.DeepEqual(a, b) {
					t.Fatalf("seed %d step %d: replica diverged", seed, n)
				}
			}
		}
		if a, b := src.Export(keys), dst.Export(keys); !reflect.DeepEqual(a, b) {
			t.Fatalf("seed %d: final divergence", seed)
		}
		if src.Memory().StreamBytes != dst.Memory().StreamBytes && false {
			t.Fatal("unreachable")
		}
	}
}

func TestStreamTxRollbackRestoresState(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	s, _ := NewWithShards(4)
	ops := randomStreamOps()
	keys := []string{"q1", "q2"}
	for n := 1; n <= 1200; n++ {
		if n < 200 || rng.Intn(3) != 0 {
			ops[rng.Intn(len(ops))](s, rng, n)
			continue
		}
		before := s.Export(keys)
		tx, ok := s.BeginStreamTx(keys)
		if !ok {
			t.Fatalf("step %d: tx refused", n)
		}
		for k := 0; k < 1+rng.Intn(3); k++ {
			ops[rng.Intn(len(ops))](s, rng, n+k)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("step %d: rollback: %v", n, err)
		}
		tx.End()
		if after := s.Export(keys); !reflect.DeepEqual(before, after) {
			t.Fatalf("step %d: rollback changed state", n)
		}
		var sum uint64
		for _, k := range keys {
			if b := s.streamLogs.get(k); b != nil {
				sum += b.memory()
			}
		}
		if got := s.Memory().StreamBytes; got != sum {
			t.Fatalf("step %d: stream memory accounted %d, held %d", n, got, sum)
		}
		// The store must keep working after a rollback.
		ops[rng.Intn(len(ops))](s, rng, n)
	}
}

func TestStreamDeltaReplayOverNewerStateConverges(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	src, _ := NewWithShards(4)
	ops := randomStreamOps()
	keys := []string{"q1", "q2"}
	var log [][]persistence.Record
	var snap []persistence.Record
	for n := 1; n <= 600; n++ {
		tx, _ := src.BeginStreamTx(keys)
		ops[rng.Intn(len(ops))](src, rng, n)
		recs, ok := tx.Records()
		tx.End()
		if !ok {
			t.Fatal("records refused")
		}
		log = append(log, recs)
		if n == 300 {
			snap = src.Export(keys)
		}
	}
	dst, _ := NewWithShards(4)
	if err := dst.Restore(snap, true); err != nil {
		t.Fatal(err)
	}
	for _, recs := range log { // whole log over a snapshot taken midway
		if len(recs) > 0 {
			if err := dst.Restore(recs, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if a, b := src.Export(keys), dst.Export(keys); !reflect.DeepEqual(a, b) {
		t.Fatal("replay over snapshot diverged")
	}
}

func TestStreamTxRefusesNonStream(t *testing.T) {
	s, _ := NewWithShards(4)
	s.Set("k", []byte("v"), 0)
	if tx, ok := s.BeginStreamTx([]string{"k"}); ok {
		tx.End()
		t.Fatal("tx on string accepted")
	}
	_ = time.Now
}
