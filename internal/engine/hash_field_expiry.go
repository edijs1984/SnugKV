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

	if e.isHotHash() {
		var freezeErr error
		e, freezeErr = s.freezeHotHashAndReloadLocked(sh, key, e)
		if freezeErr != nil {
			return nil, freezeErr
		}
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
	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return nil, errors.New("HOT hash sidecar invariant")
		}
		out := make([]int64, len(fields))
		for i, field := range fields {
			if _, found := h.get(field); found {
				out[i] = -1
			} else {
				out[i] = -2
			}
		}
		return out, nil
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
	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return nil, errors.New("HOT hash sidecar invariant")
		}
		out := make([]int64, len(fields))
		for i, field := range fields {
			if _, found := h.get(field); found {
				out[i] = -1
			} else {
				out[i] = -2
			}
		}
		return out, nil
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

	if e.isHotHash() {
		var freezeErr error
		e, freezeErr = s.freezeHotHashAndReloadLocked(sh, key, e)
		if freezeErr != nil {
			return nil, freezeErr
		}
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


type HashGetExOptions struct {
	ExpireAtMS *int64
	Persist    bool
}

type HashSetExOptions struct {
	Condition  string
	ExpireAtMS *int64
	KeepTTL    bool
}

// HashGetDel returns requested field values in request order and deletes each
// successfully returned field immediately. Duplicate fields therefore return
// the value once and nil on subsequent occurrences, matching Redis HGETDEL.
func (s *Store) HashGetDel(key string, fields [][]byte) ([][]byte, []bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	values := make([][]byte, len(fields))
	found := make([]bool, len(fields))

	now := s.now()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		if ok {
			s.remove(sh, key)
		}
		return values, found, nil
	}
	if e.valueType != TypeHash {
		return nil, nil, hashWrongType()
	}

	if e.isHotHash() {
		var freezeErr error
		e, freezeErr = s.freezeHotHashAndReloadLocked(sh, key, e)
		if freezeErr != nil {
			return nil, nil, freezeErr
		}
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return nil, nil, err
	}
	pairs = liveHashPairs(pairs, now.UnixMilli())
	expiresAt := sh.expirationAt(key, e)

	changed := false
	for i, field := range fields {
		idx := sort.Search(len(pairs), func(j int) bool {
			return bytes.Compare(pairs[j].Field, field) >= 0
		})
		if idx >= len(pairs) || !bytes.Equal(pairs[idx].Field, field) {
			continue
		}

		values[i] = append([]byte(nil), pairs[idx].Value...)
		found[i] = true
		copy(pairs[idx:], pairs[idx+1:])
		pairs = pairs[:len(pairs)-1]
		changed = true
	}

	if !changed {
		return values, found, nil
	}
	if len(pairs) == 0 {
		s.remove(sh, key)
		return values, found, nil
	}

	packed, err := encodePackedHash(pairs)
	if err != nil {
		return nil, nil, err
	}
	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return nil, nil, err
	}
	return values, found, nil
}

// HashGetEx returns requested values and optionally mutates each returned
// field's TTL. Duplicate fields are processed sequentially, as Redis does.
func (s *Store) HashGetEx(key string, fields [][]byte, options HashGetExOptions) ([][]byte, []bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	values := make([][]byte, len(fields))
	found := make([]bool, len(fields))

	now := s.now()
	nowMS := now.UnixMilli()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		if ok {
			s.remove(sh, key)
		}
		return values, found, nil
	}
	if e.valueType != TypeHash {
		return nil, nil, hashWrongType()
	}

	if e.isHotHash() {
		var freezeErr error
		e, freezeErr = s.freezeHotHashAndReloadLocked(sh, key, e)
		if freezeErr != nil {
			return nil, nil, freezeErr
		}
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return nil, nil, err
	}
	pairs = liveHashPairs(pairs, nowMS)
	keyExpiresAt := sh.expirationAt(key, e)
	changed := false

	for i, field := range fields {
		idx := sort.Search(len(pairs), func(j int) bool {
			return bytes.Compare(pairs[j].Field, field) >= 0
		})
		if idx >= len(pairs) || !bytes.Equal(pairs[idx].Field, field) {
			continue
		}

		values[i] = append([]byte(nil), pairs[idx].Value...)
		found[i] = true

		if options.Persist {
			if pairs[idx].ExpiresAtMS != 0 {
				pairs[idx].ExpiresAtMS = 0
				changed = true
			}
			continue
		}
		if options.ExpireAtMS == nil {
			continue
		}

		whenMS := *options.ExpireAtMS
		changed = true
		if whenMS <= nowMS {
			copy(pairs[idx:], pairs[idx+1:])
			pairs = pairs[:len(pairs)-1]
			continue
		}
		pairs[idx].ExpiresAtMS = whenMS
	}

	if !changed {
		return values, found, nil
	}
	if len(pairs) == 0 {
		s.remove(sh, key)
		return values, found, nil
	}

	packed, err := encodePackedHash(pairs)
	if err != nil {
		return nil, nil, err
	}
	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = keyExpiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return nil, nil, err
	}
	return values, found, nil
}

