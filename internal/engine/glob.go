package engine

// redisGlobMatch matches Redis-style byte glob patterns. It is intentionally
// byte-oriented: keys, hash fields, set members, and sorted-set members are
// binary-safe and Redis MATCH semantics operate on bytes rather than Unicode
// code points.
//
// Supported syntax:
//   *      zero or more bytes
//   ?      exactly one byte
//   [abc]  character class
//   [a-z]  character range
//   [^x]   negated class (also accepts [!x])
//   \\x     escape the next byte
func redisGlobMatch(pattern, value []byte) bool {
	var match func(pi, vi int) bool
	match = func(pi, vi int) bool {
		for pi < len(pattern) {
			switch pattern[pi] {
			case '*':
				for pi < len(pattern) && pattern[pi] == '*' {
					pi++
				}
				if pi == len(pattern) {
					return true
				}
				for k := vi; k <= len(value); k++ {
					if match(pi, k) {
						return true
					}
				}
				return false

			case '?':
				if vi >= len(value) {
					return false
				}
				pi++
				vi++

			case '\\':
				pi++
				if pi >= len(pattern) {
					if vi >= len(value) || value[vi] != '\\' {
						return false
					}
					vi++
					continue
				}
				if vi >= len(value) || value[vi] != pattern[pi] {
					return false
				}
				pi++
				vi++

			case '[':
				if vi >= len(value) {
					return false
				}
				classStart := pi
				pi++
				negate := false
				if pi < len(pattern) && (pattern[pi] == '^' || pattern[pi] == '!') {
					negate = true
					pi++
				}
				matched := false
				closed := false
				for pi < len(pattern) {
					if pattern[pi] == ']' {
						closed = true
						pi++
						break
					}

					lo := pattern[pi]
					if lo == '\\' && pi+1 < len(pattern) {
						pi++
						lo = pattern[pi]
					}
					pi++

					if pi+1 < len(pattern) && pattern[pi] == '-' && pattern[pi+1] != ']' {
						pi++
						hi := pattern[pi]
						if hi == '\\' && pi+1 < len(pattern) {
							pi++
							hi = pattern[pi]
						}
						pi++
						if lo <= hi && lo <= value[vi] && value[vi] <= hi {
							matched = true
						}
					} else if value[vi] == lo {
						matched = true
					}
				}

				if !closed {
					// Redis treats an unterminated class as a literal '['.
					pi = classStart + 1
					if value[vi] != '[' {
						return false
					}
					vi++
					continue
				}
				if matched == negate {
					return false
				}
				vi++

			default:
				if vi >= len(value) || value[vi] != pattern[pi] {
					return false
				}
				pi++
				vi++
			}
		}
		return vi == len(value)
	}

	return match(0, 0)
}
