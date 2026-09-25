package engine

import (
	"errors"
	"math"
	"snugkv/internal/arena"
	"snugkv/internal/codec"
	"snugkv/internal/index"
	"strconv"
	"sync/atomic"
	"time"
)

type entryMeta struct {
	lastRewrite, lastOptimize, lastAccess, lastWrite activityStamp
	schemaID                                          uint32
	reads, writes                                    uint8
}

type entryData struct {
	ref       arena.Ref
	rawLength uint32
	codecID   codec.ID
	valueType ValueType
	hasExpiry bool
}

// entry is a transient view over compact stored entry data plus optional
// metadata. Shards store entryData densely and keep metadata pointers in a
// lazily allocated sidecar, so metadata-free workloads do not pay 8 bytes per
// entry for a nil pointer.
type entry struct {
	entryData
	*entryMeta
}

type preparedEntry struct {
	entry
	expiresAt stamp
	data      []byte
}

func (e *entry) ensureMeta() *entryMeta {
	if e.entryMeta == nil {
		e.entryMeta = &entryMeta{}
	}
	return e.entryMeta
}

func cloneEntryMeta(meta *entryMeta) *entryMeta {
	if meta == nil {
		return nil
	}
	clone := *meta
	return &clone
}

func isNativeContainerType(t ValueType) bool {
	return t == TypeHash || t == TypeSet || t == TypeList || t == TypeZSet || t == TypeStream || t == TypeBloom || t == TypeCuckoo || t == TypeCMS || t == TypeTopK || t == TypeTDigest || t == TypeTimeSeries
}

func (s *Store) shouldTrackActivity(e entry) bool {
	return s.encoding && !isNativeContainerType(e.valueType)
}

func (sh *shard) encoded(e entry) []byte {
	value, err := sh.arena.View(e.ref)
	if err != nil {
		panic(err)
	}
	return value
}

func (sh *shard) encodedInto(e entry, dst []byte) []byte {
	if e.ref.IsInline() {
		value, ok := e.ref.InlineInto(dst)
		if !ok {
			panic("invalid inline reference")
		}
		return value
	}
	return sh.arena.ViewTrusted(e.ref)
}

func (sh *shard) expirationAt(key string, e entry) stamp {
	if !e.hasExpiry {
		return 0
	}
	at, ok := sh.expiration.atFor(key)
	if !ok {
		panic("expiration index invariant")
	}
	return at
}

func (sh *shard) expired(key string, e entry, now time.Time) bool {
	if !e.hasExpiry {
		return false
	}
	return stampOf(now) >= sh.expirationAt(key, e)
}

// Store owns immutable byte values. Returned values belong to the caller.
const shapeStoreBaseBytes = uint64(2*256 + 2048 + 1024)

type Store struct {
	expired, evicted uint64
	shards           []shard
	now              func() time.Time
	codecs           *codec.Registry
	encoding         bool
	shapeEncoding    bool
	compression      bool
	memory           accounting
	sampleCursor     uint64
	shapeCatalog     globalShapeCatalog
	hashShapes       hashShapeCatalog
	search           atomic.Pointer[searchManager]
}

func New() *Store                             { s, _ := NewWithShards(256); return s }
func NewWithShards(count int) (*Store, error) { return NewWithOptions(Options{Shards: count}) }
func NewWithOptions(options Options) (*Store, error) {
	count := options.Shards
	if count <= 0 || count&(count-1) != 0 || count > 65536 {
		return nil, errors.New("shards must be a positive power of two at most 65536")
	}
	base := structuralMemoryBytes(count)
	if options.MaxMemory > 0 && options.MaxMemory < base {
		return nil, ErrOOM
	}
	s := &Store{
		shards:        make([]shard, count),
		now:           time.Now,
		codecs:        codec.NewRegistry(),
		encoding:      options.Encoding,
		shapeEncoding: options.ShapeEncoding,
		compression:   options.Compression,
		memory: accounting{
			used:  base,
			index: base,
		},
	}
	s.memory.max.Store(options.MaxMemory)
	for i := range s.shards {
		s.shards[i].data = *index.New[uint32]()
	}
	return s, nil
}
func (s *Store) Set(key string, value []byte, ttlMilliseconds int64) error {
	if ttlMilliseconds < 0 || ttlMilliseconds > math.MaxInt64/int64(time.Millisecond) {
		return errors.New("ttl out of range")
	}
	return s.SetWithTTL(key, value, time.Duration(ttlMilliseconds)*time.Millisecond)
}
func (s *Store) SetWithTTL(key string, value []byte, ttl time.Duration) error {
	_, err := s.SetConditional(key, value, SetOptions{TTL: ttl})
	return err
}
func (s *Store) Get(key string) ([]byte, bool) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, false
	}
	// Metadata is admitted on publication. Do not lazily allocate it from a
	// read path, because GET has no error channel for max-memory admission.
	if s.shouldTrackActivity(e) && e.entryMeta != nil {
		now := s.now()
		meta := e.entryMeta
		if now.Sub(meta.lastAccess.Time()) > time.Minute {
			meta.reads = 0
		}
		meta.lastAccess = activityStampOf(now)
		if meta.reads < ^uint8(0) {
			meta.reads++
		}
		sh.set(key, e)
	}
	return s.decode(sh, e), true
}

// VisitRawString exposes an immutable raw string only for stores with encoding
// disabled. The visitor runs while the owning shard is read-locked, so the arena
// bytes stay valid without cloning. Missing, expired, encoded and native
// container values return handled=false and use the ordinary GET path.
func (s *Store) VisitRawString(key string, visit func([]byte) error) (handled bool, err error) {
	if s.encoding {
		return false, nil
	}

	hash := index.Hash(key)
	sh := s.shardForHash(hash)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.getHashed(key, hash)
	if !ok || sh.expired(key, e, s.now()) || isNativeContainerType(e.valueType) || e.codecID != codec.Raw {
		return false, nil
	}

	return true, visit(sh.encoded(e))
}

