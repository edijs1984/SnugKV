package engine

import (
	"errors"
	"snugkv/internal/codec"
	"sync"
	"sync/atomic"
	"time"
)

var ErrOOM = errors.New("OOM command not allowed when used memory exceeds max_memory")

// Reservations are conservative charges for Go allocations, not measured RSS.
const entryOverhead uint64 = 32

type Options struct {
	Shards        int
	MaxMemory     uint64
	Encoding      bool
	ShapeEncoding bool
	Compression   bool
}
type MemoryStats struct{ AccountedBytes, MaxBytes, IndexReservedBytes, EntryBytes, ArenaBytes, SchemaBytes uint64 }
type accounting struct {
	mu                                         sync.Mutex
	used, index, entries, max, arenas, schemas uint64
}

func (s *Store) Memory() MemoryStats {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	return MemoryStats{s.memory.used, s.memory.max, s.memory.index, s.memory.entries, s.memory.arenas, s.memory.schemas}
}
func entryCharge(key string, e entry) uint64 {
	return entryOverhead + uint64(len(key)) + uint64(0)
}
func (s *Store) makeEntry(value []byte) entry {
	var rec codec.Record
	if s.encoding {
		rec = s.codecs.Encode(value)
	} else {
		rec = codec.Record{ID: codec.Raw, RawLength: len(value), Data: append([]byte(nil), value...)}
	}
	return entry{value: rec.Data, codecID: rec.ID, rawLength: rec.RawLength}
}
func (s *Store) decode(e entry) []byte {
	out, err := s.codecs.Decode(codec.Record{ID: e.codecID, RawLength: e.rawLength, Data: e.value, Schema: e.schema}, e.rawLength)
	// Only verified immutable records are published. A failure is an internal
	// invariant violation and must never silently return corrupt bytes.
	if err != nil {
		panic(err)
	}
	return out
}

// publish is called with the owning shard locked. Index reservations remain
// charged after deletion because Go maps can retain their bucket allocation.
func (s *Store) publish(sh *shard, key string, e entry) error {
	return s.publishRecord(sh, key, e, true)
}
func (s *Store) publishRecord(sh *shard, key string, e entry, enforce bool) error {
	old, exists := sh.get(key)
	var oldCost uint64
	if exists {
		oldCost = entryCharge(key, old)
	}
	newCost := entryCharge(key, e)
	extraIndex := uint64(0)
	if !exists {
		extraIndex = sh.data.GrowthBytes(1)
	}
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	extraArena := sh.arena.GrowthFor([]int{len(e.value)})
	next := s.memory.used - oldCost + newCost + extraIndex + extraArena
	if e.schema != nil {
		if sh.shapes == nil || !sh.shapes.RetainRecord(e.schema, e.value) {
			return errors.New("ERR schema admission changed")
		}
	}
	if enforce && s.memory.max > 0 && next > s.memory.max {
		if e.schema != nil {
			sh.shapes.ReleaseRecord(e.schema, e.value)
		}
		return ErrOOM
	}
	if old.schema != nil {
		sh.shapes.ReleaseRecord(old.schema, old.value)
	}
	s.memory.used = next
	s.memory.entries = s.memory.entries - oldCost + newCost
	s.memory.index += extraIndex
	s.memory.arenas += extraArena

	if e.lastWrite.IsZero() {
		e.lastWrite = stampOf(s.now())
		e.lastAccess = e.lastWrite
		e.writes = 1
		if exists && s.now().Sub(old.lastWrite.Time()) < time.Minute {
			e.writes = old.writes + 1
		}
	}
	e.version = atomic.AddUint64(&s.version, 1)
	e.ref = sh.arena.Alloc(e.value)
	e.value, _ = sh.arena.View(e.ref)
	sh.set(key, e)
	if exists {
		sh.arena.Free(old.ref)
	}
	sh.schedule(key, e.expiresAt)
	return nil
}
func (s *Store) remove(sh *shard, key string) {
	if e, ok := sh.get(key); ok {
		if e.expired(s.now()) {
			atomic.AddUint64(&s.expired, 1)
		}
		s.memory.mu.Lock()
		if e.schema != nil {
			sh.shapes.ReleaseRecord(e.schema, e.value)
		}
		cost := entryCharge(key, e)
		s.memory.used -= cost
		s.memory.entries -= cost
		s.memory.mu.Unlock()
		sh.delete(key)
		sh.arena.Free(e.ref)
		sh.schedule(key, 0)
		atomic.AddUint64(&s.version, 1)
	}
}
func (s *Store) Encoding(key string) (string, int, int, bool) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return "", 0, 0, false
	}
	return s.codecs.Name(e.codecID), e.rawLength, len(e.value), true
}

func (s *Store) MemoryUsage(key string) (uint64, bool) {
	sh := s.shardFor(key)

	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return 0, false
	}

	entryBytes := entryOverhead + uint64(len(key))
	arenaBytes := sh.arena.AllocationBytes(e.ref)

	return entryBytes + arenaBytes, true
}