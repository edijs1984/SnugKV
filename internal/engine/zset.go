package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

const maxPackedZSetBytes = 32 << 20

var (
	packedZSetHeaderV1       = [...]byte{'S', 'Z', 1} // legacy raw float64 scores
	packedZSetHeaderIntDelta = [...]byte{'S', 'Z', 2} // exact int64 scores, delta-varint encoded
	packedZSetHeaderFloat64  = [...]byte{'S', 'Z', 3} // canonical raw float64 fallback
)

type ZSetItem struct {
	Member []byte
	Score  float64
}

type ZSetAddOptions struct {
	NX, XX, GT, LT, CH, INCR bool
}

type ZSetStats struct {
	Members     int
	MemberBytes int
	PackedBytes int
	StoredBytes int
	Encoding    string
}

func zsetWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func appendZSetUvarint(dst []byte, value uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	return append(dst, buf[:n]...)
}

func readZSetUvarint(data []byte, offset *int) (uint64, error) {
	if *offset >= len(data) {
		return 0, errors.New("invalid packed zset")
	}
	value, n := binary.Uvarint(data[*offset:])
	if n <= 0 {
		return 0, errors.New("invalid packed zset")
	}
	*offset += n
	return value, nil
}

func zsetUvarintLen(value uint64) int {
	var buf [binary.MaxVarintLen64]byte
	return binary.PutUvarint(buf[:], value)
}

func normalizeZSetScore(score float64) float64 {
	if score == 0 {
		return 0
	}
	return score
}

func zsetLess(a, b ZSetItem) bool {
	if a.Score < b.Score {
		return true
	}
	if a.Score > b.Score {
		return false
	}
	return bytes.Compare(a.Member, b.Member) < 0
}

func zsetExactInt64(score float64) (int64, bool) {
	score = normalizeZSetScore(score)
	if math.IsNaN(score) || math.IsInf(score, 0) || math.Trunc(score) != score {
		return 0, false
	}
	// float64(math.MaxInt64) rounds to 2^63, so use an exclusive upper bound.
	if score < -9223372036854775808.0 || score >= 9223372036854775808.0 {
		return 0, false
	}
	n := int64(score)
	if float64(n) != score {
		return 0, false
	}
	return n, true
}

func zsetZigZag(value int64) uint64 {
	return uint64(value)<<1 ^ uint64(value>>63)
}

func zsetUnZigZag(value uint64) int64 {
	return int64(value>>1) ^ -int64(value&1)
}

func zsetIntegerScoreBytes(items []ZSetItem) (int, bool) {
	if len(items) == 0 {
		return 0, false
	}
	var previous int64
	total := 0
	for i, item := range items {
		score, ok := zsetExactInt64(item.Score)
		if !ok {
			return 0, false
		}
		if i == 0 {
			total += zsetUvarintLen(zsetZigZag(score))
		} else {
			if score < previous {
				return 0, false
			}
			delta := uint64(score) - uint64(previous)
			total += zsetUvarintLen(delta)
		}
		previous = score
	}
	return total, total < 8*len(items)
}

func encodePackedZSet(items []ZSetItem) ([]byte, error) {
	for _, item := range items {
		if math.IsNaN(item.Score) {
			return nil, errors.New("ERR resulting score is not a number (NaN)")
		}
	}

	integerScoreBytes, useIntegerDelta := zsetIntegerScoreBytes(items)
	capacity := len(packedZSetHeaderFloat64) + binary.MaxVarintLen64
	if useIntegerDelta {
		capacity += integerScoreBytes
	} else {
		capacity += 8 * len(items)
	}
	for _, item := range items {
		capacity += binary.MaxVarintLen64 + len(item.Member)
		if capacity > maxPackedZSetBytes {
			return nil, errors.New("ERR sorted set exceeds 32 MiB limit")
		}
	}

	out := make([]byte, 0, capacity)
	if useIntegerDelta {
		out = append(out, packedZSetHeaderIntDelta[:]...)
	} else {
		out = append(out, packedZSetHeaderFloat64[:]...)
	}
	out = appendZSetUvarint(out, uint64(len(items)))

	var scoreBytes [8]byte
	var previous int64
	for i, item := range items {
		if useIntegerDelta {
			score, _ := zsetExactInt64(item.Score)
			if i == 0 {
				out = appendZSetUvarint(out, zsetZigZag(score))
			} else {
				out = appendZSetUvarint(out, uint64(score)-uint64(previous))
			}
			previous = score
		} else {
			binary.LittleEndian.PutUint64(scoreBytes[:], math.Float64bits(normalizeZSetScore(item.Score)))
			out = append(out, scoreBytes[:]...)
		}
		out = appendZSetUvarint(out, uint64(len(item.Member)))
		out = append(out, item.Member...)
	}
	if len(out) > maxPackedZSetBytes {
		return nil, errors.New("ERR sorted set exceeds 32 MiB limit")
	}
	return out, nil
}

