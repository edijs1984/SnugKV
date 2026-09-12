package engine

import "sort"
	


// Scan returns up to count live keys starting at cursor.
// Cursor 0 starts a new scan. Returned cursor 0 means the scan is complete.
//
// This first implementation snapshots and sorts keys. It is simple and stable.
// Later we can replace it with direct shard/index cursor traversal.
func (s *Store) Scan(cursor uint64, count int, pattern string) (uint64, []string) {
	if count <= 0 {
		count = 10
	}

	var keys []string
	now := s.now()

	for i := range s.shards {
		sh := &s.shards[i]

		sh.mu.RLock()

		for key, e := range sh.data.All() {
			if e.expired(now) {
				continue
			}

			if pattern != "" && pattern != "*" && !globMatch(pattern, key) {
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

		for key, e := range sh.data.All() {
			if e.expired(now) {
				continue
			}

			if pattern != "*" && !globMatch(pattern, key) {
				continue
			}

			keys = append(keys, key)
		}

		sh.mu.RUnlock()
	}

	sort.Strings(keys)

	return keys
}

// globMatch implements the most useful Redis MATCH behaviour:
// * = any number of characters
// ? = exactly one character
//
// We can add [] character classes later.
func globMatch(pattern, value string) bool {
	p := []rune(pattern)
	v := []rune(value)

	dp := make([][]bool, len(p)+1)

	for i := range dp {
		dp[i] = make([]bool, len(v)+1)
	}

	dp[0][0] = true

	for i := 1; i <= len(p); i++ {
		if p[i-1] == '*' {
			dp[i][0] = dp[i-1][0]
		}
	}

	for i := 1; i <= len(p); i++ {
		for j := 1; j <= len(v); j++ {
			switch p[i-1] {
			case '*':
				dp[i][j] = dp[i-1][j] || dp[i][j-1]

			case '?':
				dp[i][j] = dp[i-1][j-1]

			default:
				dp[i][j] =
					dp[i-1][j-1] &&
					p[i-1] == v[j-1]
						
			}
		}
	}

	return dp[len(p)][len(v)]
}