package engine

import (
	"bytes"
	"errors"
	"snugkv/internal/codec"
	"snugkv/internal/codec/jsonshape"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var ErrOOM = errors.New("OOM command not allowed when used memory > 'maxmemory'.")

type memoryAdmission uint8

const (
	enforceMemoryLimit memoryAdmission = iota
	allowOverMemoryLimit
)

func (s *Store) exceedsMemoryLimitLocked(next uint64, admission memoryAdmission) bool {
	return admission == enforceMemoryLimit &&
		s.memory.max > 0 &&
		next > s.memory.max
}

// Reservations track owned engine allocations, not total process RSS.
var entryStructBytes = uint64(unsafe.Sizeof(entry{}))
var entryMetaBytes = uint64(unsafe.Sizeof(entryMeta{}))

type Options struct {
	Shards        int
	MaxMemory     uint64
	Encoding      bool
	ShapeEncoding bool
	Compression   bool
}
type MemoryStats struct {
	AccountedBytes,
	MaxBytes,
	IndexReservedBytes,
	EntryBytes,
	ArenaBytes,
	ArenaPayloadBytes,
	ArenaLiveBlockBytes,
	SchemaBytes,
	MetaBytes uint64
}

type LayoutStats struct {
	EntryStructBytes     uint64
	EntryMetaStructBytes uint64
	IndexSlotBytes       uint64
	EntryCapacity        uint64
	EntryStorageBytes    uint64
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
		EntryStructBytes:     entryStructBytes,
		EntryMetaStructBytes: entryMetaBytes,
		IndexSlotBytes:       indexSlotBytes,
		EntryCapacity:        entryCapacity,
		EntryStorageBytes:    entryCapacity * entryStructBytes,
	}
}

type accounting struct {
	mu                                                                               sync.Mutex
	used, index, entries, max, arenas, arenaPayload, arenaLiveBlocks, schemas, metas uint64
}

func (s *Store) Memory() MemoryStats {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	return MemoryStats{
		AccountedBytes:      s.memory.used,
		MaxBytes:            s.memory.max,
		IndexReservedBytes:  s.memory.index,
		EntryBytes:          s.memory.entries,
		ArenaBytes:          s.memory.arenas,
		ArenaPayloadBytes:   s.memory.arenaPayload,
		ArenaLiveBlockBytes: s.memory.arenaLiveBlocks,
		SchemaBytes:         s.memory.schemas,
		MetaBytes:           s.memory.metas,
	}
}

// entryCharge is the live key-byte charge. Entry struct storage itself is
// charged by reserved []entry capacity, because deleted slots remain allocated
// and reusable.
func entryCharge(key string, _ any) uint64 {
	return uint64(len(key))
}

func metadataCharge(e entry) uint64 {
	if e.entryMeta == nil {
		return 0
	}
	return entryMetaBytes
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
			rawLength: uint32(rec.RawLength),
		},
		data: rec.Data,
	}
}

// makeEntryForShard uses an already-admitted JSON shape immediately.
//
// The caller must hold sh.mu. This avoids the normal codec/compression path
// when the shard already knows a strongly beneficial JSON representation.
func (s *Store) makeEntryForShard(sh *shard, value []byte) preparedEntry {
	if s.encoding &&
		s.shapeEncoding &&
		sh.shapes != nil &&
		structuredJSONCandidate(value) {

		schema, slots, ok := sh.shapes.Lookup(value)
		if ok {
			data := sh.shapes.EncodeSlots(slots)

			// Only take the direct path when JSON-shape is substantially
			// smaller than the logical value. This prevents a known but
			// weak shape from bypassing a potentially better compression
			// representation.
			if len(data)+16 < len(value)*3/4 {
				decoded, err := jsonshape.Decode(
					schema,
					data,
					len(value),
				)

				if err == nil && bytes.Equal(decoded, value) {
					return preparedEntry{
						entry: entry{
							entryMeta: &entryMeta{schema: schema},
							codecID:   5, // JSON-shape physical codec
							valueType: classifyValue(value),
							rawLength: uint32(len(value)),
						},
						data: data,
					}
				}
			}
		}
	}

	return s.makeEntry(value)
}

func (s *Store) decode(sh *shard, e entry) []byte {
	encoded := sh.encoded(e)
	if e.valueType == TypeHash && isShapedHash(encoded) {
		out, err := s.decodeShapedHash(encoded, int(e.rawLength))
		if err != nil {
			panic(err)
		}
		return out
	}

	var schema *jsonshape.Schema
	if e.entryMeta != nil {
		schema = e.entryMeta.schema
	}
	out, err := s.codecs.Decode(codec.Record{
		ID:        e.codecID,
		RawLength: int(e.rawLength),
		Data:      encoded,
		Schema:    schema,
	}, int(e.rawLength))
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
	return s.publishRecord(sh, key, e, enforceMemoryLimit)
}

