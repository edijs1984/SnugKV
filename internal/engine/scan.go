package engine

import (
	"sort"
	"strings"
	"time"
)

// Scan returns live keys using Redis-style cursor/MATCH/COUNT behavior.
func (s *Store) Scan(cursor uint64, count int, pattern string) (uint64, []string) {
	return s.ScanTyped(cursor, count, pattern, "")
}

// ScanTyped is the SCAN implementation including the Redis TYPE filter. Cursor
// indexes the unfiltered, sorted live-key snapshot. COUNT controls how many
// source keys are inspected, while MATCH and TYPE are applied afterwards. This
// deliberately permits an empty result with a non-zero cursor for selective
// filters, matching Redis SCAN's work-hint semantics.
func (s *Store) ScanTyped(cursor uint64, count int, pattern, typeFilter string) (uint64, []string) {
	if count <= 0 {
		count = 10
	}

	var keys []string
	now := s.now()

	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		for key, e := range sh.all() {
			if !sh.expired(key, e, now) {
				keys = append(keys, key)
			}
		}
		sh.mu.RUnlock()
	}

	sort.Strings(keys)
	if cursor >= uint64(len(keys)) {
		return 0, nil
	}

	remaining := uint64(len(keys)) - cursor
	work := uint64(count)
	if work > remaining {
		work = remaining
	}
	end := cursor + work

	filterType := strings.ToLower(typeFilter)
	out := make([]string, 0, int(work))
	for _, key := range keys[cursor:end] {
		if pattern != "*" && !redisGlobMatch([]byte(pattern), []byte(key)) {
			continue
		}
		if filterType != "" && strings.ToLower(s.Type(key)) != filterType {
			continue
		}
		out = append(out, key)
	}

	if end == uint64(len(keys)) {
		return 0, out
	}
	return end, out
}

func (s *Store) Keys(pattern string) []string {
	var keys []string
	now := s.now()

	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		for key, e := range sh.all() {
			if sh.expired(key, e, now) {
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

// globMatch remains as a string adapter for legacy internal callers. MATCH
// itself is byte-oriented through redisGlobMatch.
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
