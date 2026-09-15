package engine

import (
	"errors"
	"math"
	"snugkv/internal/arena"
	"snugkv/internal/codec"
	"snugkv/internal/codec/jsonshape"
	"snugkv/internal/index"
	"strconv"
	"time"
)

type entryMeta struct {
	schema                                           *jsonshape.Schema
	lastRewrite, lastOptimize, lastAccess, lastWrite activityStamp
	reads, writes                                    uint8
}

type entry struct {
	ref       arena.Ref
	expiresAt stamp
	*entryMeta
	rawLength uint32
	codecID   codec.ID
	valueType ValueType
}

type preparedEntry struct {
	entry
	data []byte
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

func (s *Store) shouldTrackActivity(e entry) bool {
	return s.encoding && e.valueType != TypeHash
}

func (sh *shard) encoded(e entry) []byte {
	value, err := sh.arena.View(e.ref)
	if err != nil {
		panic(err)
	}

	return value
}

func (e entry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && stampOf(now) >= e.expiresAt
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
}

func New() *Store                             { s, _ := NewWithShards(256); return s }
func NewWithShards(count int) (*Store, error) { return NewWithOptions(Options{Shards: count}) }
func NewWithOptions(options Options) (*Store, error) {
	count := options.Shards
	if count <= 0 || count&(count-1) != 0 || count > 65536 {
		return nil, errors.New("shards must be a positive power of two at most 65536")
	}
	base := uint64(count) * 512
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
			max:   options.MaxMemory,
		},
	}
	for i := range s.shards {
		s.shards[i].data = index.New[uint32]()
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
	if !ok || e.expired(s.now()) {
		return nil, false
	}
	if s.shouldTrackActivity(e) {
		now := s.now()
		meta := e.ensureMeta()
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
func (s *Store) Delete(key string) bool {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	s.remove(sh, key)
	return ok && !e.expired(s.now())
}
func (s *Store) Incr(key string) (int64, error) { return s.Add(key, 1) }
func (s *Store) Decr(key string) (int64, error) { return s.Add(key, -1) }
func (s *Store) Add(key string, delta int64) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if ok && e.expired(s.now()) {
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
	updated.expiresAt = e.expiresAt
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
	if !ok || e.expired(now) {
		return -2
	}
	if e.expiresAt.IsZero() {
		return -1
	}
	ms := e.expiresAt.Sub(now).Milliseconds()
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
			if !e.expired(now) {
				result.Keys++
				result.KeyBytes += uint64(len(k))
				result.ValueBytes += uint64(e.rawLength)
			}
		}
		sh.mu.RUnlock()
	}
	return result
}
