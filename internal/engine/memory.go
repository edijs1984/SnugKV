package engine

import (
	"errors"
	"snugkv/internal/arena"
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
	max := s.memory.max.Load()
	return admission == enforceMemoryLimit &&
		max > 0 &&
		next > max
}

// Reservations track owned engine allocations, not total process RSS.
var entryStructBytes = uint64(unsafe.Sizeof(entryData{}))
var entryMetaBytes = uint64(unsafe.Sizeof(entryMeta{}))
var entryMetaSlotBytes = uint64(unsafe.Sizeof((*entryMeta)(nil)))

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
	MetaBytes,
	SearchBytes uint64
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
	var entryStorageBytes uint64
	var indexSlotBytes uint64

	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()

		entryCapacity += uint64(cap(sh.entries))
		entryStorageBytes += uint64(cap(sh.entries)) * entryStructBytes
		if sh.metas != nil {
			entryStorageBytes += uint64(unsafe.Sizeof(entryMetaSidecar{})) +
				uint64(cap(sh.metas.slots))*entryMetaSlotBytes
		}

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
		EntryStorageBytes:    entryStorageBytes,
	}
}

type accounting struct {
	mu                                                                         sync.Mutex
	used, index, entries, arenas, arenaPayload, arenaLiveBlocks, schemas, metas uint64
	max                                                                        atomic.Uint64
}

