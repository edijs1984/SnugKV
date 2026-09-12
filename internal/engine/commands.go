package engine

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"sync/atomic"
	"time"
)

type SetOptions struct {
	NX, XX bool
	TTL    time.Duration
}

func (s *Store) SetConditional(key string, value []byte, options SetOptions) (bool, error) {
	if len(value) > 32<<20 {
		return false, errors.New("ERR value exceeds 32 MiB limit")
	}
	if options.TTL < 0 || options.NX && options.XX {
		return false, errors.New("ERR invalid SET options")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	old, exists := sh.data.Get(key)
	exists = exists && !old.expired(s.now())
	if options.NX && exists || options.XX && !exists {
		return false, nil
	}
	e := s.makeEntry(value)
	if options.TTL > 0 {
		e.expiresAt = stampOf(s.now().Add(options.TTL))
	}
	if err := s.publish(sh, key, e); err != nil {
		return false, err
	}
	return true, nil
}
func (s *Store) GetSet(key string, value []byte) ([]byte, bool, error) {
	if len(value) > 32<<20 {
		return nil, false, errors.New("ERR value exceeds 32 MiB limit")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	old, exists := sh.data.Get(key)
	exists = exists && !old.expired(s.now())
	var previous []byte
	if exists {
		previous = s.decode(old)
	}
	if err := s.publish(sh, key, s.makeEntry(value)); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}
	return previous, true, nil
}

// lockAll implements atomic cross-shard commands with a consistent lock order.
func (s *Store) lockAll() func() {
	for i := range s.shards {
		s.shards[i].mu.Lock()
	}
	return func() {
		for i := len(s.shards) - 1; i >= 0; i-- {
			s.shards[i].mu.Unlock()
		}
	}
}
func (s *Store) MSet(keys []string, values [][]byte) error {
	if len(keys) != len(values) {
		return errors.New("ERR mismatched key/value count")
	}
	for _, value := range values {
		if len(value) > 32<<20 {
			return errors.New("ERR value exceeds 32 MiB limit")
		}
	}
	unlock := s.lockAll()
	defer unlock()
	replacements := make(map[string]entry)
	for i, k := range keys {
		replacements[k] = s.makeEntry(values[i])
	}
	ordered := make([]string, 0, len(replacements))
	for key := range replacements {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	var before, after, extra, extraArena uint64
	allocations := make(map[*shard][]int)
	growth := make(map[*shard]int)
	for _, k := range ordered {
		e := replacements[k]
		sh := s.shardFor(k)
		if old, ok := sh.data.Get(k); ok {
			before += entryCharge(k, old)
		} else {
			growth[sh]++
		}
		after += entryCharge(k, e)
		allocations[sh] = append(allocations[sh], len(e.value))
	}
	for sh, n := range growth {
		extra += sh.data.GrowthBytes(n)
	}
	for sh, lengths := range allocations {
		extraArena += sh.arena.GrowthFor(lengths)
	}
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	next := s.memory.used - before + after + extra + extraArena
	if s.memory.max > 0 && next > s.memory.max {
		return ErrOOM
	}
	s.memory.used = next
	s.memory.entries = s.memory.entries - before + after
	s.memory.index += extra
	s.memory.arenas += extraArena
	for _, k := range ordered {
		e := replacements[k]
		sh := s.shardFor(k)
		e.ref = sh.arena.Alloc(e.value)
		e.value, _ = sh.arena.View(e.ref)
		replacements[k] = e
	}
	for _, k := range ordered {
		e := replacements[k]
		e.version = atomic.AddUint64(&s.version, 1)
		sh := s.shardFor(k)
		old, exists := sh.data.Get(k)
		if exists && old.schema != nil {
			sh.shapes.ReleaseRecord(old.schema, old.value)
		}
		e.lastWrite = stampOf(s.now())
		e.lastAccess = e.lastWrite
		e.writes = 1
		sh.data.Set(k, e)
		if exists {
			sh.arena.Free(old.ref)
		}
		sh.schedule(k, e.expiresAt)

	}
	return nil
}
func (s *Store) MGet(keys []string) ([][]byte, []bool) {
	unlock := s.lockAll()
	defer unlock()
	now := s.now()
	values, found := make([][]byte, len(keys)), make([]bool, len(keys))
	for i, key := range keys {
		e, ok := s.shardFor(key).data.Get(key)
		if ok && !e.expired(now) {
			values[i] = s.decode(e)
			if now.Sub(e.lastAccess.Time()) > time.Minute {
				e.reads = 0
			}
			e.lastAccess = stampOf(now)
			if e.reads < math.MaxUint32 {
				e.reads++
			}
			s.shardFor(key).data.Set(key, e)
			found[i] = true
		}
	}
	return values, found
}
func (s *Store) Exists(keys []string) int64 {
	unlock := s.lockAll()
	defer unlock()
	now := s.now()
	var count int64
	for _, key := range keys {
		if e, ok := s.shardFor(key).data.Get(key); ok && !e.expired(now) {
			count++
		}
	}
	return count
}
func (s *Store) DeleteMany(keys []string) int64 {
	unlock := s.lockAll()
	defer unlock()
	now := s.now()
	var count int64
	for _, key := range keys {
		sh := s.shardFor(key)
		if e, ok := sh.data.Get(key); ok {
			if !e.expired(now) {
				count++
			}
			s.remove(sh, key)
		}
	}
	return count
}
func (s *Store) Expire(key string, ttl time.Duration) bool {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.data.Get(key)
	now := s.now()
	if !ok || e.expired(now) {
		s.remove(sh, key)
		return false
	}
	if ttl <= 0 {
		s.remove(sh, key)
	} else {
		e.expiresAt = stampOf(now.Add(ttl))
		e.version = atomic.AddUint64(&s.version, 1)
		sh.data.Set(key, e)
		sh.schedule(key, e.expiresAt)
	}
	return true
}
func (s *Store) Persist(key string) bool {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.data.Get(key)
	if !ok || e.expired(s.now()) {
		s.remove(sh, key)
		return false
	}
	if e.expiresAt.IsZero() {
		return false
	}
	e.expiresAt = 0
	e.version = atomic.AddUint64(&s.version, 1)
	sh.data.Set(key, e)
	sh.schedule(key, e.expiresAt)
	return true
}

// Sub avoids negating MinInt64, which cannot be represented as int64.
func (s *Store) Sub(key string, decrement int64) (int64, error) {
	if decrement != math.MinInt64 {
		return s.Add(key, -decrement)
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.data.Get(key)
	if !ok || e.expired(s.now()) {
		return 0, errors.New("ERR increment or decrement would overflow")
	}
	n, err := strconv.ParseInt(string(s.decode(e)), 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != string(s.decode(e)) {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
	if n >= 0 {
		return 0, errors.New("ERR increment or decrement would overflow")
	}
	n -= decrement
	updated := s.makeEntry([]byte(strconv.FormatInt(n, 10)))
	updated.expiresAt = e.expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) Rename(source, destination string, nx bool) (bool, error) {
	if source == destination {
		return false, errors.New("ERR source and destination objects are the same")
	}

	// Lock every shard because source and destination may live on different shards.
	unlock := s.lockAll()
	defer unlock()

	now := s.now()

	sourceShard := s.shardFor(source)
	destinationShard := s.shardFor(destination)

	sourceEntry, sourceExists := sourceShard.data.Get(source)

	if !sourceExists || sourceEntry.expired(now) {
		if sourceExists {
			s.remove(sourceShard, source)
		}

		return false, errors.New("ERR no such key")
	}

	destinationEntry, destinationExists := destinationShard.data.Get(destination)

	if destinationExists && destinationEntry.expired(now) {
		s.remove(destinationShard, destination)
		destinationExists = false
	}

	// RENAMENX must not replace an existing destination.
	if nx && destinationExists {
		return false, nil
	}

	// Decode and republish so arena ownership remains correct.
	value := s.decode(sourceEntry)

	replacement := s.makeEntry(value)

	// RENAME preserves TTL.
	replacement.expiresAt = sourceEntry.expiresAt

	if err := s.publish(destinationShard, destination, replacement); err != nil {
		return false, err
	}

	s.remove(sourceShard, source)

	return true, nil
}
