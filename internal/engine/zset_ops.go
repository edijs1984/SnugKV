package engine

import (
	"errors"
	"math"
	"sort"
)

// ZSetScores returns one score slot per requested member, preserving request order.
// found[i] is false when the member is absent. Missing keys return all-missing slots.
func (s *Store) ZSetScores(key string, members [][]byte) ([]float64, []bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return make([]float64, len(members)), make([]bool, len(members)), nil
	}
	if e.valueType != TypeZSet {
		return nil, nil, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return nil, nil, err
	}
	scores := make([]float64, len(members))
	found := make([]bool, len(members))
	for i, member := range members {
		idx := zsetFindMember(items, member)
		if idx >= 0 {
			scores[i] = items[idx].Score
			found[i] = true
		}
	}
	return scores, found, nil
}

// ZSetPop removes up to count lowest/highest ranked items. A surviving key keeps
// its TTL; removing the final member deletes the key.
func (s *Store) ZSetPop(key string, count int64, max bool) ([]ZSetItem, error) {
	if count < 0 {
		return nil, errors.New("ERR value is out of range, must be positive")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return nil, nil
	}
	if e.valueType != TypeZSet {
		return nil, zsetWrongType()
	}
	if count == 0 {
		return nil, nil
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	want := int64(len(items))
	if count < want {
		want = count
	}
	popped := make([]ZSetItem, 0, int(want))
	var kept []ZSetItem
	if !max {
		for i := int64(0); i < want; i++ {
			item := items[i]
			popped = append(popped, ZSetItem{Member: append([]byte(nil), item.Member...), Score: item.Score})
		}
		kept = items[want:]
	} else {
		for i := int64(0); i < want; i++ {
			item := items[len(items)-1-int(i)]
			popped = append(popped, ZSetItem{Member: append([]byte(nil), item.Member...), Score: item.Score})
		}
		kept = items[:len(items)-int(want)]
	}
	if len(kept) == 0 {
		s.remove(sh, key)
		return popped, nil
	}
	packed, err := encodePackedZSet(kept)
	if err != nil {
		return nil, err
	}
	updated := zsetPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return nil, err
	}
	return popped, nil
}

// ZSetMPop pops from the first non-empty ZSET in keys.
func (s *Store) ZSetMPop(keys []string, count int64, max bool) (string, []ZSetItem, bool, error) {
	if len(keys) == 0 {
		return "", nil, false, errors.New("ERR zmpop requires at least one key")
	}
	if count <= 0 {
		return "", nil, false, errors.New("ERR count should be greater than 0")
	}
	unlock := s.lockAll()
	defer unlock()
	now := s.now()
	for _, key := range keys {
		sh := s.shardFor(key)
		e, ok := sh.get(key)
		if !ok || sh.expired(key, e, now) {
			continue
		}
		if e.valueType != TypeZSet {
			return "", nil, false, zsetWrongType()
		}
		items, err := s.zsetItemsFromEntry(sh, e)
		if err != nil {
			return "", nil, false, err
		}
		if len(items) == 0 {
			continue
		}
		want := count
		if want > int64(len(items)) {
			want = int64(len(items))
		}
		popped := make([]ZSetItem, 0, int(want))
		var kept []ZSetItem
		if !max {
			for i := int64(0); i < want; i++ {
				item := items[i]
				popped = append(popped, ZSetItem{Member: append([]byte(nil), item.Member...), Score: item.Score})
			}
			kept = items[want:]
		} else {
			for i := int64(0); i < want; i++ {
				item := items[len(items)-1-int(i)]
				popped = append(popped, ZSetItem{Member: append([]byte(nil), item.Member...), Score: item.Score})
			}
			kept = items[:len(items)-int(want)]
		}
		if len(kept) == 0 {
			s.remove(sh, key)
			return key, popped, true, nil
		}
		packed, err := encodePackedZSet(kept)
		if err != nil {
			return "", nil, false, err
		}
		updated := zsetPreparedEntry(packed)
		updated.expiresAt = sh.expirationAt(key, e)
		if err := s.publish(sh, key, updated); err != nil {
			return "", nil, false, err
		}
		return key, popped, true, nil
	}
	return "", nil, false, nil
}

// ZSetRandomMembers follows Redis count semantics: positive count is distinct,
// negative count samples with replacement.
func (s *Store) ZSetRandomMembers(key string, count int64) ([]ZSetItem, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, nil
	}
	if e.valueType != TypeZSet {
		return nil, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 || count == 0 {
		return nil, nil
	}
	if count > 0 {
		want := count
		if want > int64(len(items)) {
			want = int64(len(items))
		}
		indexes, err := randomDistinctSetIndexes(len(items), int(want))
		if err != nil {
			return nil, err
		}
		out := make([]ZSetItem, 0, len(indexes))
		for _, index := range indexes {
			item := items[index]
			out = append(out, ZSetItem{Member: append([]byte(nil), item.Member...), Score: item.Score})
		}
		return out, nil
	}
	if count == math.MinInt64 {
		return nil, errors.New("ERR count is too large")
	}
	want := int(-count)
	out := make([]ZSetItem, 0, want)
	for i := 0; i < want; i++ {
		index, err := randomSetIndex(len(items))
		if err != nil {
			return nil, err
		}
		item := items[index]
		out = append(out, ZSetItem{Member: append([]byte(nil), item.Member...), Score: item.Score})
	}
	return out, nil
}