func (s *Store) Memory() MemoryStats {
	searchBytes := s.SearchMemoryBytes()

	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	return MemoryStats{
		AccountedBytes:      s.memory.used + searchBytes,
		MaxBytes:            s.memory.max.Load(),
		IndexReservedBytes:  s.memory.index,
		EntryBytes:          s.memory.entries,
		ArenaBytes:          s.memory.arenas,
		ArenaPayloadBytes:   s.memory.arenaPayload,
		ArenaLiveBlockBytes: s.memory.arenaLiveBlocks,
		SchemaBytes:         s.memory.schemas,
		MetaBytes:           s.memory.metas,
		SearchBytes:         searchBytes,
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

func shouldInlinePrepared(e preparedEntry) bool {
	if e.entryMeta != nil || len(e.data) == 0 || len(e.data) > 8 {
		return false
	}
	switch e.codecID {
	case codec.Integer, codec.UnsignedInteger, codec.Float64, codec.Timestamp:
		return true
	default:
		return false
	}
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
	// preparedEntry is transient: publishRecordKnown copies data into the
	// shard arena before the caller can reuse the request buffer. For values
	// longer than 36 bytes no synchronous scalar codec can apply, so borrowing
	// the input here avoids cloning the raw payload only to copy it again into
	// the arena. The same borrowing is safe when encoding is disabled.
	if !s.encoding || len(value) > 36 {
		return preparedEntry{
			entry: entry{entryData: entryData{
				codecID:   codec.Raw,
				valueType: classifyValue(value),
				rawLength: uint32(len(value)),
			}},
			data: value,
		}
	}

	rec := s.codecs.Encode(value)
	return preparedEntry{
		entry: entry{entryData: entryData{
			codecID:   rec.ID,
			valueType: classifyValue(value),
			rawLength: uint32(rec.RawLength),
		}},
		data: rec.Data,
	}
}

// makeEntryForShard keeps foreground writes cheap.
//
// JSON-shape parsing, slot encoding, compression selection, and verification are
// intentionally background optimizer work. Performing those steps here after a
// schema becomes admitted makes SET latency depend on representation complexity
// and holds the shard lock during JSON parsing. Publish the normal synchronous
// scalar/raw representation and let the optimizer rewrite it asynchronously.
func (s *Store) makeEntryForShard(_ *shard, value []byte) preparedEntry {
	return s.makeEntry(value)
}

func (s *Store) decodeInto(sh *shard, e entry, dst []byte) []byte {
	var inline [8]byte
	encoded := sh.encodedInto(e, inline[:0])
	if e.valueType == TypeHash && isShapedHash(encoded) {
		return s.decode(sh, e)
	}

	var schema *jsonshape.Schema
	if e.entryMeta != nil && e.entryMeta.schemaID != 0 {
		if shapes := s.loadShapeStore(); shapes != nil {
			schema = shapes.ByID(e.entryMeta.schemaID)
		}
	}
	out, err := s.codecs.DecodeInto(codec.Record{
		ID:        e.codecID,
		RawLength: int(e.rawLength),
		Data:      encoded,
		Schema:    schema,
	}, int(e.rawLength), dst)
	if err != nil {
		panic(err)
	}
	return out
}

func (s *Store) decode(sh *shard, e entry) []byte {
	var inline [8]byte
	encoded := sh.encodedInto(e, inline[:0])
	if e.valueType == TypeHash && isShapedHash(encoded) {
		out, err := s.decodeShapedHash(encoded, int(e.rawLength))
		if err != nil {
			panic(err)
		}
		return out
	}

	var schema *jsonshape.Schema
	if e.entryMeta != nil && e.entryMeta.schemaID != 0 {
		if shapes := s.loadShapeStore(); shapes != nil {
			schema = shapes.ByID(e.entryMeta.schemaID)
		}
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
	return s.publishRecordKnown(sh, key, e, admission, old, exists)
}

func (s *Store) publishRecordKnown(
	sh *shard,
	key string,
	e preparedEntry,
	admission memoryAdmission,
	old entry,
	exists bool,
) error {
	return s.publishRecordKnownWithHash(sh, key, 0, false, e, admission, old, exists)
}

func (s *Store) publishRecordKnownHashed(
	sh *shard,
	key string,
	hash uint64,
	e preparedEntry,
	admission memoryAdmission,
	old entry,
	exists bool,
) error {
	return s.publishRecordKnownWithHash(sh, key, hash, true, e, admission, old, exists)
}

func (s *Store) publishRecordKnownWithHash(
	sh *shard,
	key string,
	hash uint64,
	hashKnown bool,
	e preparedEntry,
	admission memoryAdmission,
	old entry,
	exists bool,
) error {
	var oldCost uint64
	if exists {
		oldCost = entryCharge(key, old)
	}

	newCost := entryCharge(key, e)
	oldMetaCost := metadataCharge(old)
	newMetaCost := metadataCharge(e.entry)

	extraIndex := uint64(0)
	extraEntries := uint64(0)
	additionalEntries := 0
	if !exists {
		additionalEntries = 1
		extraIndex = sh.data.GrowthBytes(1)
		extraEntries = sh.entryGrowthBytes(1)
	}
	extraMetaSlots := sh.metaSlotGrowthBytes(additionalEntries, e.entryMeta != nil)

	// Tiny encoded scalars fit directly in arena.Ref, so they need no arena
	// block and do not contribute arena payload/live-block accounting.
	inlineNew := shouldInlinePrepared(e)
	extraArena := uint64(0)
	if !inlineNew {
		extraArena = sh.arena.GrowthFor([]int{len(e.data)})
	}
	if exists {
		extraArena += sh.arena.FreeGrowth(old.ref)
	}
	newBlockBytes := uint64(0)
	if !inlineNew {
		newBlockBytes = arena.AllocationBytesForLength(len(e.data))
	}
	oldBlockBytes := uint64(0)
	if exists {
		oldBlockBytes = sh.arena.AllocationBytes(old.ref)
	}

	var newSchema *jsonshape.Schema
	if e.entryMeta != nil && e.entryMeta.schemaID != 0 {
		if sh.shapes != nil {
			newSchema = sh.shapes.ByID(e.entryMeta.schemaID)
		}
		if newSchema == nil {
			return errors.New("ERR schema handle is invalid")
		}
	}

	// Reserve the accounting delta atomically with maxmemory admission, but do
	// not hold the global accounting mutex across the physical shard publish.
	s.memory.mu.Lock()

	next := s.memory.used -
		oldCost -
		oldMetaCost +
		newCost +
		newMetaCost +
		extraIndex +
		extraEntries +
		extraMetaSlots +
		extraArena

	if newSchema != nil {
		if sh.shapes == nil || !sh.shapes.RetainRecord(newSchema, e.data) {
			s.memory.mu.Unlock()
			return errors.New("ERR schema admission changed")
		}
	}

	if s.exceedsMemoryLimitLocked(next, admission) {
		if newSchema != nil {
			sh.shapes.ReleaseRecord(newSchema, e.data)
		}
		s.memory.mu.Unlock()
		return ErrOOM
	}

	if exists && old.entryMeta != nil && old.entryMeta.schemaID != 0 {
		oldSchema := sh.shapes.ByID(old.entryMeta.schemaID)
		if oldSchema == nil {
			s.memory.mu.Unlock()
			return errors.New("ERR stored schema handle is invalid")
		}
		sh.shapes.ReleaseRecord(oldSchema, sh.encoded(old))
	}

	s.memory.used = next
	s.memory.entries =
		s.memory.entries - oldCost + newCost + extraEntries + extraMetaSlots
	s.memory.metas = s.memory.metas - oldMetaCost + newMetaCost
	s.memory.index += extraIndex
	s.memory.arenas += extraArena

	if exists {
		if !old.ref.IsInline() {
			s.memory.arenaPayload -= uint64(len(sh.encoded(old)))
		}
		s.memory.arenaLiveBlocks -= oldBlockBytes
	}

	if !inlineNew {
		s.memory.arenaPayload += uint64(len(e.data))
	}
	s.memory.arenaLiveBlocks += newBlockBytes

	s.memory.mu.Unlock()

	if s.shouldTrackActivity(e.entry) && e.entryMeta != nil {
		meta := e.entryMeta
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

	if inlineNew {
		ref, ok := sh.arena.AllocInline(e.data)
		if !ok {
			panic("inline scalar admission invariant")
		}
		e.ref = ref
	} else {
		e.ref = sh.arena.Alloc(e.data)
	}

	// The queue owns the expiration timestamp. The hot entry stores only this
	// one-byte presence bit so persistent reads never need a map lookup.
	e.hasExpiry = !e.expiresAt.IsZero()
	if hashKnown {
		sh.setKnownHashed(key, hash, e.entry, exists)
	} else {
		sh.set(key, e.entry)
	}

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
		if e.entryMeta != nil && e.entryMeta.schemaID != 0 {
			schema := sh.shapes.ByID(e.entryMeta.schemaID)
			if schema == nil {
				s.memory.mu.Unlock()
				panic("stored schema handle is invalid")
			}
			sh.shapes.ReleaseRecord(schema, sh.encoded(e))
		}
		cost := entryCharge(key, e)
		metaCost := metadataCharge(e)
		s.memory.used -= cost + metaCost
		s.memory.used += freeGrowth
		s.memory.entries -= cost
		s.memory.metas -= metaCost
		s.memory.arenas += freeGrowth
		if !e.ref.IsInline() {
			s.memory.arenaPayload -= uint64(len(sh.encoded(e)))
		}
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
	if sh.metas != nil {
		entryBytes += entryMetaSlotBytes
	}
	arenaBytes := sh.arena.AllocationBytes(e.ref)

	return entryBytes + arenaBytes, true
}

// MaxMemory returns the current runtime memory limit in accounted bytes.
// Zero means unlimited.
func (s *Store) MaxMemory() uint64 {
	return s.memory.max.Load()
}

// SetMaxMemory changes the runtime memory admission limit.
//
// Lowering the limit below current accounted usage is allowed, matching Redis:
// existing data remains resident, while subsequent memory-growing writes are
// subject to the configured eviction/OOM policy.
func (s *Store) SetMaxMemory(max uint64) {
	s.memory.max.Store(max)
}
