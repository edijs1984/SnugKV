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

func streamAckedByAllGroups(state *streamState, id StreamID) bool {
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

func streamRemovePendingRefs(state *streamState, ids map[StreamID]struct{}) int {
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

func streamPolicyAllowsTrim(state *streamState, id StreamID, policy StreamRefPolicy) bool {
	if policy != StreamRefAcked || len(state.Groups) == 0 {
		return true
	}
	return streamAckedByAllGroups(state, id)
}

// streamTrimPlan lists the entries a trim removes. The plan is computed before
// anything is changed so a failed publish leaves the stream untouched.
type streamTrimPlan struct {
	ids map[StreamID]struct{}
	// prefix is the number of oldest entries removed when the plan is exactly
	// that many entries from the front, and -1 when it is not.
	prefix int
}

func (p streamTrimPlan) empty() bool { return len(p.ids) == 0 }

func planTrimMaxLen(state *streamState, maxLen, limit int, policy StreamRefPolicy) (streamTrimPlan, bool) {
	plan := streamTrimPlan{ids: map[StreamID]struct{}{}}
	need := state.log.Len() - maxLen
	if need <= 0 {
		return plan, false
	}
	deleted, examined := 0, 0
	skipped := false
	state.log.ForEachID(func(id StreamID) bool {
		if deleted >= need || limit > 0 && examined >= limit {
			return false
		}
		examined++
		if !streamPolicyAllowsTrim(state, id, policy) {
			skipped = true
			return true
		}
		plan.ids[id] = struct{}{}
		deleted++
		return true
	})
	return finishTrimPlan(state, plan, skipped, policy)
}

func planTrimMinID(state *streamState, minID StreamID, limit int, policy StreamRefPolicy, already map[StreamID]struct{}) (streamTrimPlan, bool) {
	plan := streamTrimPlan{ids: map[StreamID]struct{}{}}
	deleted, examined := 0, 0
	skipped := len(already) > 0
	state.log.ForEachID(func(id StreamID) bool {
		if _, gone := already[id]; gone {
			return true
		}
		if !id.less(minID) {
			return false
		}
		if limit > 0 && examined >= limit {
			return false
		}
		examined++
		if !streamPolicyAllowsTrim(state, id, policy) {
			skipped = true
			return true
		}
		plan.ids[id] = struct{}{}
		deleted++
		return true
	})
	return finishTrimPlan(state, plan, skipped, policy)
}

// finishTrimPlan records whether the plan is a pure prefix and, for DELREF,
// drops the pending references of the removed entries from the working groups.
// It reports whether any group changed.
func finishTrimPlan(state *streamState, plan streamTrimPlan, skipped bool, policy StreamRefPolicy) (streamTrimPlan, bool) {
	plan.prefix = -1
	if len(plan.ids) == 0 {
		return plan, false
	}
	if !skipped {
		plan.prefix = len(plan.ids)
	}
	groupsChanged := false
	if policy == StreamRefDelete && streamRemovePendingRefs(state, plan.ids) > 0 {
		groupsChanged = true
	}
	return plan, groupsChanged
}

// applyTrimPlans removes the planned entries from the shared log.
func applyTrimPlans(state streamState, plans ...streamTrimPlan) int {
	total, prefix := 0, 0
	pure := true
	union := map[StreamID]struct{}{}
	for _, plan := range plans {
		if plan.empty() {
			continue
		}
		total += len(plan.ids)
		if plan.prefix < 0 {
			pure = false
		} else {
			prefix += plan.prefix
		}
		for id := range plan.ids {
			union[id] = struct{}{}
		}
	}
	if total == 0 {
		return 0
	}
	if pure {
		return state.log.TrimFront(prefix)
	}
	return state.log.Delete(union)
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
	state := streamState{log: newStreamBody()}
	var expiresAt stamp
	if exists {
		if old.valueType != TypeStream {
			return StreamID{}, false, streamWrongType()
		}
		var err error
		state, err = s.streamStateFromEntry(sh, key, old)
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
	size := streamEntrySize(fields)
	if state.log.dataBytes+uint64(size)+streamStubAllowance(state) > maxPackedStreamBytes {
		return StreamID{}, false, errStreamTooLarge
	}
	if err := s.admitStreamGrowth(streamAddGrowth(size)); err != nil {
		return StreamID{}, false, err
	}

	mark := state.log.mark()
	before := mark.memory
	if !exists {
		before = 0
	}
	state.log.Append(id, fields)
	state.LastID = id
	state.EntriesAdded++
	state.syncLog()

	var plans []streamTrimPlan
	groupsChanged := false
	var first map[StreamID]struct{}
	if options.HasMaxLen {
		plan, changed := planTrimMaxLen(&state, options.MaxLen, options.Limit, policy)
		plans = append(plans, plan)
		groupsChanged = groupsChanged || changed
		first = plan.ids
	}
	if options.HasMinID {
		plan, changed := planTrimMinID(&state, options.MinID, options.Limit, policy, first)
		plans = append(plans, plan)
		groupsChanged = groupsChanged || changed
	}

	if !exists {
		applyTrimPlans(state, plans...)
		if err := s.createStreamLocked(sh, key, state, expiresAt); err != nil {
			return StreamID{}, false, err
		}
		return id, true, nil
	}
	if groupsChanged {
		if err := s.publishStreamStateLocked(sh, key, old, state); err != nil {
			state.log.rollback(mark)
			return StreamID{}, false, err
		}
	}
	applyTrimPlans(state, plans...)
	s.settleStreamMemory(before, state.log.memory())
	return id, true, nil
}

func (s *Store) StreamTrimMaxLenWithPolicy(key string, maxLen, limit int, policy StreamRefPolicy) (int64, error) {
	if maxLen < 0 || limit < 0 || !validStreamRefPolicy(policy) {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
	return s.streamTrimWithPolicy(key, policy, func(state *streamState) (streamTrimPlan, bool) {
		return planTrimMaxLen(state, maxLen, limit, policy)
	})
}

func (s *Store) StreamTrimMinIDWithPolicy(key string, minID StreamID, limit int, policy StreamRefPolicy) (int64, error) {
	if limit < 0 || !validStreamRefPolicy(policy) {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
	return s.streamTrimWithPolicy(key, policy, func(state *streamState) (streamTrimPlan, bool) {
		return planTrimMinID(state, minID, limit, policy, nil)
	})
}

func (s *Store) streamTrimWithPolicy(key string, policy StreamRefPolicy, plan func(*streamState) (streamTrimPlan, bool)) (int64, error) {
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
	state, err := s.streamStateFromEntry(sh, key, e)
	if err != nil {
		return 0, err
	}
	p, groupsChanged := plan(&state)
	if p.empty() {
		return 0, nil
	}
	if groupsChanged {
		if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
			return 0, err
		}
	}
	before := state.log.memory()
	removed := applyTrimPlans(state, p)
	s.settleStreamMemory(before, state.log.memory())
	return int64(removed), nil
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
	state, err := s.streamStateFromEntry(sh, key, e)
	if err != nil {
		return nil, err
	}
	toDelete := map[StreamID]struct{}{}
	groupsChanged := false
	for i, id := range ids {
		_, gone := toDelete[id]
		exists := !gone && state.log.Has(id)
		if policy == StreamRefDelete {
			if streamRemovePendingRefs(&state, map[StreamID]struct{}{id: {}}) > 0 {
				groupsChanged = true
			}
		}
		if !exists {
			continue
		}
		if policy == StreamRefAcked && !streamAckedByAllGroups(&state, id) {
			statuses[i] = 2
			continue
		}
		toDelete[id] = struct{}{}
		noteStreamDeleted(&state, id)
		statuses[i] = 1
	}
	if err := s.commitStreamChange(sh, key, e, state, groupsChanged, toDelete); err != nil {
		return nil, err
	}
	return statuses, nil
}

// commitStreamChange publishes changed groups first and only then removes
// entries, so a failed publish leaves the stream as it was.
func (s *Store) commitStreamChange(sh *shard, key string, old entry, state streamState, groupsChanged bool, toDelete map[StreamID]struct{}) error {
	if !groupsChanged && len(toDelete) == 0 {
		return nil
	}
	if groupsChanged {
		if err := s.publishStreamStateLocked(sh, key, old, state); err != nil {
			return err
		}
	}
	before := state.log.memory()
	state.log.Delete(toDelete)
	state.syncLog()
	s.settleStreamMemory(before, state.log.memory())
	return nil
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
	state, err := s.streamStateFromEntry(sh, key, e)
	if err != nil {
		return nil, err
	}
	gi := streamGroupIndex(state.Groups, groupName)
	if gi < 0 {
		return nil, streamNoGroup(groupName, key)
	}
	toDelete := map[StreamID]struct{}{}
	groupsChanged := false
	for i, id := range ids {
		changedForID := false

		if streamRemoveOnePendingRef(&state.Groups[gi], id) {
			groupsChanged = true
			changedForID = true
		}

		if policy == StreamRefDelete {
			if streamRemovePendingRefs(
				&state,
				map[StreamID]struct{}{id: {}},
			) > 0 {
				groupsChanged = true
				changedForID = true
			}
		}

		_, gone := toDelete[id]
		if gone || !state.log.Has(id) {
			if changedForID {
				statuses[i] = 1
			}
			continue
		}
		if policy == StreamRefAcked && !streamAckedByAllGroups(&state, id) {
			statuses[i] = 2
			continue
		}
		toDelete[id] = struct{}{}
		noteStreamDeleted(&state, id)
		statuses[i] = 1
	}
	if err := s.commitStreamChange(sh, key, e, state, groupsChanged, toDelete); err != nil {
		return nil, err
	}
	return statuses, nil
}