func decodePackedZSet(data []byte) ([]ZSetItem, error) {
	if len(data) < 3 || data[0] != 'S' || data[1] != 'Z' {
		return nil, errors.New("invalid packed zset")
	}
	version := data[2]
	if version != packedZSetHeaderV1[2] && version != packedZSetHeaderIntDelta[2] && version != packedZSetHeaderFloat64[2] {
		return nil, errors.New("invalid packed zset")
	}
	offset := 3
	count64, err := readZSetUvarint(data, &offset)
	if err != nil || count64 > uint64(maxPackedZSetBytes) {
		return nil, errors.New("invalid packed zset")
	}
	items := make([]ZSetItem, 0, int(count64))
	var previousInt int64
	for i := 0; i < int(count64); i++ {
		var score float64
		switch version {
		case 1, 3:
			if len(data)-offset < 8 {
				return nil, errors.New("invalid packed zset")
			}
			score = math.Float64frombits(binary.LittleEndian.Uint64(data[offset : offset+8]))
			offset += 8
			if math.IsNaN(score) {
				return nil, errors.New("invalid packed zset")
			}
			score = normalizeZSetScore(score)

		case 2:
			encoded, err := readZSetUvarint(data, &offset)
			if err != nil {
				return nil, errors.New("invalid packed zset")
			}
			var scoreInt int64
			if i == 0 {
				scoreInt = zsetUnZigZag(encoded)
			} else {
				scoreInt = int64(uint64(previousInt) + encoded)
				if scoreInt < previousInt {
					return nil, errors.New("invalid packed zset")
				}
			}
			score = float64(scoreInt)
			roundTrip, ok := zsetExactInt64(score)
			if !ok || roundTrip != scoreInt {
				return nil, errors.New("invalid packed zset")
			}
			previousInt = scoreInt
		}

		length64, err := readZSetUvarint(data, &offset)
		if err != nil || length64 > uint64(len(data)-offset) {
			return nil, errors.New("invalid packed zset")
		}
		end := offset + int(length64)
		items = append(items, ZSetItem{Member: append([]byte(nil), data[offset:end]...), Score: score})
		offset = end
	}
	if offset != len(data) {
		return nil, errors.New("invalid packed zset trailing data")
	}
	for i := 1; i < len(items); i++ {
		if !zsetLess(items[i-1], items[i]) {
			return nil, errors.New("invalid packed zset order")
		}
	}
	return items, nil
}

func zsetEncodingName(data []byte) string {
	if len(data) < 3 || data[0] != 'S' || data[1] != 'Z' {
		return "unknown"
	}
	switch data[2] {
	case 1:
		return "packed-v1-float64"
	case 2:
		return "packed-int-delta"
	case 3:
		return "packed-float64"
	default:
		return "unknown"
	}
}

func zsetPreparedEntry(packed []byte) preparedEntry {
	return preparedEntry{entry: entry{valueType: TypeZSet, rawLength: uint32(len(packed))}, data: append([]byte(nil), packed...)}
}

func (s *Store) zsetItemsFromEntry(sh *shard, e entry) ([]ZSetItem, error) {
	return decodePackedZSet(sh.encoded(e))
}

func (s *Store) zsetLogicalValue(sh *shard, e entry) ([]byte, error) {
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	return encodePackedZSet(items)
}

func zsetFindMember(items []ZSetItem, member []byte) int {
	for i := range items {
		if bytes.Equal(items[i].Member, member) {
			return i
		}
	}
	return -1
}

