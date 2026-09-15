package engine

import (
	"sort"
	"time"
)

// Scan returns up to count live keys starting at cursor.
// Cursor 0 starts a new scan. Returned cursor 0 means the scan is complete.
//
// This implementation snapshots and sorts keys. MATCH is applied while the
// snapshot is built and COUNT limits the number of returned snapshot entries.
// The public command layer supplies "*" when MATCH is omitted, which lets an
// explicit empty MATCH pattern retain its Redis meaning instead of being treated
// as "no filter".
func (s *Store) Scan(cursor uint64, count int, pattern string) (uint64, []string) {
	if count <= 0 {
		count = 10
	}

	var keys []string
	now := s.now()

	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()

		for key, e := range sh.all() {
			if e.expired(now) {
				continue
			}
			if pattern != "*" && !redisGlobMatch([]byte(pattern), []byte(key)) {
				continue
			}
			keys = append(keys, key)
		}

		sh.mu.RUnlock()
	}

	sort.Strings(keys)

	if cursor >= uint64(len(keys)) {
		return 0, nil
	}

	end := cursor + uint64(count)
	if end >= uint64(len(keys)) {
		end = uint64(len(keys))
		return 0, keys[cursor:end]
	}

	return end, keys[cursor:end]
}

func (s *Store) Keys(pattern string) []string {
	var keys []string
	now := s.now()

	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()

		for key, e := range sh.all() {
			if e.expired(now) {
				continue
			}
			if pattern != "*" && !redisGlobMatch([]byte(pattern), []byte(key)) {
				continue
			}
			keys = append(keys, key)
		}

		sh.mu.RUnlock()
	}

	sort.Strings(keys)
	return keys
}

// globMatch remains as a string adapter for callers that have not yet migrated
// to the shared binary matcher. MATCH itself is byte-oriented.
func globMatch(pattern, value string) bool {
	return redisGlobMatch([]byte(pattern), []byte(value))
}

func (s *Store) RandomKey() (string, bool) {
	keys := s.Keys("*")
	if len(keys) == 0 {
		return "", false
	}

	// Good enough for RANDOMKEY; no cryptographic randomness needed.
	n := time.Now().UnixNano()
	if n < 0 {
		n = -n
	}
	return keys[int(n%int64(len(keys)))], true
}
