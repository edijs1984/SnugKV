package engine

import "errors"

// StreamRefPolicy controls how stream entry deletion interacts with consumer
// group pending-entry references. KEEPREF is the Redis-compatible default.
type StreamRefPolicy uint8

const (
	StreamRefKeep StreamRefPolicy = iota
	StreamRefDelete
	StreamRefAcked
)

func validStreamRefPolicy(policy StreamRefPolicy) bool {
	return policy == StreamRefKeep || policy == StreamRefDelete || policy == StreamRefAcked
}

func streamEntryIndex(entries []StreamEntry, id StreamID) int {
	for i := range entries {
		if entries[i].ID.equal(id) {
			return i
		}
	}
	return -1
}

func streamAckedByAllGroups(state *packedStream, id StreamID) bool {
	if len(state.Groups) == 0 {
		return false
	}
	for i := range state.Groups {
		group := &state.Groups[i]
		if group.LastDeliveredID.less(id) {
			return false
		}
		if streamPendingIndex(group.Pending, id) >= 0 {
			return false
		}
	}
	return true
}

func streamRemovePendingRefs(state *packedStream, ids map[StreamID]struct{}) int {
	removed := 0
	for gi := range state.Groups {
		group := &state.Groups[gi]
		kept := group.Pending[:0]
		for _, pending := range group.Pending {
			if _, ok := ids[pending.ID]; ok {
				removed++
				continue
			}
			kept = append(kept, pending)
		}
		group.Pending = kept
	}
	return removed
}

func streamRemoveOnePendingRef(group *streamGroup, id StreamID) bool {
	index := streamPendingIndex(group.Pending, id)
	if index < 0 {
		return false
	}
	copy(group.Pending[index:], group.Pending[index+1:])
	group.Pending = group.Pending[:len(group.Pending)-1]
	return true
}

func streamPolicyAllowsTrim(state *packedStream, id StreamID, policy StreamRefPolicy) bool {
	if policy != StreamRefAcked || len(state.Groups) == 0 {
		return true
	}
	return streamAckedByAllGroups(state, id)
}

func trimStreamMaxLenPolicy(state *packedStream, maxLen, limit int, policy StreamRefPolicy) int {
	need := len(state.Entries) - maxLen
	if need <= 0 {
		return 0
	}
	kept := make([]StreamEntry, 0, len(state.Entries))
	deletedIDs := make(map[StreamID]struct{})
	deleted := 0
	examined := 0
	for i, item := range state.Entries {
		if deleted >= need || limit > 0 && examined >= limit {
			kept = append(kept, state.Entries[i:]...)
			break
		}
		examined++
		if !streamPolicyAllowsTrim(state, item.ID, policy) {
			kept = append(kept, item)
			continue
		}
		deletedIDs[item.ID] = struct{}{}
		deleted++
	}
	if deleted == 0 {
		return 0
	}
	state.Entries = kept
	if policy == StreamRefDelete {
		streamRemovePendingRefs(state, deletedIDs)
	}
	return deleted
}

func trimStreamMinIDPolicy(state *packedStream, minID StreamID, limit int, policy StreamRefPolicy) int {
	kept := make([]StreamEntry, 0, len(state.Entries))
	deletedIDs := make(map[StreamID]struct{})
	deleted := 0
	examined := 0
	for i, item := range state.Entries {
		if !item.ID.less(minID) {
			kept = append(kept, state.Entries[i:]...)
			break
		}
		if limit > 0 && examined >= limit {
			kept = append(kept, state.Entries[i:]...)
			break
		}
		examined++
		if !streamPolicyAllowsTrim(state, item.ID, policy) {
			kept = append(kept, item)
			continue
		}
		deletedIDs[item.ID] = struct{}{}
		deleted++
	}
	if deleted == 0 {
		return 0
	}
	state.Entries = kept
	if policy == StreamRefDelete {
		streamRemovePendingRefs(state, deletedIDs)
	}
	return deleted
}

// StreamAddWithPolicy is the policy-aware XADD path introduced by Redis 8.2.
// Older engine callers keep using StreamAdd, whose behavior is KEEPREF.
func (s *Store) StreamAddWithPolicy(key, idSpec string, fields []StreamField, options StreamAddOptions, policy StreamRefPolicy) (StreamID, bool, error) {
	if !validStreamRefPolicy(policy) {
		return StreamID{}, false, errors.New("ERR syntax error")
	}
	if len(fields) == 0 {
		return StreamID{}, false, errors.New("ERR wrong number of arguments for 'xadd' command")
	}
	if options.HasMaxLen && options.MaxLen < 0 || options.Limit < 0 {
		return StreamID{}, false, errors.New("ERR value is not an integer or out of range")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := s.now()
	old, exists := sh.get(key)
	if exists && sh.expired(key, old, now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}
	if !exists && options.NoMkStream {
		return StreamID{}, false, nil
	}
	state := packedStream{}
	var expiresAt stamp
	if exists {
		if old.valueType != TypeStream {
			return StreamID{}, false, streamWrongType()
		}
		var err error
		state, err = s.streamStateFromEntry(sh, old)
		if err != nil {
			return StreamID{}, false, err
		}
		expiresAt = sh.expirationAt(key, old)
	}
	nowMS := uint64(0)
	if now.UnixMilli() > 0 {
		nowMS = uint64(now.UnixMilli())
	}
	id, err := resolveStreamAddID(idSpec, nowMS, state.LastID)
	if err != nil {
		return StreamID{}, false, err
	}
	state.LastID = id
	state.EntriesAdded++
	state.Entries = append(state.Entries, StreamEntry{ID: id, Fields: cloneStreamFields(fields)})
	if options.HasMaxLen {
		trimStreamMaxLenPolicy(&state, options.MaxLen, options.Limit, policy)
	}
	if options.HasMinID {
		trimStreamMinIDPolicy(&state, options.MinID, options.Limit, policy)
	}
	packed, err := encodePackedStream(state)
	if err != nil {
		return StreamID{}, false, err
	}
	updated := streamPreparedEntry(packed)
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return StreamID{}, false, err
	}
	return id, true, nil
}

