package engine

// ZSetScanCompat incrementally inspects the score/member ordered logical ZSET.
// Cursor is the next source index to inspect, not an index into the MATCH-filtered
// result. This matters because Redis applies MATCH after doing COUNT-sized work,
// so a call may legitimately return zero elements with a non-zero cursor.
//
// A nil pattern means MATCH was omitted. A non-nil empty pattern is a real empty
// glob and therefore only matches an empty member.
func (s *Store) ZSetScanCompat(key string, cursor uint64, count int, pattern []byte) (uint64, []ZSetItem, error) {
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
	if e.valueType != TypeZSet {
		return 0, nil, zsetWrongType()
	}

	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return 0, nil, err
	}
	if cursor >= uint64(len(items)) {
		return 0, nil, nil
	}

	start := int(cursor)
	end := start + count
	if end > len(items) {
		end = len(items)
	}

	out := make([]ZSetItem, 0, end-start)
	for i := start; i < end; i++ {
		item := items[i]
		if pattern != nil && !redisGlobMatch(pattern, item.Member) {
			continue
		}
		out = append(out, ZSetItem{
			Member: append([]byte(nil), item.Member...),
			Score:  item.Score,
		})
	}

	if end == len(items) {
		return 0, out, nil
	}
	return uint64(end), out, nil
}
