package engine

import (
	"bytes"
	"errors"
	"math"
	"sort"
)

type ZSetAggregate uint8

const (
	ZSetAggregateSum ZSetAggregate = iota
	ZSetAggregateMin
	ZSetAggregateMax
	ZSetAggregateCount
)

type zsetAlgebraOperation uint8

const (
	zsetUnionOperation zsetAlgebraOperation = iota
	zsetIntersectOperation
	zsetDiffOperation
)

type zsetSource map[string]float64

func (s *Store) ZSetUnion(keys []string, weights []float64, aggregate ZSetAggregate) ([]ZSetItem, error) {
	return s.zsetAlgebra(keys, weights, aggregate, zsetUnionOperation)
}

func (s *Store) ZSetIntersect(keys []string, weights []float64, aggregate ZSetAggregate) ([]ZSetItem, error) {
	return s.zsetAlgebra(keys, weights, aggregate, zsetIntersectOperation)
}

func (s *Store) ZSetDiff(keys []string) ([]ZSetItem, error) {
	return s.zsetAlgebra(keys, nil, ZSetAggregateSum, zsetDiffOperation)
}

func (s *Store) ZSetUnionStore(destination string, keys []string, weights []float64, aggregate ZSetAggregate) (int64, error) {
	return s.zsetAlgebraStore(destination, keys, weights, aggregate, zsetUnionOperation)
}

func (s *Store) ZSetIntersectStore(destination string, keys []string, weights []float64, aggregate ZSetAggregate) (int64, error) {
	return s.zsetAlgebraStore(destination, keys, weights, aggregate, zsetIntersectOperation)
}

func (s *Store) ZSetDiffStore(destination string, keys []string) (int64, error) {
	return s.zsetAlgebraStore(destination, keys, nil, ZSetAggregateSum, zsetDiffOperation)
}

func (s *Store) ZSetIntersectCardinality(keys []string, limit int64) (int64, error) {
	if len(keys) == 0 {
		return 0, errors.New("ERR sorted set operation requires at least one key")
	}
	if limit < 0 {
		return 0, errors.New("ERR LIMIT can't be negative")
	}
	unlock := s.lockAll()
	defer unlock()
	sources, err := s.zsetSourcesLocked(keys)
	if err != nil {
		return 0, err
	}
	if len(sources) == 0 || len(sources[0]) == 0 {
		return 0, nil
	}
	var count int64
	for member := range sources[0] {
		present := true
		for i := 1; i < len(sources); i++ {
			if _, ok := sources[i][member]; !ok {
				present = false
				break
			}
		}
		if present {
			count++
			if limit > 0 && count >= limit {
				return limit, nil
			}
		}
	}
	return count, nil
}

func (s *Store) zsetAlgebra(keys []string, weights []float64, aggregate ZSetAggregate, operation zsetAlgebraOperation) ([]ZSetItem, error) {
	if len(keys) == 0 {
		return nil, errors.New("ERR sorted set operation requires at least one key")
	}
	weights, err := normalizeZSetWeights(len(keys), weights)
	if err != nil {
		return nil, err
	}
	unlock := s.lockAll()
	defer unlock()
	sources, err := s.zsetSourcesLocked(keys)
	if err != nil {
		return nil, err
	}
	return applyZSetAlgebra(sources, weights, aggregate, operation), nil
}

func (s *Store) zsetAlgebraStore(destination string, keys []string, weights []float64, aggregate ZSetAggregate, operation zsetAlgebraOperation) (int64, error) {
	if len(keys) == 0 {
		return 0, errors.New("ERR sorted set operation requires at least one key")
	}
	weights, err := normalizeZSetWeights(len(keys), weights)
	if err != nil {
		return 0, err
	}
	unlock := s.lockAll()
	defer unlock()

	// Snapshot all sources before replacing destination so destination may also
	// be one of the input keys.
	sources, err := s.zsetSourcesLocked(keys)
	if err != nil {
		return 0, err
	}
	result := applyZSetAlgebra(sources, weights, aggregate, operation)
	sh := s.shardFor(destination)
	if len(result) == 0 {
		s.remove(sh, destination)
		return 0, nil
	}
	packed, err := encodePackedZSet(result)
	if err != nil {
		return 0, err
	}
	updated := zsetPreparedEntry(packed)
	// STORE variants overwrite the destination and therefore clear its TTL.
	if err := s.publish(sh, destination, updated); err != nil {
		return 0, err
	}
	return int64(len(result)), nil
}

