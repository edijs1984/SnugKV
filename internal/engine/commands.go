package engine

import (
	"errors"
	"math"
	"math/bits"
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
		previous = s.decode(sh, old)
		hadPrevious = true
	}

	if options.NX && exists || options.XX && !exists {
		return false, previous, hadPrevious, nil
	}

	e := s.makeEntryForShard(sh, value)

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

	// Real engine writes train JSON shape admission while the shard
	// lock is already held.
	s.observeJSONShapeLocked(sh, value)

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
		previous = s.decode(sh, old)
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
	replacements := make(map[string]preparedEntry)
	for i, k := range keys {
		replacements[k] = s.makeEntry(values[i])
	}
	ordered := make([]string, 0, len(replacements))
	for key := range replacements {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	var before, after, extra, extraEntries, extraArena uint64
	var oldPayload, oldLiveBlocks uint64
	var newPayload uint64

	allocations := make(map[*shard][]int)
	growth := make(map[*shard]int)

	for _, k := range ordered {
		e := replacements[k]
		sh := s.shardFor(k)

		if old, ok := sh.get(k); ok {
			before += entryCharge(k, old)
			oldPayload += uint64(len(sh.encoded(old)))
			oldLiveBlocks += sh.arena.AllocationBytes(old.ref)
		} else {
			growth[sh]++
		}

		after += entryCharge(k, e)
		newPayload += uint64(len(e.data))
		allocations[sh] = append(allocations[sh], len(e.data))
	}
	for sh, n := range growth {
		extra += sh.data.GrowthBytes(n)
		extraEntries += sh.entryGrowthBytes(n)
	}
	for sh, lengths := range allocations {
		extraArena += sh.arena.GrowthFor(lengths)
	}
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	next := s.memory.used - before + after + extra + extraEntries + extraArena
	if s.memory.max > 0 && next > s.memory.max {
		return ErrOOM
	}
	s.memory.used = next
	s.memory.entries =
		s.memory.entries - before + after + extraEntries
	s.memory.index += extra
	s.memory.arenas += extraArena
	var newLiveBlocks uint64

	for _, k := range ordered {
		e := replacements[k]
		sh := s.shardFor(k)

		e.ref = sh.arena.Alloc(e.data)

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
			sh.shapes.ReleaseRecord(old.schema, sh.encoded(old))
		}
		e.lastWrite = activityStampOf(s.now())
		e.lastAccess = e.lastWrite
		e.writes = 1
		sh.set(k, e.entry)
		if exists {
			sh.arena.Free(old.ref)
		}
		sh.schedule(k, e.expiresAt)

	}
	return nil
}

func (s *Store) MSetNX(keys []string, values [][]byte) (bool, error) {
	if len(keys) != len(values) {
		return false, errors.New("ERR mismatched key/value count")
	}

	for _, value := range values {
		if len(value) > 32<<20 {
			return false, errors.New("ERR value exceeds 32 MiB limit")
		}
	}

	unlock := s.lockAll()
	defer unlock()

	now := s.now()

	// Redis MSETNX is all-or-nothing. Check the entire target key set
	// before allocating or publishing anything.
	seen := make(map[string]struct{}, len(keys))

	for _, key := range keys {
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}

		sh := s.shardFor(key)

		if old, ok := sh.get(key); ok {
			if old.expired(now) {
				// Expired keys are logically absent.
				s.remove(sh, key)
				continue
			}

			return false, nil
		}
	}

	// Duplicate keys use the final supplied value, matching MSET behavior.
	replacements := make(map[string]preparedEntry, len(keys))

	for i, key := range keys {
		replacements[key] = s.makeEntry(values[i])
	}

	ordered := make([]string, 0, len(replacements))

	for key := range replacements {
		ordered = append(ordered, key)
	}

	sort.Strings(ordered)

	var entryBytes uint64
	var extraIndex uint64
	var extraEntries uint64
	var extraArena uint64
	var newPayload uint64

	growth := make(map[*shard]int)
	allocations := make(map[*shard][]int)

	for _, key := range ordered {
		e := replacements[key]
		sh := s.shardFor(key)

		entryBytes += entryCharge(key, e)
		newPayload += uint64(len(e.data))

		growth[sh]++
		allocations[sh] = append(allocations[sh], len(e.data))
	}

	for sh, n := range growth {
		extraIndex += sh.data.GrowthBytes(n)
		extraEntries += sh.entryGrowthBytes(n)
	}

	for sh, lengths := range allocations {
		extraArena += sh.arena.GrowthFor(lengths)
	}

	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()

	next := s.memory.used +
		entryBytes +
		extraIndex +
		extraEntries +
		extraArena

	if s.memory.max > 0 && next > s.memory.max {
		return false, ErrOOM
	}

	s.memory.used = next
	s.memory.entries += entryBytes + extraEntries
	s.memory.index += extraIndex
	s.memory.arenas += extraArena

	var newLiveBlocks uint64

	for _, key := range ordered {
		e := replacements[key]
		sh := s.shardFor(key)

		e.ref = sh.arena.Alloc(e.data)

		newLiveBlocks += sh.arena.AllocationBytes(e.ref)
		replacements[key] = e
	}

	s.memory.arenaPayload += newPayload
	s.memory.arenaLiveBlocks += newLiveBlocks

	for _, key := range ordered {
		e := replacements[key]
		sh := s.shardFor(key)

		e.version = atomic.AddUint64(&s.version, 1)
		e.lastWrite = activityStampOf(now)
		e.lastAccess = e.lastWrite
		e.writes = 1

		sh.set(key, e.entry)
		sh.schedule(key, e.expiresAt)
	}

	return true, nil
}