// GetString performs the Redis string GET type check and value lookup under one
// shard lock. wrongType is true only for native container values that GET must
// reject; missing/expired keys return found=false.
func (s *Store) GetStringInto(key string, dst []byte) (value []byte, found bool, wrongType bool) {
	hash := index.Hash(key)
	sh := s.shardForHash(hash)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.getHashed(key, hash)
	if !ok {
		return nil, false, false
	}
	now := s.now()
	if sh.expired(key, e, now) {
		return nil, false, false
	}
	if isNativeContainerType(e.valueType) {
		return nil, false, true
	}

	if s.shouldTrackActivity(e) && e.entryMeta != nil {
		meta := e.entryMeta
		if meta.lastAccess.IsOlderThan(now, time.Minute) {
			meta.reads = 0
		}
		meta.lastAccess = activityStampOf(now)
		if meta.reads < ^uint8(0) {
			meta.reads++
		}
		// entryMeta is shared by pointer with the stored entry. Updating the
		// pointed-to metadata is sufficient; rewriting the entry/index on every
		// GET only adds lock-held work.
	}

	return s.decodeInto(sh, e, dst), true, false
}

func (s *Store) GetStringBytesInto(key []byte, dst []byte) (value []byte, found bool, wrongType bool) {
	return s.GetStringBytesIntoAt(key, dst, s.now())
}

func (s *Store) GetStringBytesIntoAt(key []byte, dst []byte, now time.Time) (value []byte, found bool, wrongType bool) {
	hash := index.HashBytes(key)
	sh := s.shardForHash(hash)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.getHashedBytes(key, hash)
	if !ok {
		return nil, false, false
	}

	if e.hasExpiry && sh.expired(string(key), e, now) {
		return nil, false, false
	}
	if isNativeContainerType(e.valueType) {
		return nil, false, true
	}

	if s.shouldTrackActivity(e) && e.entryMeta != nil {
		meta := e.entryMeta
		if meta.lastAccess.IsOlderThan(now, time.Minute) {
			meta.reads = 0
		}
		meta.lastAccess = activityStampOf(now)
		if meta.reads < ^uint8(0) {
			meta.reads++
		}
	}

	return s.decodeInto(sh, e, dst), true, false
}

func (s *Store) GetString(key string) (value []byte, found bool, wrongType bool) {
	hash := index.Hash(key)
	sh := s.shardForHash(hash)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.getHashed(key, hash)
	if !ok {
		return nil, false, false
	}
	now := s.now()
	if sh.expired(key, e, now) {
		return nil, false, false
	}

	if isNativeContainerType(e.valueType) {
		return nil, false, true
	}

	// Keep the same activity accounting semantics as Get.
	if s.shouldTrackActivity(e) && e.entryMeta != nil {
		meta := e.entryMeta
		if meta.lastAccess.IsOlderThan(now, time.Minute) {
			meta.reads = 0
		}
		meta.lastAccess = activityStampOf(now)
		if meta.reads < ^uint8(0) {
			meta.reads++
		}
		// entryMeta is shared by pointer with the stored entry.
	}

	return s.decode(sh, e), true, false
}
func (s *Store) Delete(key string) bool {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	expired := ok && sh.expired(key, e, s.now())
	s.remove(sh, key)
	return ok && !expired
}
func (s *Store) Incr(key string) (int64, error) { return s.Add(key, 1) }
func (s *Store) Decr(key string) (int64, error) { return s.Add(key, -1) }
func (s *Store) Add(key string, delta int64) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if ok && sh.expired(key, e, s.now()) {
		s.remove(sh, key)
		e = entry{}
		ok = false
	}
	var n int64
	if ok {
		var err error
		n, err = strconv.ParseInt(string(s.decode(sh, e)), 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != string(s.decode(sh, e)) {
			return 0, errors.New("ERR value is not an integer or out of range")
		}
	}
	if (delta > 0 && n > math.MaxInt64-delta) || (delta < 0 && n < math.MinInt64-delta) {
		return 0, errors.New("ERR increment or decrement would overflow")
	}
	n += delta
	updated := s.makeEntry([]byte(strconv.FormatInt(n, 10)))
	if ok {
		updated.expiresAt = sh.expirationAt(key, e)
	}
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return n, nil
}

// TTL returns -2 for missing keys, -1 for persistent keys, otherwise remaining
// time in the requested unit. Seconds are rounded to the nearest second.
func (s *Store) TTL(key string, milliseconds bool) int64 {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	now := s.now()
	if !ok || sh.expired(key, e, now) {
		return -2
	}
	if !e.hasExpiry {
		return -1
	}
	at := sh.expirationAt(key, e)
	ms := at.Sub(now).Milliseconds()
	if milliseconds {
		return ms
	}
	return ms/1000 + (ms%1000+500)/1000
}

// CleanupExpired reclaims expired records, locking one shard at a time.
func (s *Store) CleanupExpired() int { return s.CleanupExpiredLimit(math.MaxInt) }

// DatasetStats reports live logical bytes, not physical allocator usage.
type DatasetStats struct{ Keys, KeyBytes, ValueBytes uint64 }

func (s *Store) Stats() DatasetStats {
	var result DatasetStats
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		now := s.now()
		for k, e := range sh.all() {
			if !sh.expired(k, e, now) {
				result.Keys++
				result.KeyBytes += uint64(len(k))
				result.ValueBytes += uint64(e.rawLength)
			}
		}
		sh.mu.RUnlock()
	}
	return result
}
