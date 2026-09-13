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
	NX, XX      bool
	Get         bool
	KeepTTL     bool
	TTL         time.Duration
	ExpireAt    time.Time
	HasExpireAt bool
}

func (s *Store) SetConditional(
	key string,
	value []byte,
	options SetOptions,
) (bool, error) {
	applied, _, _, err := s.SetWithOptions(key, value, options)
	return applied, err
}

func (s *Store) SetWithOptions(
	key string,
	value []byte,
	options SetOptions,
) (bool, []byte, bool, error) {
	if len(value) > 32<<20 {
		return false, nil, false, errors.New("ERR value exceeds 32 MiB limit")
	}

	if options.TTL < 0 ||
		options.NX && options.XX ||
		options.KeepTTL && options.TTL > 0 ||
		options.KeepTTL && options.HasExpireAt ||
		options.TTL > 0 && options.HasExpireAt {
		return false, nil, false, errors.New("ERR invalid SET options")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	old, exists := sh.get(key)

	if exists && old.expired(now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}

	var previous []byte
	hadPrevious := false

	if options.Get && exists {
		previous = s.decode(old)
		hadPrevious = true
	}

	if options.NX && exists || options.XX && !exists {
		return false, previous, hadPrevious, nil
	}

	e := s.makeEntry(value)

	switch {
	case options.KeepTTL && exists:
		e.expiresAt = old.expiresAt

	case options.TTL > 0:
		e.expiresAt = stampOf(now.Add(options.TTL))

	case options.HasExpireAt:
		e.expiresAt = stampOf(options.ExpireAt)
	}

	if err := s.publish(sh, key, e); err != nil {
		return false, nil, false, err
	}

	// Absolute expiration in the past means SET succeeds but
	// the resulting key is immediately expired.
	if options.HasExpireAt && !options.ExpireAt.After(now) {
		s.remove(sh, key)
	}

	return true, previous, hadPrevious, nil
}

func (s *Store) GetSet(key string, value []byte) ([]byte, bool, error) {
	if len(value) > 32<<20 {
		return nil, false, errors.New("ERR value exceeds 32 MiB limit")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	old, exists := sh.get(key)
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
	var oldPayload, oldLiveBlocks uint64
	var newPayload uint64

	allocations := make(map[*shard][]int)
	growth := make(map[*shard]int)

	for _, k := range ordered {
		e := replacements[k]
		sh := s.shardFor(k)

		if old, ok := sh.get(k); ok {
			before += entryCharge(k, old)
			oldPayload += uint64(len(old.value))
			oldLiveBlocks += sh.arena.AllocationBytes(old.ref)
		} else {
			growth[sh]++
		}

		after += entryCharge(k, e)
		newPayload += uint64(len(e.value))
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
	var newLiveBlocks uint64

	for _, k := range ordered {
		e := replacements[k]
		sh := s.shardFor(k)

		e.ref = sh.arena.Alloc(e.value)
		e.value, _ = sh.arena.View(e.ref)

		newLiveBlocks += sh.arena.AllocationBytes(e.ref)
		replacements[k] = e
	}

	s.memory.arenaPayload =
		s.memory.arenaPayload - oldPayload + newPayload

	s.memory.arenaLiveBlocks =
		s.memory.arenaLiveBlocks - oldLiveBlocks + newLiveBlocks
	for _, k := range ordered {
		e := replacements[k]
		e.version = atomic.AddUint64(&s.version, 1)
		sh := s.shardFor(k)
		old, exists := sh.get(k)
		if exists && old.schema != nil {
			sh.shapes.ReleaseRecord(old.schema, old.value)
		}
		e.lastWrite = stampOf(s.now())
		e.lastAccess = e.lastWrite
		e.writes = 1
		sh.set(k, e)
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
		e, ok := s.shardFor(key).get(key)
		if ok && !e.expired(now) {
			values[i] = s.decode(e)
			if now.Sub(e.lastAccess.Time()) > time.Minute {
				e.reads = 0
			}
			e.lastAccess = stampOf(now)
			if e.reads < math.MaxUint32 {
				e.reads++
			}
			s.shardFor(key).set(key, e)
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
		if e, ok := s.shardFor(key).get(key); ok && !e.expired(now) {
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
		if e, ok := sh.get(key); ok {
			if !e.expired(now) {
				count++
			}
			s.remove(sh, key)
		}
	}
	return count
}
func (s *Store) Expire(key string, ttl time.Duration) bool {
	return s.ExpireConditional(key, ttl, "")
}

func (s *Store) ExpireConditional(
	key string,
	ttl time.Duration,
	condition string,
) bool {
	return s.expireAtConditional(
		key,
		s.now().Add(ttl),
		condition,
	)
}

func (s *Store) expireAtConditional(
	key string,
	when time.Time,
	condition string,
) bool {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	now := s.now()

	if !ok || e.expired(now) {
		if ok {
			s.remove(sh, key)
		}
		return false
	}

	hasExpiry := !e.expiresAt.IsZero()

	switch condition {
	case "":
	case "NX":
		if hasExpiry {
			return false
		}

	case "XX":
		if !hasExpiry {
			return false
		}

	case "GT":
		// Persistent keys are treated as having infinite TTL.
		if !hasExpiry || !when.After(e.expiresAt.Time()) {
			return false
		}

	case "LT":
		// Persistent keys have infinite TTL, therefore every finite
		// expiration is less than their current expiration.
		if hasExpiry && !when.Before(e.expiresAt.Time()) {
			return false
		}

	default:
		return false
	}

	if !when.After(now) {
		s.remove(sh, key)
		return true
	}

	e.expiresAt = stampOf(when)
	e.version = atomic.AddUint64(&s.version, 1)

	sh.set(key, e)
	sh.schedule(key, e.expiresAt)

	return true
}

func (s *Store) Persist(key string) bool {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		s.remove(sh, key)
		return false
	}
	if e.expiresAt.IsZero() {
		return false
	}
	e.expiresAt = 0
	e.version = atomic.AddUint64(&s.version, 1)
	sh.set(key, e)
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
	e, ok := sh.get(key)
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

	sourceEntry, sourceExists := sourceShard.get(source)

	if !sourceExists || sourceEntry.expired(now) {
		if sourceExists {
			s.remove(sourceShard, source)
		}

		return false, errors.New("ERR no such key")
	}

	destinationEntry, destinationExists := destinationShard.get(destination)

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

func (s *Store) AddFloat(key string, increment float64) (string, error) {
	if math.IsNaN(increment) || math.IsInf(increment, 0) {
		return "", errors.New("ERR value is not a valid float")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	e, exists := sh.get(key)

	current := float64(0)

	if exists {
		if e.expired(now) {
			s.remove(sh, key)
			exists = false
		} else {
			raw := string(s.decode(e))

			n, err := strconv.ParseFloat(raw, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return "", errors.New("ERR value is not a valid float")
			}

			current = n
		}
	}

	result := current + increment

	if math.IsNaN(result) || math.IsInf(result, 0) {
		return "", errors.New("ERR increment would produce NaN or Infinity")
	}

	// Avoid storing "-0".
	if result == 0 {
		result = 0
	}

	formatted := strconv.FormatFloat(result, 'f', -1, 64)

	updated := s.makeEntry([]byte(formatted))

	if exists {
		// INCRBYFLOAT preserves the existing TTL.
		updated.expiresAt = e.expiresAt
	}

	if err := s.publish(sh, key, updated); err != nil {
		return "", err
	}

	return formatted, nil
}

func (s *Store) Touch(keys []string) int {
	count := 0
	now := s.now()

	for _, key := range keys {
		sh := s.shardFor(key)

		sh.mu.Lock()

		e, ok := sh.get(key)

		if !ok {
			sh.mu.Unlock()
			continue
		}

		if e.expired(now) {
			s.remove(sh, key)
			sh.mu.Unlock()
			continue
		}

		count++

		sh.mu.Unlock()
	}

	return count
}
func (s *Store) ExpireAt(key string, when time.Time) bool {
	return s.ExpireAtConditional(key, when, "")
}

func (s *Store) ExpireAtConditional(
	key string,
	when time.Time,
	condition string,
) bool {
	return s.expireAtConditional(key, when, condition)
}

func (s *Store) ExpireTime(key string, milliseconds bool) int64 {
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

	if milliseconds {
		return e.expiresAt.Time().UnixMilli()
	}

	return e.expiresAt.Time().Unix()
}

func (s *Store) GetDel(key string) ([]byte, bool) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	now := s.now()

	if !ok || e.expired(now) {
		if ok {
			s.remove(sh, key)
		}
		return nil, false
	}

	value := s.decode(e)

	s.remove(sh, key)

	return value, true
}
func (s *Store) GetEx(
	key string,
	expireAt *time.Time,
	persist bool,
) ([]byte, bool) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	now := s.now()

	if !ok || e.expired(now) {
		if ok {
			s.remove(sh, key)
		}
		return nil, false
	}

	value := s.decode(e)

	if persist {
		e.expiresAt = 0
		e.version = atomic.AddUint64(&s.version, 1)

		sh.set(key, e)
		sh.schedule(key, e.expiresAt)

		return value, true
	}

	if expireAt != nil {
		// Return the old value but delete the key if the requested
		// expiration time is already in the past.
		if !expireAt.After(now) {
			s.remove(sh, key)
			return value, true
		}

		e.expiresAt = stampOf(*expireAt)
		e.version = atomic.AddUint64(&s.version, 1)

		sh.set(key, e)
		sh.schedule(key, e.expiresAt)
	}

	return value, true
}

func (s *Store) Append(key string, suffix []byte) (int, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	e, exists := sh.get(key)

	var current []byte
	var expiresAt stamp

	if exists {
		if e.expired(now) {
			s.remove(sh, key)
			exists = false
		} else {
			current = s.decode(e)
			expiresAt = e.expiresAt
		}
	}

	if len(current)+len(suffix) > 32<<20 {
		return 0, errors.New("ERR value exceeds 32 MiB limit")
	}

	value := make([]byte, 0, len(current)+len(suffix))
	value = append(value, current...)
	value = append(value, suffix...)

	updated := s.makeEntry(value)
	updated.expiresAt = expiresAt

	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}

	return len(value), nil
}

func (s *Store) GetRange(key string, start, end int64) []byte {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)

	if !ok || e.expired(s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return []byte{}
	}

	value := s.decode(e)
	length := int64(len(value))

	if length == 0 {
		return []byte{}
	}

	if start < 0 {
		start = length + start
	}

	if end < 0 {
		end = length + end
	}

	if start < 0 {
		start = 0
	}

	if end < 0 || start >= length || start > end {
		return []byte{}
	}

	if end >= length {
		end = length - 1
	}

	return append([]byte(nil), value[start:end+1]...)
}
func (s *Store) SetRange(key string, offset int64, replacement []byte) (int, error) {
	if offset < 0 {
		return 0, errors.New("ERR offset is out of range")
	}

	if offset > 32<<20 {
		return 0, errors.New("ERR string exceeds maximum allowed size")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	e, exists := sh.get(key)

	var current []byte
	var expiresAt stamp

	if exists {
		if e.expired(now) {
			s.remove(sh, key)
			exists = false
		} else {
			current = s.decode(e)
			expiresAt = e.expiresAt
		}
	}

	required := offset + int64(len(replacement))

	if required > 32<<20 {
		return 0, errors.New("ERR string exceeds maximum allowed size")
	}

	if len(replacement) == 0 {
		return len(current), nil
	}

	size := len(current)

	if int(required) > size {
		size = int(required)
	}

	value := make([]byte, size)
	copy(value, current)
	copy(value[int(offset):], replacement)

	updated := s.makeEntry(value)
	updated.expiresAt = expiresAt

	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}

	return len(value), nil
}
