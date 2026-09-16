package engine

type ZSetScoreBound struct {
	Score     float64
	Exclusive bool
}

func zsetScoreInRange(score float64, min, max ZSetScoreBound) bool {
	if min.Exclusive {
		if score <= min.Score {
			return false
		}
	} else if score < min.Score {
		return false
	}
	if max.Exclusive {
		if score >= max.Score {
			return false
		}
	} else if score > max.Score {
		return false
	}
	return true
}

func (s *Store) ZSetCount(key string, min, max ZSetScoreBound) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, nil
	}
	if e.valueType != TypeZSet {
		return 0, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, item := range items {
		if zsetScoreInRange(item.Score, min, max) {
			count++
		}
	}
	return count, nil
}

// ZSetRangeByScore returns score-ordered items within [min,max]. A negative
// count means unlimited. A negative offset yields an empty result, matching
// Redis LIMIT behavior.
func (s *Store) ZSetRangeByScore(key string, min, max ZSetScoreBound, reverse bool, offset, count int64) ([]ZSetItem, error) {
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
	if offset < 0 || count == 0 {
		return nil, nil
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return nil, err
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
		out = append(out, ZSetItem{Member: append([]byte(nil), item.Member...), Score: item.Score})
		return count >= 0 && int64(len(out)) >= count
	}
	if !reverse {
		for i := range items {
			if appendItem(items[i]) {
				break
			}
		}
		return out, nil
	}
	for i := len(items) - 1; i >= 0; i-- {
		if appendItem(items[i]) {
			break
		}
	}
	return out, nil
}

func (s *Store) ZSetRemoveRangeByRank(key string, start, stop int64) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return 0, nil
	}
	if e.valueType != TypeZSet {
		return 0, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	first, last, ok := normalizeZSetRange(len(items), start, stop)
	if !ok {
		return 0, nil
	}
	removed := int64(last - first + 1)
	if removed == int64(len(items)) {
		s.remove(sh, key)
		return removed, nil
	}
	kept := make([]ZSetItem, 0, len(items)-int(removed))
	kept = append(kept, items[:first]...)
	kept = append(kept, items[last+1:]...)
	packed, err := encodePackedZSet(kept)
	if err != nil {
		return 0, err
	}
	updated := zsetPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return removed, nil
}

func (s *Store) ZSetRemoveRangeByScore(key string, min, max ZSetScoreBound) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return 0, nil
	}
	if e.valueType != TypeZSet {
		return 0, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	kept := make([]ZSetItem, 0, len(items))
	var removed int64
	for _, item := range items {
		if zsetScoreInRange(item.Score, min, max) {
			removed++
			continue
		}
		kept = append(kept, item)
	}
	if removed == 0 {
		return 0, nil
	}
	if len(kept) == 0 {
		s.remove(sh, key)
		return removed, nil
	}
	packed, err := encodePackedZSet(kept)
	if err != nil {
		return 0, err
	}
	updated := zsetPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return removed, nil
}
