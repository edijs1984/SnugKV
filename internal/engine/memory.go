package engine

import (
	"errors"
	"snugkv/internal/codec"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var ErrOOM = errors.New("OOM command not allowed when used memory exceeds max_memory")

// Reservations track owned engine allocations, not total process RSS.
var entryStructBytes = uint64(unsafe.Sizeof(entry{}))

type Options struct {
	Shards        int
	MaxMemory     uint64
	Encoding      bool
	ShapeEncoding bool
	Compression   bool
}
type MemoryStats struct{ AccountedBytes, MaxBytes, IndexReservedBytes, EntryBytes, ArenaBytes, ArenaPayloadBytes, ArenaLiveBlockBytes, SchemaBytes uint64 }

type LayoutStats struct {
	EntryStructBytes  uint64
	IndexSlotBytes    uint64
	EntryCapacity     uint64
	EntryStorageBytes uint64
}

func (s *Store) Layout() LayoutStats {
	var entryCapacity uint64
	var indexSlotBytes uint64

	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()

		entryCapacity += uint64(cap(sh.entries))

		if indexSlotBytes == 0 {
			indexSlotBytes = sh.data.EntryBytes()
		}

		sh.mu.RUnlock()
	}

	return LayoutStats{
		EntryStructBytes:  entryStructBytes,
		IndexSlotBytes:    indexSlotBytes,
		EntryCapacity:     entryCapacity,
		EntryStorageBytes: entryCapacity * entryStructBytes,
	}
}

type accounting struct {
	mu                                                                        sync.Mutex
	used, index, entries, max, arenas, arenaPayload, arenaLiveBlocks, schemas uint64
}

func (s *Store) Memory() MemoryStats {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	return MemoryStats{
		s.memory.used,
		s.memory.max,
		s.memory.index,
		s.memory.entries,
		s.memory.arenas,
		s.memory.arenaPayload,
		s.memory.arenaLiveBlocks,
		s.memory.schemas,
	}
}

// entryCharge is the live key-byte charge. Entry struct storage itself is
// charged by reserved []entry capacity, because deleted slots remain allocated
// and reusable.
func entryCharge(key string, _ any) uint64 {
	return uint64(len(key))
}

func (sh *shard) entryGrowthBytes(additional int) uint64 {
	next := sh.entryCapacityFor(additional)
	current := cap(sh.entries)

	if next <= current {
		return 0
	}

	return uint64(next-current) * entryStructBytes
}

func (s *Store) makeEntry(value []byte) preparedEntry {
	var rec codec.Record
	if s.encoding {
		rec = s.codecs.Encode(value)
	} else {
		rec = codec.Record{
			ID:        codec.Raw,
			RawLength: len(value),
			Data:      append([]byte(nil), value...),
		}
	}

	return preparedEntry{
		entry: entry{
			codecID:   rec.ID,
			valueType: classifyValue(value),
			rawLength: rec.RawLength,
		},
		data: rec.Data,
	}
}

func (s *Store) decode(e entry) []byte {
	out, err := s.codecs.Decode(codec.Record{
		ID:        e.codecID,
		RawLength: e.rawLength,
		Data:      e.encoded(),
		Schema:    e.schema,
	}, e.rawLength)
	// Only verified immutable records are published. A failure is an internal
	// invariant violation and must never silently return corrupt bytes.
	if err != nil {
		panic(err)
	}
	return out
}

// publish is called with the owning shard locked. Index reservations remain
// charged after deletion because Go maps can retain their bucket allocation.
func (s *Store) publish(sh *shard, key string, e preparedEntry) error {
	return s.publishRecord(sh, key, e, true)
}

func (s *Store) publishRecord(
	sh *shard,
	key string,
	e preparedEntry,
	enforce bool,
) error {
	old, exists := sh.get(key)

	var oldCost uint64
	if exists {
		oldCost = entryCharge(key, old)
	}

	newCost := entryCharge(key, e)

	extraIndex := uint64(0)
	extraEntries := uint64(0)

	if !exists {
		extraIndex = sh.data.GrowthBytes(1)
		extraEntries = sh.entryGrowthBytes(1)
	}

	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()

	extraArena := sh.arena.GrowthFor([]int{len(e.data)})
	next := s.memory.used -
		oldCost +
		newCost +
		extraIndex +
		extraEntries +
		extraArena

	if e.schema != nil {
		if sh.shapes == nil || !sh.shapes.RetainRecord(e.schema, e.data) {
			return errors.New("ERR schema admission changed")
		}
	}

	if enforce && s.memory.max > 0 && next > s.memory.max {
		if e.schema != nil {
			sh.shapes.ReleaseRecord(e.schema, e.data)
		}
		return ErrOOM
	}

	if old.schema != nil {
		sh.shapes.ReleaseRecord(old.schema, old.encoded())
	}

	s.memory.used = next
	s.memory.entries =
		s.memory.entries - oldCost + newCost + extraEntries
	s.memory.index += extraIndex
	s.memory.arenas += extraArena

	if exists {
		s.memory.arenaPayload -= uint64(len(old.encoded()))
	}

	s.memory.arenaPayload += uint64(len(e.data))

	if e.lastWrite.IsZero() {
		e.lastWrite = stampOf(s.now())
		e.lastAccess = e.lastWrite
		e.writes = 1

		if exists && s.now().Sub(old.lastWrite.Time()) < time.Minute {
			if old.writes < ^uint16(0) {
				e.writes = old.writes + 1
			} else {
				e.writes = old.writes
			}
		}
	}

	e.version = atomic.AddUint64(&s.version, 1)
	e.ref = sh.arena.Alloc(e.data)
	e.arena = &sh.arena

	newBlockBytes := sh.arena.AllocationBytes(e.ref)

	if exists {
		oldBlockBytes := sh.arena.AllocationBytes(old.ref)
		s.memory.arenaLiveBlocks -= oldBlockBytes
	}

	s.memory.arenaLiveBlocks += newBlockBytes

	sh.set(key, e.entry)

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
			sh.shapes.ReleaseRecord(e.schema, e.encoded())
		}
		cost := entryCharge(key, e)
		s.memory.used -= cost
		s.memory.entries -= cost
		s.memory.arenaPayload -= uint64(len(e.encoded()))
		s.memory.arenaLiveBlocks -= sh.arena.AllocationBytes(e.ref)
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
	return s.codecs.Name(e.codecID), e.rawLength, len(e.encoded()), true
}

func (s *Store) MemoryUsage(key string) (uint64, bool) {
	sh := s.shardFor(key)

	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return 0, false
	}

	// Per-key usage assigns one entry struct to this key. Global Memory()
	// additionally accounts for spare reserved entry capacity.
	entryBytes := entryStructBytes + uint64(len(key))
	arenaBytes := sh.arena.AllocationBytes(e.ref)

	return entryBytes + arenaBytes, true
}