// HashSetEx atomically evaluates FNX/FXX against the pre-command live hash,
// then updates all field/value pairs with Redis HSETEX TTL semantics.
func (s *Store) HashSetEx(key string, fields, values [][]byte, options HashSetExOptions) (bool, error) {
	if len(fields) == 0 || len(fields) != len(values) {
		return false, errors.New("ERR invalid hash field/value count")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	nowMS := now.UnixMilli()
	e, exists := sh.get(key)
	if exists && sh.expired(key, e, now) {
		s.remove(sh, key)
		exists = false
		e = entry{}
	}

	var pairs []HashPair
	var keyExpiresAt stamp
	if exists {
		if e.valueType != TypeHash {
			return false, hashWrongType()
		}
		if e.isHotHash() {
			var freezeErr error
			e, freezeErr = s.freezeHotHashAndReloadLocked(sh, key, e)
			if freezeErr != nil {
				return false, freezeErr
			}
		}
		var err error
		pairs, err = decodePackedHash(s.decode(sh, e))
		if err != nil {
			return false, err
		}
		pairs = liveHashPairs(pairs, nowMS)
		keyExpiresAt = sh.expirationAt(key, e)
	}

	if options.Condition != "" {
		foundCount := 0
		for _, field := range fields {
			idx := sort.Search(len(pairs), func(j int) bool {
				return bytes.Compare(pairs[j].Field, field) >= 0
			})
			if idx < len(pairs) && bytes.Equal(pairs[idx].Field, field) {
				foundCount++
			}
		}
		switch options.Condition {
		case "FXX":
			if foundCount != len(fields) {
				return false, nil
			}
		case "FNX":
			if foundCount != 0 {
				return false, nil
			}
		default:
			return false, errors.New("ERR invalid HSETEX condition")
		}
	}

	for i, field := range fields {
		idx := sort.Search(len(pairs), func(j int) bool {
			return bytes.Compare(pairs[j].Field, field) >= 0
		})

		var ttl int64
		if idx < len(pairs) && bytes.Equal(pairs[idx].Field, field) {
			if options.KeepTTL {
				ttl = pairs[idx].ExpiresAtMS
			}
			pairs[idx].Value = append([]byte(nil), values[i]...)
		} else {
			pair := HashPair{
				Field: append([]byte(nil), field...),
				Value: append([]byte(nil), values[i]...),
			}
			pairs = append(pairs, HashPair{})
			copy(pairs[idx+1:], pairs[idx:])
			pairs[idx] = pair
		}

		if options.ExpireAtMS != nil {
			ttl = *options.ExpireAtMS
		}
		pairs[idx].ExpiresAtMS = ttl
	}

	if options.ExpireAtMS != nil && *options.ExpireAtMS <= nowMS {
		pairs = liveHashPairs(pairs, nowMS)
	}
	if len(pairs) == 0 {
		if exists {
			s.remove(sh, key)
		}
		return true, nil
	}

	packed, err := encodePackedHash(pairs)
	if err != nil {
		return false, err
	}
	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = keyExpiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return false, err
	}
	return true, nil
}