func (s *Store) publishRecord(
	sh *shard,
	key string,
	e preparedEntry,
	admission memoryAdmission,
) error {
	old, exists := sh.get(key)

	if s.shouldTrackActivity(e.entry) && e.entryMeta == nil {
		e.entryMeta = &entryMeta{}
	}
	if s.shouldTrackActivity(e.entry) {
		meta := e.ensureMeta()
		if exists {
			meta.revision = nextEntryRevision(old)
		} else {
			meta.revision = 1
		}
	}

	var oldCost uint64
	if exists {
		oldCost = entryCharge(key, old)
	}

	newCost := entryCharge(key, e)
	oldMetaCost := metadataCharge(old)
	newMetaCost := metadataCharge(e.entry)

	extraIndex := uint64(0)
	extraEntries := uint64(0)

	if !exists {
		extraIndex = sh.data.GrowthBytes(1)
		extraEntries = sh.entryGrowthBytes(1)
	}

	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()

	extraArena := sh.arena.GrowthFor([]int{len(e.data)})
	if exists {
		extraArena += sh.arena.FreeGrowth(old.ref)
	}
	next := s.memory.used -
		oldCost -
		oldMetaCost +
		newCost +
		newMetaCost +
		extraIndex +
		extraEntries +
		extraArena

	var newSchema *jsonshape.Schema
	if e.entryMeta != nil {
		newSchema = e.entryMeta.schema
	}
	if newSchema != nil {
		if sh.shapes == nil || !sh.shapes.RetainRecord(newSchema, e.data) {
			return errors.New("ERR schema admission changed")
		}
	}

	if s.exceedsMemoryLimitLocked(next, admission) {
		if newSchema != nil {
			sh.shapes.ReleaseRecord(newSchema, e.data)
		}
		return ErrOOM
	}

	if exists && old.entryMeta != nil && old.entryMeta.schema != nil {
		sh.shapes.ReleaseRecord(old.entryMeta.schema, sh.encoded(old))
	}

	s.memory.used = next
	s.memory.entries =
		s.memory.entries - oldCost + newCost + extraEntries
	s.memory.metas = s.memory.metas - oldMetaCost + newMetaCost
	s.memory.index += extraIndex
	s.memory.arenas += extraArena

	if exists {
		s.memory.arenaPayload -= uint64(len(sh.encoded(old)))
	}

	s.memory.arenaPayload += uint64(len(e.data))

	if s.shouldTrackActivity(e.entry) {
		meta := e.ensureMeta()
		if meta.lastWrite.IsZero() {
			now := s.now()
			meta.lastWrite = activityStampOf(now)
			meta.lastAccess = meta.lastWrite
			meta.writes = 1

			if exists && old.entryMeta != nil && now.Sub(old.entryMeta.lastWrite.Time()) < time.Minute {
				if old.entryMeta.writes < ^uint8(0) {
					meta.writes = old.entryMeta.writes + 1
				} else {
					meta.writes = old.entryMeta.writes
				}
			}
		}
	}

	e.ref = sh.arena.Alloc(e.data)

	newBlockBytes := sh.arena.AllocationBytes(e.ref)

	if exists {
		oldBlockBytes := sh.arena.AllocationBytes(old.ref)
		s.memory.arenaLiveBlocks -= oldBlockBytes
	}

	s.memory.arenaLiveBlocks += newBlockBytes

	// The queue owns the expiration timestamp. The hot entry stores only this
	// one-byte presence bit so persistent reads never need a map lookup.
	e.hasExpiry = !e.expiresAt.IsZero()
	sh.set(key, e.entry)

	if exists {
		sh.arena.Free(old.ref)
	}

	sh.schedule(key, e.expiresAt)

	return nil
}

func (s *Store) remove(sh *shard, key string) {
	if e, ok := sh.get(key); ok {
		if sh.expired(key, e, s.now()) {
			atomic.AddUint64(&s.expired, 1)
		}
		freeGrowth := sh.arena.FreeGrowth(e.ref)
		s.memory.mu.Lock()
		if e.entryMeta != nil && e.entryMeta.schema != nil {
			sh.shapes.ReleaseRecord(e.entryMeta.schema, sh.encoded(e))
		}
		cost := entryCharge(key, e)
		metaCost := metadataCharge(e)
		s.memory.used -= cost + metaCost
		s.memory.used += freeGrowth
		s.memory.entries -= cost
		s.memory.metas -= metaCost
		s.memory.arenas += freeGrowth
		s.memory.arenaPayload -= uint64(len(sh.encoded(e)))
		s.memory.arenaLiveBlocks -= sh.arena.AllocationBytes(e.ref)
		s.memory.mu.Unlock()
		sh.delete(key)
		sh.arena.Free(e.ref)
		sh.schedule(key, 0)
	}
}
func (s *Store) Encoding(key string) (string, int, int, bool) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return "", 0, 0, false
	}
	return s.codecs.Name(e.codecID), int(e.rawLength), len(sh.encoded(e)), true
}

func (s *Store) MemoryUsage(key string) (uint64, bool) {
	sh := s.shardFor(key)

	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, false
	}

	// Per-key usage assigns one entry struct to this key. Global Memory()
	// additionally accounts for spare reserved entry capacity.
	entryBytes := entryStructBytes + uint64(len(key)) + metadataCharge(e)
	arenaBytes := sh.arena.AllocationBytes(e.ref)

	return entryBytes + arenaBytes, true
}

// MaxMemory returns the current runtime memory limit in accounted bytes.
// Zero means unlimited.
func (s *Store) MaxMemory() uint64 {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()

	return s.memory.max
}

// SetMaxMemory changes the runtime memory admission limit.
//
// Lowering the limit below current accounted usage is allowed, matching Redis:
// existing data remains resident, while subsequent memory-growing writes are
// subject to the configured eviction/OOM policy.
func (s *Store) SetMaxMemory(max uint64) {
	s.memory.mu.Lock()
	s.memory.max = max
	s.memory.mu.Unlock()
}