func (s *Store) StreamTrimMaxLenWithPolicy(key string, maxLen, limit int, policy StreamRefPolicy) (int64, error) {
	if maxLen < 0 || limit < 0 || !validStreamRefPolicy(policy) {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
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
	if e.valueType != TypeStream {
		return 0, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	trimmed := trimStreamMaxLenPolicy(&state, maxLen, limit, policy)
	if trimmed == 0 {
		return 0, nil
	}
	if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
		return 0, err
	}
	return int64(trimmed), nil
}

func (s *Store) StreamTrimMinIDWithPolicy(key string, minID StreamID, limit int, policy StreamRefPolicy) (int64, error) {
	if limit < 0 || !validStreamRefPolicy(policy) {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
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
	if e.valueType != TypeStream {
		return 0, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	trimmed := trimStreamMinIDPolicy(&state, minID, limit, policy)
	if trimmed == 0 {
		return 0, nil
	}
	if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
		return 0, err
	}
	return int64(trimmed), nil
}

// StreamDeleteEx implements XDELEX and returns one Redis 8.2 status code per ID.
func (s *Store) StreamDeleteEx(key string, ids []StreamID, policy StreamRefPolicy) ([]int64, error) {
	if !validStreamRefPolicy(policy) {
		return nil, errors.New("ERR syntax error")
	}
	statuses := make([]int64, len(ids))
	for i := range statuses {
		statuses[i] = -1
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return statuses, nil
	}
	if e.valueType != TypeStream {
		return nil, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	changed := false
	for i, id := range ids {
		index := streamEntryIndex(state.Entries, id)
		if policy == StreamRefDelete {
			if streamRemovePendingRefs(&state, map[StreamID]struct{}{id: {}}) > 0 {
				changed = true
			}
		}
		if index < 0 {
			continue
		}
		if policy == StreamRefAcked && !streamAckedByAllGroups(&state, id) {
			statuses[i] = 2
			continue
		}
		copy(state.Entries[index:], state.Entries[index+1:])
		state.Entries = state.Entries[:len(state.Entries)-1]
		noteStreamDeleted(&state, id)
		statuses[i] = 1
		changed = true
	}
	if !changed {
		return statuses, nil
	}
	if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
		return nil, err
	}
	return statuses, nil
}

// StreamAckDelete implements XACKDEL. The target group's PEL reference is
// acknowledged first; the selected policy then controls references in other
// groups and whether the live stream entry may be removed.
func (s *Store) StreamAckDelete(key, groupName string, ids []StreamID, policy StreamRefPolicy) ([]int64, error) {
	if !validStreamRefPolicy(policy) {
		return nil, errors.New("ERR syntax error")
	}
	statuses := make([]int64, len(ids))
	for i := range statuses {
		statuses[i] = -1
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return statuses, nil
	}
	if e.valueType != TypeStream {
		return nil, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	gi := streamGroupIndex(state.Groups, groupName)
	if gi < 0 {
		return nil, streamNoGroup(groupName, key)
	}
	changed := false
	for i, id := range ids {
		changedForID := false

		if streamRemoveOnePendingRef(&state.Groups[gi], id) {
			changed = true
			changedForID = true
		}

		if policy == StreamRefDelete {
			if streamRemovePendingRefs(
				&state,
				map[StreamID]struct{}{id: {}},
			) > 0 {
				changed = true
				changedForID = true
			}
		}

		index := streamEntryIndex(state.Entries, id)
		if index < 0 {
			if changedForID {
				statuses[i] = 1
			}
			continue
		}
		if policy == StreamRefAcked && !streamAckedByAllGroups(&state, id) {
			statuses[i] = 2
			continue
		}
		copy(state.Entries[index:], state.Entries[index+1:])
		state.Entries = state.Entries[:len(state.Entries)-1]
		noteStreamDeleted(&state, id)
		statuses[i] = 1
		changed = true
	}
	if !changed {
		return statuses, nil
	}
	if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
		return nil, err
	}
	return statuses, nil
}
