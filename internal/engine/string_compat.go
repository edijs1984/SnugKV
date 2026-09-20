package engine

import "time"

// MGetStrings implements Redis MGET string semantics for native container keys:
// a key that exists but is not string-compatible contributes a missing/nil slot
// rather than exposing its packed physical bytes.
func (s *Store) MGetStrings(keys []string) ([][]byte, []bool) {
	unlock := s.lockAll()
	defer unlock()

	now := s.now()
	values := make([][]byte, len(keys))
	found := make([]bool, len(keys))

	for i, key := range keys {
		sh := s.shardFor(key)
		e, ok := sh.get(key)
		if !ok || sh.expired(key, e, now) || isNativeContainerType(e.valueType) {
			continue
		}

		values[i] = s.decode(sh, e)
		if s.shouldTrackActivity(e) && e.entryMeta != nil {
			e.entryMeta.recordRead(now)
			sh.set(key, e)
		}
		found[i] = true
	}

	return values, found
}

// GetDelString implements Redis GETDEL semantics: native container keys are
// treated like non-string values, producing nil without deleting the key.
func (s *Store) GetDelString(key string) ([]byte, bool) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok {
		return nil, false
	}
	if sh.expired(key, e, s.now()) {
		s.remove(sh, key)
		return nil, false
	}
	if isNativeContainerType(e.valueType) {
		return nil, false
	}

	value := s.decode(sh, e)
	s.remove(sh, key)
	return value, true
}
