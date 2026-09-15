package engine

import (
	"bytes"
	"sort"
)

// SetMultiContains checks multiple members while decoding the native SET only
// once. Results preserve the caller's input order, matching Redis SMISMEMBER.
func (s *Store) SetMultiContains(key string, targets [][]byte) ([]bool, error) {
	results := make([]bool, len(targets))
	if len(targets) == 0 {
		return results, nil
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return results, nil
	}
	if e.valueType != TypeSet {
		return nil, setWrongType()
	}

	members, err := s.setMembersFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	for i, target := range targets {
		index := sort.Search(len(members), func(j int) bool {
			return bytes.Compare(members[j], target) >= 0
		})
		results[i] = index < len(members) && bytes.Equal(members[index], target)
	}
	return results, nil
}

// SetScan incrementally iterates the sorted logical SET. Cursor is the next
// member index to inspect. COUNT is a work hint: at most count source members
// are inspected, so MATCH may return fewer than count results. A nil pattern
// means MATCH was omitted; a non-nil empty pattern is a real empty glob and only
// matches an empty member.
func (s *Store) SetScan(key string, cursor uint64, count int, pattern []byte) (uint64, [][]byte, error) {
	if count <= 0 {
		count = 10
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return 0, nil, nil
	}
	if e.valueType != TypeSet {
		return 0, nil, setWrongType()
	}

	members, err := s.setMembersFromEntry(sh, e)
	if err != nil {
		return 0, nil, err
	}
	if cursor >= uint64(len(members)) {
		return 0, nil, nil
	}

	start := int(cursor)
	end := start + count
	if end > len(members) {
		end = len(members)
	}

	out := make([][]byte, 0, end-start)
	for i := start; i < end; i++ {
		if pattern != nil && !redisGlobMatch(pattern, members[i]) {
			continue
		}
		out = append(out, append([]byte(nil), members[i]...))
	}

	if end == len(members) {
		return 0, out, nil
	}
	return uint64(end), out, nil
}
