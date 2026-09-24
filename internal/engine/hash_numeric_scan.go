package engine

import (
	"bytes"
	"errors"
	"math"
	"sort"
	"strconv"
)

// HashIncrBy atomically increments an integer HASH field. Missing fields start
// at zero and existing key TTL is preserved.
func (s *Store) HashIncrBy(key string, field []byte, increment int64) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	old, exists := sh.get(key)
	if exists && sh.expired(key, old, now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}

	var pairs []HashPair
	var expiresAt stamp
	if exists {
		if old.valueType != TypeHash {
			return 0, hashWrongType()
		}
		var err error
		pairs, err = decodePackedHash(s.decode(sh, old))
		if err != nil {
			return 0, err
		}
		pairs = liveHashPairs(pairs, now.UnixMilli())
		expiresAt = sh.expirationAt(key, old)
	}

	index := sort.Search(len(pairs), func(i int) bool {
		return bytes.Compare(pairs[i].Field, field) >= 0
	})

	current := int64(0)
	found := index < len(pairs) && bytes.Equal(pairs[index].Field, field)
	if found {
		parsed, err := strconv.ParseInt(string(pairs[index].Value), 10, 64)
		if err != nil {
			return 0, errors.New("ERR hash value is not an integer")
		}
		current = parsed
	}

	if increment > 0 && current > math.MaxInt64-increment ||
		increment < 0 && current < math.MinInt64-increment {
		return 0, errors.New("ERR increment or decrement would overflow")
	}

	result := current + increment
	value := []byte(strconv.FormatInt(result, 10))

	if found {
		pairs[index].Value = value
		pairs[index].ExpiresAtMS = 0
	} else {
		pairs = append(pairs, HashPair{})
		copy(pairs[index+1:], pairs[index:])
		pairs[index] = HashPair{
			Field: append([]byte(nil), field...),
			Value: value,
		}
	}

	packed, err := encodePackedHash(pairs)
	if err != nil {
		return 0, err
	}
	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}

	return result, nil
}

// HashIncrByFloat atomically increments a floating-point HASH field. Missing
// fields start at zero and existing key TTL is preserved.
func (s *Store) HashIncrByFloat(key string, field []byte, increment float64) (string, error) {
	if math.IsNaN(increment) || math.IsInf(increment, 0) {
		return "", errors.New("ERR value is not a valid float")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	old, exists := sh.get(key)
	if exists && sh.expired(key, old, now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}

	var pairs []HashPair
	var expiresAt stamp
	if exists {
		if old.valueType != TypeHash {
			return "", hashWrongType()
		}
		var err error
		pairs, err = decodePackedHash(s.decode(sh, old))
		if err != nil {
			return "", err
		}
		pairs = liveHashPairs(pairs, now.UnixMilli())
		expiresAt = sh.expirationAt(key, old)
	}

	index := sort.Search(len(pairs), func(i int) bool {
		return bytes.Compare(pairs[i].Field, field) >= 0
	})

	current := float64(0)
	found := index < len(pairs) && bytes.Equal(pairs[index].Field, field)
	if found {
		parsed, err := strconv.ParseFloat(string(pairs[index].Value), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return "", errors.New("ERR hash value is not a float")
		}
		current = parsed
	}

	result := current + increment
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return "", errors.New("ERR increment would produce NaN or Infinity")
	}
	if result == 0 {
		result = 0
	}
	formatted := strconv.FormatFloat(result, 'f', -1, 64)
	value := []byte(formatted)

	if found {
		pairs[index].Value = value
		pairs[index].ExpiresAtMS = 0
	} else {
		pairs = append(pairs, HashPair{})
		copy(pairs[index+1:], pairs[index:])
		pairs[index] = HashPair{
			Field: append([]byte(nil), field...),
			Value: value,
		}
	}

	packed, err := encodePackedHash(pairs)
	if err != nil {
		return "", err
	}
	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return "", err
	}

	return formatted, nil
}

// HashScan incrementally iterates sorted HASH fields. Cursor is the next field
// index to inspect. COUNT is a work hint: at most count source fields are
// inspected on a call, so MATCH may return fewer than count results. A nil
// pattern means MATCH was omitted; a non-nil empty pattern is a real empty glob.
func (s *Store) HashScan(key string, cursor uint64, count int, pattern []byte) (uint64, []HashPair, error) {
	if count <= 0 {
		count = 10
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, nil, nil
	}
	if e.valueType != TypeHash {
		return 0, nil, hashWrongType()
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return 0, nil, err
	}
	pairs = liveHashPairs(pairs, s.now().UnixMilli())
	if cursor >= uint64(len(pairs)) {
		return 0, nil, nil
	}

	start := int(cursor)
	end := start + count
	if end > len(pairs) {
		end = len(pairs)
	}

	out := make([]HashPair, 0, end-start)
	for i := start; i < end; i++ {
		if pattern != nil && !redisGlobMatch(pattern, pairs[i].Field) {
			continue
		}
		out = append(out, HashPair{
			Field: append([]byte(nil), pairs[i].Field...),
			Value: append([]byte(nil), pairs[i].Value...),
		})
	}

	if end == len(pairs) {
		return 0, out, nil
	}
	return uint64(end), out, nil
}
