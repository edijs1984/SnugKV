package engine

import (
	"bytes"
	"errors"
	"sort"
)

// HashSetNX sets field only when it does not already exist. It returns true
// when the field was inserted. The operation is atomic under the owning shard
// lock and preserves the key TTL.
func (s *Store) HashSetNX(key string, field, value []byte) (bool, error) {
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
			return false, hashWrongType()
		}
		var err error
		pairs, err = decodePackedHash(s.decode(sh, old))
		if err != nil {
			return false, err
		}
		pairs = liveHashPairs(pairs, now.UnixMilli())
		expiresAt = sh.expirationAt(key, old)
	}

	index := sort.Search(len(pairs), func(i int) bool {
		return bytes.Compare(pairs[i].Field, field) >= 0
	})
	if index < len(pairs) && bytes.Equal(pairs[index].Field, field) {
		return false, nil
	}

	pairs = append(pairs, HashPair{})
	copy(pairs[index+1:], pairs[index:])
	pairs[index] = HashPair{
		Field: append([]byte(nil), field...),
		Value: append([]byte(nil), value...),
	}

	packed, err := encodePackedHash(pairs)
	if err != nil {
		return false, err
	}
	if len(packed) > maxPackedHashBytes {
		return false, errors.New("ERR hash exceeds 32 MiB limit")
	}

	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return false, err
	}

	return true, nil
}