func normalizeZSetWeights(count int, weights []float64) ([]float64, error) {
	if len(weights) == 0 {
		out := make([]float64, count)
		for i := range out {
			out[i] = 1
		}
		return out, nil
	}
	if len(weights) != count {
		return nil, errors.New("ERR weight value is not a float")
	}
	out := append([]float64(nil), weights...)
	for _, weight := range out {
		if math.IsNaN(weight) {
			return nil, errors.New("ERR weight value is not a float")
		}
	}
	return out, nil
}

func (s *Store) zsetSourcesLocked(keys []string) ([]zsetSource, error) {
	now := s.now()
	sources := make([]zsetSource, len(keys))
	for i, key := range keys {
		sh := s.shardFor(key)
		e, ok := sh.get(key)
		if !ok || e.expired(now) {
			sources[i] = zsetSource{}
			continue
		}
		source := zsetSource{}
		switch e.valueType {
		case TypeZSet:
			items, err := s.zsetItemsFromEntry(sh, e)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				source[string(item.Member)] = item.Score
			}
		case TypeSet:
			members, err := s.setMembersFromEntry(sh, e)
			if err != nil {
				return nil, err
			}
			for _, member := range members {
				source[string(member)] = 1
			}
		default:
			return nil, zsetWrongType()
		}
		sources[i] = source
	}
	return sources, nil
}

func applyZSetAlgebra(sources []zsetSource, weights []float64, aggregate ZSetAggregate, operation zsetAlgebraOperation) []ZSetItem {
	if len(sources) == 0 {
		return nil
	}
	result := make(map[string]float64)

	switch operation {
	case zsetUnionOperation:
		for i, source := range sources {
			for member, score := range source {
				weighted := zsetWeightedScore(score, weights[i], aggregate)
				if current, ok := result[member]; ok {
					result[member] = zsetAggregateScores(current, weighted, aggregate)
				} else {
					result[member] = weighted
				}
			}
		}

	case zsetIntersectOperation:
		for member, score := range sources[0] {
			value := zsetWeightedScore(score, weights[0], aggregate)
			present := true
			for i := 1; i < len(sources); i++ {
				next, ok := sources[i][member]
				if !ok {
					present = false
					break
				}
				value = zsetAggregateScores(value, zsetWeightedScore(next, weights[i], aggregate), aggregate)
			}
			if present {
				result[member] = value
			}
		}

	case zsetDiffOperation:
		for member, score := range sources[0] {
			presentElsewhere := false
			for i := 1; i < len(sources); i++ {
				if _, ok := sources[i][member]; ok {
					presentElsewhere = true
					break
				}
			}
			if !presentElsewhere {
				result[member] = score
			}
		}
	}

	items := make([]ZSetItem, 0, len(result))
	for member, score := range result {
		items = append(items, ZSetItem{Member: []byte(member), Score: zsetSafeScore(score)})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			return bytes.Compare(items[i].Member, items[j].Member) < 0
		}
		return items[i].Score < items[j].Score
	})
	return items
}

func zsetWeightedScore(score, weight float64, aggregate ZSetAggregate) float64 {
	if aggregate == ZSetAggregateCount {
		return zsetSafeScore(weight)
	}
	return zsetSafeScore(score * weight)
}

func zsetAggregateScores(left, right float64, aggregate ZSetAggregate) float64 {
	switch aggregate {
	case ZSetAggregateMin:
		return math.Min(left, right)
	case ZSetAggregateMax:
		return math.Max(left, right)
	default: // SUM and COUNT both sum their contributions.
		return zsetSafeScore(left + right)
	}
}

func zsetSafeScore(score float64) float64 {
	// Redis normalizes NaN produced by algebraic combinations such as +inf +
	// -inf or 0*inf instead of allowing NaN into a sorted set.
	if math.IsNaN(score) {
		return 0
	}
	if score == 0 {
		return 0
	}
	return score
}
