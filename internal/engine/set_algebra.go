package engine

import (
	"bytes"
	"errors"
)

type setAlgebraOperation uint8

const (
	setUnionOperation setAlgebraOperation = iota
	setIntersectOperation
	setDiffOperation
)

// SetUnion returns the sorted union of the supplied SET keys. Missing keys are
// treated as empty sets. The operation is evaluated under a consistent all-shard
// snapshot so concurrent writes cannot produce a mixed result.
func (s *Store) SetUnion(keys []string) ([][]byte, error) {
	return s.setAlgebra(keys, setUnionOperation)
}

// SetIntersect returns the sorted intersection of the supplied SET keys.
// Missing keys are treated as empty sets.
func (s *Store) SetIntersect(keys []string) ([][]byte, error) {
	return s.setAlgebra(keys, setIntersectOperation)
}

// SetDiff returns members of the first SET that do not occur in any subsequent
// SET. Missing keys are treated as empty sets.
func (s *Store) SetDiff(keys []string) ([][]byte, error) {
	return s.setAlgebra(keys, setDiffOperation)
}

func (s *Store) setAlgebra(keys []string, operation setAlgebraOperation) ([][]byte, error) {
	if len(keys) == 0 {
		return nil, errors.New("ERR set operation requires at least one key")
	}
	unlock := s.lockAll()
	defer unlock()
	sets, err := s.setSourcesLocked(keys)
	if err != nil {
		return nil, err
	}
	return applySetAlgebra(sets, operation), nil
}

// SetUnionStore atomically replaces destination with the union result and
// returns its cardinality. The replacement is a fresh value, so an existing
// destination TTL is cleared.
func (s *Store) SetUnionStore(destination string, keys []string) (int64, error) {
	return s.setAlgebraStore(destination, keys, setUnionOperation)
}

// SetIntersectStore atomically replaces destination with the intersection
// result and returns its cardinality.
func (s *Store) SetIntersectStore(destination string, keys []string) (int64, error) {
	return s.setAlgebraStore(destination, keys, setIntersectOperation)
}

// SetDiffStore atomically replaces destination with the difference result and
// returns its cardinality.
func (s *Store) SetDiffStore(destination string, keys []string) (int64, error) {
	return s.setAlgebraStore(destination, keys, setDiffOperation)
}

func (s *Store) setAlgebraStore(destination string, keys []string, operation setAlgebraOperation) (int64, error) {
	if len(keys) == 0 {
		return 0, errors.New("ERR set operation requires at least one source key")
	}

	unlock := s.lockAll()
	defer unlock()

	// Read every source before touching destination. This is required when the
	// destination is also one of the source keys.
	sets, err := s.setSourcesLocked(keys)
	if err != nil {
		return 0, err
	}
	result := applySetAlgebra(sets, operation)
	destinationShard := s.shardFor(destination)

	if len(result) == 0 {
		// Redis STORE variants delete the destination when the result is empty,
		// regardless of the destination's previous datatype.
		s.remove(destinationShard, destination)
		return 0, nil
	}

	packed, err := encodePackedSet(result)
	if err != nil {
		return 0, err
	}
	updated := setPreparedEntry(packed)
	// expiresAt intentionally remains zero: STORE overwrites destination and
	// clears any previous TTL.
	if err := s.publish(destinationShard, destination, updated); err != nil {
		return 0, err
	}
	return int64(len(result)), nil
}

func (s *Store) setSourcesLocked(keys []string) ([][][]byte, error) {
	now := s.now()
	sets := make([][][]byte, len(keys))
	for i, key := range keys {
		sh := s.shardFor(key)
		e, ok := sh.get(key)
		if !ok || e.expired(now) {
			continue
		}
		if e.valueType != TypeSet {
			return nil, setWrongType()
		}
		members, err := s.setMembersFromEntry(sh, e)
		if err != nil {
			return nil, err
		}
		sets[i] = members
	}
	return sets, nil
}

func applySetAlgebra(sets [][][]byte, operation setAlgebraOperation) [][]byte {
	if len(sets) == 0 {
		return nil
	}

	switch operation {
	case setUnionOperation:
		var result [][]byte
		for _, members := range sets {
			result = mergeSortedSetUnion(result, members)
		}
		return result

	case setIntersectOperation:
		result := cloneSetMembers(sets[0])
		for i := 1; i < len(sets) && len(result) > 0; i++ {
			result = mergeSortedSetIntersection(result, sets[i])
		}
		return result

	case setDiffOperation:
		result := cloneSetMembers(sets[0])
		for i := 1; i < len(sets) && len(result) > 0; i++ {
			result = mergeSortedSetDifference(result, sets[i])
		}
		return result
	}
	return nil
}

func cloneSetMembers(members [][]byte) [][]byte {
	out := make([][]byte, len(members))
	for i := range members {
		out[i] = append([]byte(nil), members[i]...)
	}
	return out
}

func mergeSortedSetUnion(left, right [][]byte) [][]byte {
	out := make([][]byte, 0, len(left)+len(right))
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		switch cmp := bytes.Compare(left[i], right[j]); {
		case cmp < 0:
			out = append(out, append([]byte(nil), left[i]...))
			i++
		case cmp > 0:
			out = append(out, append([]byte(nil), right[j]...))
			j++
		default:
			out = append(out, append([]byte(nil), left[i]...))
			i++
			j++
		}
	}
	for ; i < len(left); i++ {
		out = append(out, append([]byte(nil), left[i]...))
	}
	for ; j < len(right); j++ {
		out = append(out, append([]byte(nil), right[j]...))
	}
	return out
}

func mergeSortedSetIntersection(left, right [][]byte) [][]byte {
	capacity := len(left)
	if len(right) < capacity {
		capacity = len(right)
	}
	out := make([][]byte, 0, capacity)
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		switch cmp := bytes.Compare(left[i], right[j]); {
		case cmp < 0:
			i++
		case cmp > 0:
			j++
		default:
			out = append(out, append([]byte(nil), left[i]...))
			i++
			j++
		}
	}
	return out
}

func mergeSortedSetDifference(left, right [][]byte) [][]byte {
	out := make([][]byte, 0, len(left))
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		switch cmp := bytes.Compare(left[i], right[j]); {
		case cmp < 0:
			out = append(out, append([]byte(nil), left[i]...))
			i++
		case cmp > 0:
			j++
		default:
			i++
			j++
		}
	}
	for ; i < len(left); i++ {
		out = append(out, append([]byte(nil), left[i]...))
	}
	return out
}
