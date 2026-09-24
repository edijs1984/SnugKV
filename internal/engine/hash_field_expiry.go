package engine

import (
	"bytes"
	"errors"
	"sort"
)

func liveHashPairs(pairs []HashPair, nowMS int64) []HashPair {
	out := pairs[:0]
	for _, pair := range pairs {
		if pair.ExpiresAtMS != 0 && pair.ExpiresAtMS <= nowMS {
			continue
		}
		out = append(out, pair)
	}
	return out
}

// HashFieldExpireAt applies an unconditional absolute millisecond expiration to HASH fields.
func (s *Store) HashFieldExpireAt(key string, fields [][]byte, whenMS int64) ([]int64, error) {
	return s.HashFieldExpireAtCondition(key, fields, whenMS, "")
}

// HashFieldExpireAtCondition applies an absolute millisecond expiration with
// Redis-compatible NX/XX/GT/LT semantics.
// Results: -2 missing field/key, 0 condition not met, 1 expiry set, 2 field deleted.
func (s *Store) HashFieldExpireAtCondition(key string, fields [][]byte, whenMS int64, condition string) ([]int64, error) {
	if len(fields) == 0 {
		return nil, errors.New("ERR no hash fields")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		if ok {
			s.remove(sh, key)
		}
		out := make([]int64, len(fields))
		for i := range out {
			out[i] = -2
		}
		return out, nil
	}
	if e.valueType != TypeHash {
		return nil, hashWrongType()
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	pairs = liveHashPairs(pairs, now.UnixMilli())

	results := make([]int64, len(fields))
	changed := false
	remove := make(map[string]struct{})
	for i, field := range fields {
		idx := sort.Search(len(pairs), func(j int) bool {
			return bytes.Compare(pairs[j].Field, field) >= 0
		})
		if idx >= len(pairs) || !bytes.Equal(pairs[idx].Field, field) {
			results[i] = -2
			continue
		}
		current := pairs[idx].ExpiresAtMS
		switch condition {
		case "":
		case "NX":
			if current != 0 {
				results[i] = 0
				continue
			}
		case "XX":
			if current == 0 {
				results[i] = 0
				continue
			}
		case "GT":
			if current == 0 || current >= whenMS {
				results[i] = 0
				continue
			}
		case "LT":
			if current != 0 && current <= whenMS {
				results[i] = 0
				continue
			}
		default:
			return nil, errors.New("ERR invalid hash field expiry condition")
		}

		changed = true
		if whenMS <= now.UnixMilli() {
			results[i] = 2
			remove[string(field)] = struct{}{}
			continue
		}
		results[i] = 1
		pairs[idx].ExpiresAtMS = whenMS
	}

	if !changed {
		return results, nil
	}
	if len(remove) != 0 {
		kept := pairs[:0]
		for _, pair := range pairs {
			if _, drop := remove[string(pair.Field)]; drop {
				continue
			}
			kept = append(kept, pair)
		}
		pairs = kept
	}
	if len(pairs) == 0 {
		s.remove(sh, key)
		return results, nil
	}

	packed, err := encodePackedHash(pairs)
	if err != nil {
		return nil, err
	}
	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return nil, err
	}
	return results, nil
}

// HashFieldPTTL returns Redis-style per-field TTLs in milliseconds:
// -2 for a missing/expired field, -1 for a field without expiry, otherwise remaining TTL.
func (s *Store) HashFieldPTTL(key string, fields [][]byte) ([]int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		out := make([]int64, len(fields))
		for i := range out {
			out[i] = -2
		}
		return out, nil
	}
	if e.valueType != TypeHash {
		return nil, hashWrongType()
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	nowMS := now.UnixMilli()
	out := make([]int64, len(fields))
	for i, field := range fields {
		idx := sort.Search(len(pairs), func(j int) bool {
			return bytes.Compare(pairs[j].Field, field) >= 0
		})
		if idx >= len(pairs) || !bytes.Equal(pairs[idx].Field, field) ||
			(pairs[idx].ExpiresAtMS != 0 && pairs[idx].ExpiresAtMS <= nowMS) {
			out[i] = -2
			continue
		}
		if pairs[idx].ExpiresAtMS == 0 {
			out[i] = -1
			continue
		}
		out[i] = pairs[idx].ExpiresAtMS - nowMS
	}
	return out, nil
}


// HashFieldExpireTime returns absolute millisecond field expiration times:
// -2 missing/expired field, -1 no field TTL, otherwise Unix time in milliseconds.
func (s *Store) HashFieldExpireTime(key string, fields [][]byte) ([]int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		out := make([]int64, len(fields))
		for i := range out {
			out[i] = -2
		}
		return out, nil
	}
	if e.valueType != TypeHash {
		return nil, hashWrongType()
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	nowMS := now.UnixMilli()
	out := make([]int64, len(fields))
	for i, field := range fields {
		idx := sort.Search(len(pairs), func(j int) bool {
			return bytes.Compare(pairs[j].Field, field) >= 0
		})
		if idx >= len(pairs) || !bytes.Equal(pairs[idx].Field, field) ||
			(pairs[idx].ExpiresAtMS != 0 && pairs[idx].ExpiresAtMS <= nowMS) {
			out[i] = -2
			continue
		}
		if pairs[idx].ExpiresAtMS == 0 {
			out[i] = -1
			continue
		}
		out[i] = pairs[idx].ExpiresAtMS
	}
	return out, nil
}

// HashFieldPersist removes expiration from HASH fields.
// Results: -2 missing/expired field, -1 field exists without TTL, 1 TTL removed.
func (s *Store) HashFieldPersist(key string, fields [][]byte) ([]int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		if ok {
			s.remove(sh, key)
		}
		out := make([]int64, len(fields))
		for i := range out {
			out[i] = -2
		}
		return out, nil
	}
	if e.valueType != TypeHash {
		return nil, hashWrongType()
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	pairs = liveHashPairs(pairs, now.UnixMilli())

	out := make([]int64, len(fields))
	changed := false
	for i, field := range fields {
		idx := sort.Search(len(pairs), func(j int) bool {
			return bytes.Compare(pairs[j].Field, field) >= 0
		})
		if idx >= len(pairs) || !bytes.Equal(pairs[idx].Field, field) {
			out[i] = -2
			continue
		}
		if pairs[idx].ExpiresAtMS == 0 {
			out[i] = -1
			continue
		}
		pairs[idx].ExpiresAtMS = 0
		out[i] = 1
		changed = true
	}
	if !changed {
		return out, nil
	}

	packed, err := encodePackedHash(pairs)
	if err != nil {
		return nil, err
	}
	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return nil, err
	}
	return out, nil
}