// ZSetAdd applies Redis-style ZADD options. The returned integer is the number
// of newly added members, or the number of changed members when CH is set.
// When INCR is set, incremented is true only if the update was applied.
func (s *Store) ZSetAdd(key string, pairs []ZSetItem, options ZSetAddOptions) (count int64, incremented bool, incrementScore float64, err error) {
	if len(pairs) == 0 {
		return 0, false, 0, errors.New("ERR invalid sorted set member count")
	}
	if options.NX && options.XX || options.GT && options.LT || options.NX && (options.GT || options.LT) {
		return 0, false, 0, errors.New("ERR XX, NX, LT, and GT options at the same time are not compatible")
	}
	if options.INCR && len(pairs) != 1 {
		return 0, false, 0, errors.New("ERR INCR option supports a single increment-element pair")
	}
	for _, pair := range pairs {
		if math.IsNaN(pair.Score) {
			return 0, false, 0, errors.New("ERR resulting score is not a number (NaN)")
		}
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := s.now()
	old, exists := sh.get(key)
	if exists && old.expired(now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}
	var items []ZSetItem
	var expiresAt stamp
	if exists {
		if old.valueType != TypeZSet {
			return 0, false, 0, zsetWrongType()
		}
		items, err = s.zsetItemsFromEntry(sh, old)
		if err != nil {
			return 0, false, 0, err
		}
		expiresAt = old.expiresAt
	}

	changedMembers := make(map[string]struct{})
	var added int64
	for _, pair := range pairs {
		score := normalizeZSetScore(pair.Score)
		idx := zsetFindMember(items, pair.Member)
		if options.INCR && idx >= 0 {
			score = normalizeZSetScore(items[idx].Score + score)
			if math.IsNaN(score) {
				return 0, false, 0, errors.New("ERR resulting score is not a number (NaN)")
			}
		}

		if idx < 0 {
			if options.XX {
				continue
			}
			items = append(items, ZSetItem{Member: append([]byte(nil), pair.Member...), Score: score})
			added++
			changedMembers[string(pair.Member)] = struct{}{}
			if options.INCR {
				incremented, incrementScore = true, score
			}
			continue
		}

		if options.NX {
			continue
		}
		current := items[idx].Score
		if (options.GT && score <= current) || (options.LT && score >= current) {
			continue
		}
		if score != current {
			items[idx].Score = score
			changedMembers[string(pair.Member)] = struct{}{}
		}
		if options.INCR {
			incremented, incrementScore = true, score
		}
	}

	if len(changedMembers) == 0 {
		if options.INCR {
			return 0, false, 0, nil
		}
		if options.CH {
			return 0, false, 0, nil
		}
		return added, false, 0, nil
	}

	sort.Slice(items, func(i, j int) bool { return zsetLess(items[i], items[j]) })
	packed, err := encodePackedZSet(items)
	if err != nil {
		return 0, false, 0, err
	}
	updated := zsetPreparedEntry(packed)
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, false, 0, err
	}
	if options.CH {
		return int64(len(changedMembers)), incremented, incrementScore, nil
	}
	return added, incremented, incrementScore, nil
}

func (s *Store) ZSetRemove(key string, members [][]byte) (int64, error) {
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
	remove := make(map[string]struct{}, len(members))
	for _, member := range members {
		remove[string(member)] = struct{}{}
	}
	kept := make([]ZSetItem, 0, len(items))
	var removed int64
	for _, item := range items {
		if _, ok := remove[string(item.Member)]; ok {
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

func (s *Store) ZSetScore(key string, member []byte) (float64, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return 0, false, nil
	}
	if e.valueType != TypeZSet {
		return 0, false, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return 0, false, err
	}
	idx := zsetFindMember(items, member)
	if idx < 0 {
		return 0, false, nil
	}
	return items[idx].Score, true, nil
}

func (s *Store) ZSetCard(key string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return 0, nil
	}
	if e.valueType != TypeZSet {
		return 0, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	return int64(len(items)), err
}

func (s *Store) ZSetRank(key string, member []byte, reverse bool) (int64, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return 0, false, nil
	}
	if e.valueType != TypeZSet {
		return 0, false, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return 0, false, err
	}
	idx := zsetFindMember(items, member)
	if idx < 0 {
		return 0, false, nil
	}
	if reverse {
		return int64(len(items) - 1 - idx), true, nil
	}
	return int64(idx), true, nil
}

func normalizeZSetRange(length int, start, stop int64) (int, int, bool) {
	n := int64(length)
	if start < 0 {
		start += n
	}
	if stop < 0 {
		stop += n
	}
	if start < 0 {
		start = 0
	}
	if stop < 0 || start >= n || start > stop {
		return 0, 0, false
	}
	if stop >= n {
		stop = n - 1
	}
	return int(start), int(stop), true
}

func (s *Store) ZSetRange(key string, start, stop int64, reverse bool) ([]ZSetItem, error) {
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
	startIndex, stopIndex, ok := normalizeZSetRange(len(items), start, stop)
	if !ok {
		return nil, nil
	}
	out := make([]ZSetItem, 0, stopIndex-startIndex+1)
	if !reverse {
		for i := startIndex; i <= stopIndex; i++ {
			out = append(out, ZSetItem{Member: append([]byte(nil), items[i].Member...), Score: items[i].Score})
		}
		return out, nil
	}
	for rank := startIndex; rank <= stopIndex; rank++ {
		i := len(items) - 1 - rank
		out = append(out, ZSetItem{Member: append([]byte(nil), items[i].Member...), Score: items[i].Score})
	}
	return out, nil
}

func (s *Store) ZSetStorageStats(key string) (ZSetStats, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return ZSetStats{}, false, nil
	}
	if e.valueType != TypeZSet {
		return ZSetStats{}, false, zsetWrongType()
	}
	items, err := s.zsetItemsFromEntry(sh, e)
	if err != nil {
		return ZSetStats{}, false, err
	}
	logical, err := encodePackedZSet(items)
	if err != nil {
		return ZSetStats{}, false, err
	}
	stored := sh.encoded(e)
	stats := ZSetStats{Members: len(items), PackedBytes: len(logical), StoredBytes: len(stored), Encoding: zsetEncodingName(stored)}
	for _, item := range items {
		stats.MemberBytes += len(item.Member)
	}
	return stats, true, nil
}
