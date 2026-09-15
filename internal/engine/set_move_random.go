package engine

import (
	"bytes"
	"crypto/rand"
	"errors"
	"math"
	"math/big"
	"sort"
)

// SetMove atomically moves member from source to destination. Existing TTLs on
// both keys are preserved. When source == destination the command is a no-op
// that reports whether member exists, matching Redis SMOVE semantics.
func (s *Store) SetMove(source, destination string, member []byte) (bool, error) {
	unlock := s.lockAll()
	defer unlock()

	now := s.now()
	sourceShard := s.shardFor(source)
	sourceEntry, sourceExists := sourceShard.get(source)
	if !sourceExists || sourceEntry.expired(now) {
		return false, nil
	}
	if sourceEntry.valueType != TypeSet {
		return false, setWrongType()
	}

	sourceMembers, err := s.setMembersFromEntry(sourceShard, sourceEntry)
	if err != nil {
		return false, err
	}
	sourceIndex := sort.Search(len(sourceMembers), func(i int) bool {
		return bytes.Compare(sourceMembers[i], member) >= 0
	})
	sourceHasMember := sourceIndex < len(sourceMembers) && bytes.Equal(sourceMembers[sourceIndex], member)

	if source == destination {
		return sourceHasMember, nil
	}

	destinationShard := s.shardFor(destination)
	destinationEntry, destinationExists := destinationShard.get(destination)
	if destinationExists && destinationEntry.expired(now) {
		destinationExists = false
	}
	if destinationExists && destinationEntry.valueType != TypeSet {
		return false, setWrongType()
	}

	// Redis validates destination type before checking whether member is present
	// in source. Keep the same behavior.
	if !sourceHasMember {
		return false, nil
	}

	var destinationMembers [][]byte
	if destinationExists {
		destinationMembers, err = s.setMembersFromEntry(destinationShard, destinationEntry)
		if err != nil {
			return false, err
		}
	}

	updates := make(map[string]preparedEntry, 2)
	deletions := make(map[string]bool, 1)

	remaining := make([][]byte, 0, len(sourceMembers)-1)
	remaining = append(remaining, sourceMembers[:sourceIndex]...)
	remaining = append(remaining, sourceMembers[sourceIndex+1:]...)
	if len(remaining) == 0 {
		deletions[source] = true
	} else {
		packed, err := encodePackedSet(remaining)
		if err != nil {
			return false, err
		}
		updated := setPreparedEntry(packed)
		updated.expiresAt = sourceEntry.expiresAt
		updates[source] = updated
	}

	destinationIndex := sort.Search(len(destinationMembers), func(i int) bool {
		return bytes.Compare(destinationMembers[i], member) >= 0
	})
	destinationHasMember := destinationIndex < len(destinationMembers) && bytes.Equal(destinationMembers[destinationIndex], member)
	if !destinationHasMember {
		next := make([][]byte, len(destinationMembers)+1)
		copy(next, destinationMembers[:destinationIndex])
		next[destinationIndex] = append([]byte(nil), member...)
		copy(next[destinationIndex+1:], destinationMembers[destinationIndex:])

		packed, err := encodePackedSet(next)
		if err != nil {
			return false, err
		}
		updated := setPreparedEntry(packed)
		if destinationExists {
			updated.expiresAt = destinationEntry.expiresAt
		}
		updates[destination] = updated
	}

	if err := s.applyPreparedBatchLocked(updates, deletions, true); err != nil {
		return false, err
	}
	return true, nil
}

// SetRandomMembers returns random members without modifying key. Positive count
// returns at most count distinct members; negative count returns exactly
// abs(count) members with replacement. Missing keys are empty sets.
func (s *Store) SetRandomMembers(key string, count int64) ([][]byte, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return nil, nil
	}
	if e.valueType != TypeSet {
		return nil, setWrongType()
	}
	members, err := s.setMembersFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 || count == 0 {
		return nil, nil
	}

	if count > 0 {
		want := count
		if want > int64(len(members)) {
			want = int64(len(members))
		}
		indexes, err := randomDistinctSetIndexes(len(members), int(want))
		if err != nil {
			return nil, err
		}
		out := make([][]byte, 0, len(indexes))
		for _, index := range indexes {
			out = append(out, append([]byte(nil), members[index]...))
		}
		return out, nil
	}

	if count == math.MinInt64 {
		return nil, errors.New("ERR count is too large")
	}
	want := int(-count)
	out := make([][]byte, 0, want)
	for i := 0; i < want; i++ {
		index, err := randomSetIndex(len(members))
		if err != nil {
			return nil, err
		}
		out = append(out, append([]byte(nil), members[index]...))
	}
	return out, nil
}

