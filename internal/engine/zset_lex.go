package engine

import (
	"bytes"
	"sort"
)

type ZSetLexBound struct {
	Value     []byte
	Exclusive bool
	Infinite  int // -1 = -inf, 0 = finite, +1 = +inf
}

func zsetLexAtLeast(member []byte, min ZSetLexBound) bool {
	if min.Infinite < 0 {
		return true
	}
	if min.Infinite > 0 {
		return false
	}
	cmp := bytes.Compare(member, min.Value)
	if min.Exclusive {
		return cmp > 0
	}
	return cmp >= 0
}

func zsetLexAtMost(member []byte, max ZSetLexBound) bool {
	if max.Infinite > 0 {
		return true
	}
	if max.Infinite < 0 {
		return false
	}
	cmp := bytes.Compare(member, max.Value)
	if max.Exclusive {
		return cmp < 0
	}
	return cmp <= 0
}

func zsetLexInRange(member []byte, min, max ZSetLexBound) bool {
	return zsetLexAtLeast(member, min) && zsetLexAtMost(member, max)
}

func zsetLexSorted(items []ZSetItem) []ZSetItem {
	out := make([]ZSetItem, len(items))
	for i := range items {
		out[i] = ZSetItem{Member: append([]byte(nil), items[i].Member...), Score: items[i].Score}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].Member, out[j].Member) < 0 })
	return out
}

func (s *Store) zsetLexSnapshot(key string) ([]ZSetItem, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return nil, nil
	}
	if e.valueType != TypeZSet {
		return nil, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	return zsetLexSorted(items), nil
}

func (s *Store) ZSetLexCount(key string, min, max ZSetLexBound) (int64, error) {
	items, err := s.zsetLexSnapshot(key)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, item := range items {
		if zsetLexInRange(item.Member, min, max) {
			count++
		}
	}
	return count, nil
}

func (s *Store) ZSetRangeByLex(key string, min, max ZSetLexBound, reverse bool, offset, count int64) ([]ZSetItem, error) {
	items, err := s.zsetLexSnapshot(key)
	if err != nil {
		return nil, err
	}
	if reverse {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}
	if offset < 0 {
		offset = 0
	}
	out := make([]ZSetItem, 0)
	var skipped int64
	for _, item := range items {
		if !zsetLexInRange(item.Member, min, max) {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		if count == 0 {
			break
		}
		out = append(out, item)
		if count > 0 && int64(len(out)) >= count {
			break
		}
	}
	return out, nil
}

func (s *Store) ZSetRemoveRangeByLex(key string, min, max ZSetLexBound) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
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
		if zsetLexInRange(item.Member, min, max) {
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
	updated.expiresAt = e.expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return removed, nil
}
