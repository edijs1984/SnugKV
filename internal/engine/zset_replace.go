package engine

import (
	"errors"
	"math"
	"sort"
)

// ZSetReplace atomically replaces key with the supplied sorted-set items.
// Store-style commands use replacement semantics: an empty result deletes the
// destination and a non-empty result clears any previous TTL.
func (s *Store) ZSetReplace(key string, items []ZSetItem) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if len(items) == 0 {
		s.remove(sh, key)
		return nil
	}

	copyItems := make([]ZSetItem, len(items))
	seen := make(map[string]struct{}, len(items))
	for i, item := range items {
		if math.IsNaN(item.Score) {
			return errors.New("ERR resulting score is not a number (NaN)")
		}
		member := string(item.Member)
		if _, ok := seen[member]; ok {
			return errors.New("ERR duplicate sorted set member")
		}
		seen[member] = struct{}{}
		copyItems[i] = ZSetItem{Member: append([]byte(nil), item.Member...), Score: normalizeZSetScore(item.Score)}
	}

	sort.Slice(copyItems, func(i, j int) bool { return zsetLess(copyItems[i], copyItems[j]) })
	packed, err := encodePackedZSet(copyItems)
	if err != nil {
		return err
	}

	// Deliberately do not copy the old expiration: STORE-style replacement
	// clears the destination TTL, matching Redis sorted-set store semantics.
	return s.publish(sh, key, zsetPreparedEntry(packed))
}