func (s *Store) MGet(keys []string) ([][]byte, []bool) {
	unlock := s.lockAll()
	defer unlock()

	now := s.now()
	values := make([][]byte, len(keys))
	found := make([]bool, len(keys))

	for i, key := range keys {
		sh := s.shardFor(key)
		e, ok := sh.get(key)

		if !ok || e.expired(now) {
			continue
		}

		values[i] = s.decode(sh, e)

		if now.Sub(e.lastAccess.Time()) > time.Minute {
			e.reads = 0
		}

		e.lastAccess = activityStampOf(now)

		if e.reads < ^uint8(0) {
			e.reads++
		}

		sh.set(key, e)
		found[i] = true
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
	n, err := strconv.ParseInt(string(s.decode(sh, e)), 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != string(s.decode(sh, e)) {
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
	value := s.decode(sourceShard, sourceEntry)

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
			raw := string(s.decode(sh, e))

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

const maxStringBytes = 32 << 20
const maxBitOffset = int64(maxStringBytes*8 - 1)

func (s *Store) GetBit(key string, offset int64) (int64, error) {
	if offset < 0 || offset > maxBitOffset {
		return 0, errors.New("ERR bit offset is not an integer or out of range")
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)

	if !ok || e.expired(s.now()) {
		return 0, nil
	}

	value := s.decode(sh, e)

	byteIndex := offset / 8
	if byteIndex >= int64(len(value)) {
		return 0, nil
	}

	bitIndex := uint(7 - (offset % 8))
	mask := byte(1 << bitIndex)

	if value[byteIndex]&mask != 0 {
		return 1, nil
	}

	return 0, nil
}

func (s *Store) SetBit(
	key string,
	offset int64,
	bitValue int,
) (int64, error) {
	if offset < 0 || offset > maxBitOffset {
		return 0, errors.New("ERR bit offset is not an integer or out of range")
	}

	if bitValue != 0 && bitValue != 1 {
		return 0, errors.New("ERR bit is not an integer or out of range")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	old, exists := sh.get(key)

	var current []byte
	var expiresAt stamp

	if exists {
		if old.expired(now) {
			s.remove(sh, key)
			exists = false
		} else {
			current = s.decode(sh, old)
			expiresAt = old.expiresAt
		}
	}

	byteIndex := int(offset / 8)
	required := byteIndex + 1

	updatedValue := make([]byte, required)

	if len(current) > 0 {
		if len(current) > required {
			updatedValue = make([]byte, len(current))
		}

		copy(updatedValue, current)
	}

	bitIndex := uint(7 - (offset % 8))
	mask := byte(1 << bitIndex)

	oldBit := int64(0)

	if updatedValue[byteIndex]&mask != 0 {
		oldBit = 1
	}

	if bitValue == 1 {
		updatedValue[byteIndex] |= mask
	} else {
		updatedValue[byteIndex] &^= mask
	}

	updated := s.makeEntry(updatedValue)

	// SETBIT modifies the existing value and preserves TTL.
	if exists {
		updated.expiresAt = expiresAt
	}

	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}

	return oldBit, nil
}

func normalizeBitRange(
	length int64,
	start int64,
	end int64,
) (int64, int64, bool) {
	if length <= 0 {
		return 0, 0, false
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

	if end < 0 || start >= length {
		return 0, 0, false
	}

	if end >= length {
		end = length - 1
	}

	if start > end {
		return 0, 0, false
	}

	return start, end, true
}

func countBitsInBitRange(
	value []byte,
	startBit int64,
	endBit int64,
) int64 {
	if len(value) == 0 {
		return 0
	}

	firstByte := startBit / 8
	lastByte := endBit / 8

	var count int64

	if firstByte == lastByte {
		for bit := startBit; bit <= endBit; bit++ {
			byteIndex := bit / 8
			bitIndex := uint(7 - (bit % 8))

			if value[byteIndex]&(1<<bitIndex) != 0 {
				count++
			}
		}

		return count
	}

	firstByteEnd := firstByte*8 + 7

	for bit := startBit; bit <= firstByteEnd; bit++ {
		bitIndex := uint(7 - (bit % 8))

		if value[firstByte]&(1<<bitIndex) != 0 {
			count++
		}
	}

	for byteIndex := firstByte + 1; byteIndex < lastByte; byteIndex++ {
		count += int64(bits.OnesCount8(value[byteIndex]))
	}

	lastByteStart := lastByte * 8

	for bit := lastByteStart; bit <= endBit; bit++ {
		bitIndex := uint(7 - (bit % 8))

		if value[lastByte]&(1<<bitIndex) != 0 {
			count++
		}
	}

	return count
}

func (s *Store) BitCount(
	key string,
	start *int64,
	end *int64,
	bitMode bool,
) int64 {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)

	if !ok || e.expired(s.now()) {
		return 0
	}

	value := s.decode(sh, e)

	if len(value) == 0 {
		return 0
	}

	if start == nil || end == nil {
		var total int64

		for _, b := range value {
			total += int64(bits.OnesCount8(b))
		}

		return total
	}

	if bitMode {
		lengthBits := int64(len(value)) * 8

		rangeStart, rangeEnd, ok :=
			normalizeBitRange(lengthBits, *start, *end)

		if !ok {
			return 0
		}

		return countBitsInBitRange(
			value,
			rangeStart,
			rangeEnd,
		)
	}

	rangeStart, rangeEnd, ok :=
		normalizeBitRange(
			int64(len(value)),
			*start,
			*end,
		)

	if !ok {
		return 0
	}

	var total int64

	for i := rangeStart; i <= rangeEnd; i++ {
		total += int64(bits.OnesCount8(value[i]))
	}

	return total
}

func (s *Store) BitOp(
	op string,
	destination string,
	sourceKeys []string,
) (int, error) {
	if len(sourceKeys) == 0 {
		return 0, errors.New("ERR syntax error")
	}

	if op == "NOT" && len(sourceKeys) != 1 {
		return 0, errors.New("ERR BITOP NOT must be called with a single source key")
	}

	unlock := s.lockAll()
	defer unlock()

	now := s.now()

	sources := make([][]byte, len(sourceKeys))
	maxLen := 0

	// Read every source before touching destination because destination
	// is allowed to also be one of the source keys.
	for i, key := range sourceKeys {
		sh := s.shardFor(key)
		e, ok := sh.get(key)

		if !ok {
			continue
		}

		if e.expired(now) {
			s.remove(sh, key)
			continue
		}

		value := s.decode(sh, e)
		sources[i] = value

		if len(value) > maxLen {
			maxLen = len(value)
		}
	}

	destShard := s.shardFor(destination)

	// When every source is empty/non-existent, Redis leaves no
	// destination string and returns zero.
	if maxLen == 0 {
		if old, ok := destShard.get(destination); ok {
			s.remove(destShard, destination)
			_ = old
		}

		return 0, nil
	}

	result := make([]byte, maxLen)

	byteAt := func(src []byte, index int) byte {
		if index >= len(src) {
			return 0
		}

		return src[index]
	}

	switch op {
	case "AND":
		for i := 0; i < maxLen; i++ {
			v := byte(0xff)

			for _, src := range sources {
				v &= byteAt(src, i)
			}

			result[i] = v
		}

	case "OR":
		for i := 0; i < maxLen; i++ {
			var v byte

			for _, src := range sources {
				v |= byteAt(src, i)
			}

			result[i] = v
		}

	case "XOR":
		for i := 0; i < maxLen; i++ {
			var v byte

			for _, src := range sources {
				v ^= byteAt(src, i)
			}

			result[i] = v
		}

	case "NOT":
		src := sources[0]

		// NOT result length is the source length.
		result = make([]byte, len(src))

		for i := range src {
			result[i] = ^src[i]
		}

	case "DIFF":
		for i := 0; i < maxLen; i++ {
			x := byteAt(sources[0], i)
			var others byte

			for _, src := range sources[1:] {
				others |= byteAt(src, i)
			}

			result[i] = x &^ others
		}

	case "DIFF1":
		for i := 0; i < maxLen; i++ {
			x := byteAt(sources[0], i)
			var others byte

			for _, src := range sources[1:] {
				others |= byteAt(src, i)
			}

			result[i] = others &^ x
		}

	case "ANDOR":
		for i := 0; i < maxLen; i++ {
			x := byteAt(sources[0], i)
			var others byte

			for _, src := range sources[1:] {
				others |= byteAt(src, i)
			}

			result[i] = x & others
		}

	case "ONE":
		// A result bit is 1 iff exactly one source contains that bit.
		for i := 0; i < maxLen; i++ {
			var out byte

			for bit := uint(0); bit < 8; bit++ {
				mask := byte(1 << bit)
				count := 0

				for _, src := range sources {
					if byteAt(src, i)&mask != 0 {
						count++

						if count > 1 {
							break
						}
					}
				}

				if count == 1 {
					out |= mask
				}
			}

			result[i] = out
		}

	default:
		return 0, errors.New("ERR syntax error")
	}

	if len(result) == 0 {
		s.remove(destShard, destination)
		return 0, nil
	}

	if len(result) > maxStringBytes {
		return 0, errors.New("ERR value exceeds 32 MiB limit")
	}

	// BITOP replaces destination and therefore clears any old TTL.
	if err := s.publish(
		destShard,
		destination,
		s.makeEntry(result),
	); err != nil {
		return 0, err
	}

	return len(result), nil
}

func bitValueAt(value []byte, position int64) int {
	byteIndex := position / 8
	bitIndex := uint(7 - (position % 8))

	if value[byteIndex]&(1<<bitIndex) != 0 {
		return 1
	}

	return 0
}

func resolveRangeStart(length, start int64) int64 {
	if start < 0 {
		start = length + start
	}

	if start < 0 {
		start = 0
	}

	return start
}

func (s *Store) BitPos(
	key string,
	target int,
	start *int64,
	end *int64,
	bitMode bool,
) (int64, error) {
	if target != 0 && target != 1 {
		return 0, errors.New("ERR bit must be 0 or 1")
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)

	var value []byte

	if ok && !e.expired(s.now()) {
		value = s.decode(sh, e)
	}

	lengthBits := int64(len(value)) * 8

	// No explicit range.
	if start == nil {
		for position := int64(0); position < lengthBits; position++ {
			if bitValueAt(value, position) == target {
				return position, nil
			}
		}

		if target == 0 {
			// Redis treats the right side as zero padded when no
			// explicit range is supplied.
			return lengthBits, nil
		}

		return -1, nil
	}

	var unitLength int64

	if bitMode {
		unitLength = lengthBits
	} else {
		unitLength = int64(len(value))
	}

	startUnit := resolveRangeStart(unitLength, *start)

	// start-only also uses implicit zero padding when searching for zero.
	if end == nil {
		if startUnit >= unitLength {
			if target == 0 {
				if bitMode {
					return startUnit, nil
				}

				return startUnit * 8, nil
			}

			return -1, nil
		}

		var startBit int64

		if bitMode {
			startBit = startUnit
		} else {
			startBit = startUnit * 8
		}

		for position := startBit; position < lengthBits; position++ {
			if bitValueAt(value, position) == target {
				return position, nil
			}
		}

		if target == 0 {
			return lengthBits, nil
		}

		return -1, nil
	}

	// Explicit start+end is a bounded search. No implicit right-side
	// zero padding is considered.
	endUnit := *end

	if endUnit < 0 {
		endUnit = unitLength + endUnit
	}

	if endUnit < 0 || startUnit >= unitLength {
		return -1, nil
	}

	if endUnit >= unitLength {
		endUnit = unitLength - 1
	}

	if startUnit > endUnit {
		return -1, nil
	}

	var startBit, endBit int64

	if bitMode {
		startBit = startUnit
		endBit = endUnit
	} else {
		startBit = startUnit * 8
		endBit = endUnit*8 + 7
	}

	for position := startBit; position <= endBit; position++ {
		if bitValueAt(value, position) == target {
			return position, nil
		}
	}

	return -1, nil
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

	value := s.decode(sh, e)

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

	value := s.decode(sh, e)

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
			current = s.decode(sh, e)
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

	value := s.decode(sh, e)
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
			current = s.decode(sh, e)
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
