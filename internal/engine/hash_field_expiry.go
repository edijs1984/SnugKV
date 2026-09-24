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

// HashFieldExpireAt applies an absolute millisecond expiration to HASH fields.
// Results follow Redis HPEXPIREAT's unconditional result codes:
// -2 missing field/key, 1 expiry set, 2 field deleted because expiry is in the past.
func (s *Store) HashFieldExpireAt(key string, fields [][]byte, whenMS int64) ([]int64, error) {
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