// ZSetScan is a stable snapshot/index cursor implementation. Cursor 0 starts an
// iteration. Returned 0 means complete.
func (s *Store) ZSetScan(key string, cursor uint64, count int, pattern string) (uint64, []ZSetItem, error) {
	if count <= 0 {
		count = 10
	}
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, nil, nil
	}
	if e.valueType != TypeZSet {
		return 0, nil, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return 0, nil, err
	}
	matched := make([]ZSetItem, 0, len(items))
	for _, item := range items {
		if pattern != "" && pattern != "*" && !globMatch(pattern, string(item.Member)) {
			continue
		}
		matched = append(matched, item)
	}
	if cursor >= uint64(len(matched)) {
		return 0, nil, nil
	}
	end := cursor + uint64(count)
	next := end
	if end >= uint64(len(matched)) {
		end = uint64(len(matched))
		next = 0
	}
	out := make([]ZSetItem, 0, int(end-cursor))
	for _, item := range matched[cursor:end] {
		out = append(out, ZSetItem{Member: append([]byte(nil), item.Member...), Score: item.Score})
	}
	return next, out, nil
}

func zsetRankRangeItems(items []ZSetItem, start, stop int64, reverse bool) []ZSetItem {
	first, last, ok := normalizeZSetRange(len(items), start, stop)
	if !ok {
		return nil
	}
	out := make([]ZSetItem, 0, last-first+1)
	if !reverse {
		for i := first; i <= last; i++ {
			out = append(out, items[i])
		}
		return out
	}
	for rank := first; rank <= last; rank++ {
		out = append(out, items[len(items)-1-rank])
	}
	return out
}

func zsetScoreRangeItems(items []ZSetItem, min, max ZSetScoreBound, reverse bool, offset, count int64) []ZSetItem {
	if offset < 0 || count == 0 {
		return nil
	}
	out := make([]ZSetItem, 0)
	var skipped int64
	appendItem := func(item ZSetItem) bool {
		if !zsetScoreInRange(item.Score, min, max) {
			return false
		}
		if skipped < offset {
			skipped++
			return false
		}
		if count >= 0 && int64(len(out)) >= count {
			return true
		}
		out = append(out, item)
		return count >= 0 && int64(len(out)) >= count
	}
	if !reverse {
		for _, item := range items {
			if appendItem(item) { break }
		}
	} else {
		for i := len(items)-1; i >= 0; i-- {
			if appendItem(items[i]) { break }
		}
	}
	return out
}

func zsetLexRangeItems(items []ZSetItem, min, max ZSetLexBound, reverse bool, offset, count int64) []ZSetItem {
	if offset < 0 || count == 0 {
		return nil
	}
	ordered := zsetLexSorted(items)
	if reverse {
		for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		}
	}
	out := make([]ZSetItem, 0)
	var skipped int64
	for _, item := range ordered {
		if !zsetLexInRange(item.Member, min, max) {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		if count >= 0 && int64(len(out)) >= count {
			break
		}
		out = append(out, item)
	}
	return out
}

func (s *Store) zsetRangeStore(destination, source string, selectItems func([]ZSetItem) []ZSetItem) (int64, error) {
	unlock := s.lockAll()
	defer unlock()
	now := s.now()
	sourceShard := s.shardFor(source)
	e, ok := sourceShard.get(source)
	var sourceItems []ZSetItem
	if ok && !sourceShard.expired(source, e, now) {
		if e.valueType != TypeZSet {
			return 0, zsetWrongType()
		}
		var err error
		sourceItems, err = s.zsetItemsFromEntry(sourceShard, e)
		if err != nil {
			return 0, err
		}
	}
	result := selectItems(sourceItems)
	// Canonical ZSET storage is score/member ordered regardless of query order.
	sort.Slice(result, func(i, j int) bool { return zsetLess(result[i], result[j]) })
	destinationShard := s.shardFor(destination)
	if len(result) == 0 {
		s.remove(destinationShard, destination)
		return 0, nil
	}
	packed, err := encodePackedZSet(result)
	if err != nil {
		return 0, err
	}
	updated := zsetPreparedEntry(packed) // overwrite intentionally clears TTL
	if err := s.publish(destinationShard, destination, updated); err != nil {
		return 0, err
	}
	return int64(len(result)), nil
}

func (s *Store) ZSetRangeStoreByRank(destination, source string, start, stop int64, reverse bool) (int64, error) {
	return s.zsetRangeStore(destination, source, func(items []ZSetItem) []ZSetItem {
		return zsetRankRangeItems(items, start, stop, reverse)
	})
}

func (s *Store) ZSetRangeStoreByScore(destination, source string, min, max ZSetScoreBound, reverse bool, offset, count int64) (int64, error) {
	return s.zsetRangeStore(destination, source, func(items []ZSetItem) []ZSetItem {
		return zsetScoreRangeItems(items, min, max, reverse, offset, count)
	})
}

func (s *Store) ZSetRangeStoreByLex(destination, source string, min, max ZSetLexBound, reverse bool, offset, count int64) (int64, error) {
	return s.zsetRangeStore(destination, source, func(items []ZSetItem) []ZSetItem {
		return zsetLexRangeItems(items, min, max, reverse, offset, count)
	})
}