// SetPop removes and returns up to count distinct random members. A surviving
// set preserves its TTL; removing every member deletes the key.
func (s *Store) SetPop(key string, count int64) ([][]byte, error) {
	if count < 0 {
		return nil, errors.New("ERR value is out of range, must be positive")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return nil, nil
	}
	if e.valueType != TypeSet {
		return nil, setWrongType()
	}
	if count == 0 {
		return nil, nil
	}

	members, err := s.setMembersFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	want := count
	if want > int64(len(members)) {
		want = int64(len(members))
	}
	indexes, err := randomDistinctSetIndexes(len(members), int(want))
	if err != nil {
		return nil, err
	}

	popped := make([][]byte, 0, len(indexes))
	for _, index := range indexes {
		popped = append(popped, append([]byte(nil), members[index]...))
	}
	if len(indexes) == len(members) {
		s.remove(sh, key)
		return popped, nil
	}

	ordered := append([]int(nil), indexes...)
	sort.Ints(ordered)
	remaining := make([][]byte, 0, len(members)-len(ordered))
	removeAt := 0
	for i, member := range members {
		if removeAt < len(ordered) && ordered[removeAt] == i {
			removeAt++
			continue
		}
		remaining = append(remaining, member)
	}

	packed, err := encodePackedSet(remaining)
	if err != nil {
		return nil, err
	}
	updated := setPreparedEntry(packed)
	updated.expiresAt = e.expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return nil, err
	}
	return popped, nil
}

func randomSetIndex(n int) (int, error) {
	if n <= 0 {
		return 0, errors.New("ERR empty set")
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, errors.New("ERR random source unavailable")
	}
	return int(value.Int64()), nil
}

// randomDistinctSetIndexes performs a partial Fisher-Yates shuffle using a
// sparse swap table, so temporary memory is O(count) rather than O(cardinality).
func randomDistinctSetIndexes(cardinality, count int) ([]int, error) {
	if count <= 0 || cardinality <= 0 {
		return nil, nil
	}
	if count >= cardinality {
		indexes := make([]int, cardinality)
		for i := range indexes {
			indexes[i] = i
		}
		return indexes, nil
	}

	swaps := make(map[int]int, 2*count)
	selected := make([]int, count)
	for i := 0; i < count; i++ {
		offset, err := randomSetIndex(cardinality - i)
		if err != nil {
			return nil, err
		}
		j := i + offset
		left := i
		if value, ok := swaps[i]; ok {
			left = value
		}
		right := j
		if value, ok := swaps[j]; ok {
			right = value
		}
		swaps[i] = right
		swaps[j] = left
		selected[i] = right
	}
	return selected, nil
}

// applyPreparedBatchLocked preflights a small multi-key batch against maxmemory
// and then publishes it with all shards already locked. Growth simulation keeps
// old arena allocations live, making admission conservative until publication.
func (s *Store) applyPreparedBatchLocked(updates map[string]preparedEntry, deletions map[string]bool, enforce bool) error {
	var before, after, beforeMeta, afterMeta, extraIndex, extraEntries, extraArena uint64
	allocations := make(map[*shard][]int)
	growth := make(map[*shard]int)

	for key := range deletions {
		sh := s.shardFor(key)
		if old, ok := sh.get(key); ok {
			before += entryCharge(key, old)
			beforeMeta += metadataCharge(old)
			growth[sh]--
		}
	}
	for key, prepared := range updates {
		sh := s.shardFor(key)
		if old, ok := sh.get(key); ok {
			before += entryCharge(key, old)
			beforeMeta += metadataCharge(old)
		} else {
			growth[sh]++
		}
		after += entryCharge(key, prepared)
		if s.shouldTrackActivity(prepared.entry) {
			afterMeta += entryMetaBytes
		} else {
			afterMeta += metadataCharge(prepared.entry)
		}
		allocations[sh] = append(allocations[sh], len(prepared.data))
	}
	for sh, n := range growth {
		extraIndex += sh.data.GrowthBytes(n)
		extraEntries += sh.entryGrowthBytes(n)
	}
	for sh, lengths := range allocations {
		extraArena += sh.arena.GrowthFor(lengths)
	}

	s.memory.mu.Lock()
	next := s.memory.used - before - beforeMeta + after + afterMeta + extraIndex + extraEntries + extraArena
	if enforce && s.memory.max > 0 && next > s.memory.max {
		s.memory.mu.Unlock()
		return ErrOOM
	}
	s.memory.mu.Unlock()

	for key := range deletions {
		s.remove(s.shardFor(key), key)
	}
	for key, prepared := range updates {
		if err := s.publishRecord(s.shardFor(key), key, prepared, false); err != nil {
			return err
		}
	}
	return nil
}
