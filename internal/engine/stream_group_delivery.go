package engine

import (
	"errors"
	"math"
	"sort"
	"time"
)

type StreamGroupReadCursor struct {
	ID  StreamID
	New bool
}

type StreamPendingConsumer struct {
	Name  string
	Count int64
}

type StreamPendingSummary struct {
	Count     int64
	MinID     *StreamID
	MaxID     *StreamID
	Consumers []StreamPendingConsumer
}

type StreamPendingInfo struct {
	ID         StreamID
	Consumer   string
	IdleMillis int64
	Deliveries uint64
}

func streamEntryByID(entries []StreamEntry, id StreamID) (StreamEntry, bool) {
	i := sort.Search(len(entries), func(i int) bool { return !entries[i].ID.less(id) })
	if i < len(entries) && entries[i].ID.equal(id) {
		return cloneStreamEntry(entries[i]), true
	}
	return StreamEntry{}, false
}

func ensureStreamConsumer(group *streamGroup, name string, nowMS int64) *streamConsumer {
	i := streamConsumerIndex(group.Consumers, name)
	if i < 0 {
		group.Consumers = append(group.Consumers, streamConsumer{Name: name, SeenAt: nowMS, ActiveAt: nowMS})
		return &group.Consumers[len(group.Consumers)-1]
	}
	group.Consumers[i].SeenAt = nowMS
	return &group.Consumers[i]
}

func (s *Store) StreamGroupRead(keys []string, groupName, consumer string, cursors []StreamGroupReadCursor, count int, noAck bool) ([]StreamReadResult, error) {
	if len(keys) != len(cursors) {
		return nil, errors.New("ERR mismatched stream key/ID count")
	}
	if count <= 0 {
		return nil, errors.New("ERR count should be greater than 0")
	}
	unlock := s.lockAll()
	defer unlock()

	now := s.now()
	nowMS := now.UnixMilli()
	if nowMS < 0 {
		nowMS = 0
	}
	results := make([]StreamReadResult, 0, len(keys))

	for i, key := range keys {
		sh := s.shardFor(key)
		e, ok := sh.get(key)
		if !ok || sh.expired(key, e, now) {
			if ok {
				s.remove(sh, key)
			}
			return nil, streamNoGroup(groupName, key)
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
		group := &state.Groups[gi]
		consumerState := ensureStreamConsumer(group, consumer, nowMS)
		entries := make([]StreamEntry, 0)

		if cursors[i].New {
			if group.EntriesRead < 0 {
				if inferred := inferStreamGroupEntriesRead(
					state,
					group.LastDeliveredID,
				); inferred >= 0 {
					group.EntriesRead = inferred
				}
			}

			for _, item := range state.Entries {
				if !group.LastDeliveredID.less(item.ID) {
					continue
				}
				entries = append(entries, cloneStreamEntry(item))
				group.LastDeliveredID = item.ID
				if group.EntriesRead >= 0 {
					group.EntriesRead++
				} else if item.ID.equal(state.LastID) &&
					state.EntriesAdded <= math.MaxInt64 {
					// Redis restores lag tracking when an
					// unknown group catches up to the stream.
					group.EntriesRead =
						int64(state.EntriesAdded)
				}
				if !noAck {
					group.Pending = append(group.Pending, streamPending{ID: item.ID, Consumer: consumer, DeliveredAt: nowMS, Deliveries: 1})
				}
				if len(entries) == count {
					break
				}
			}
		} else {
			for pi := range group.Pending {
				pending := &group.Pending[pi]
				if pending.Consumer != consumer || !cursors[i].ID.less(pending.ID) {
					continue
				}
				if entry, exists := streamEntryByID(state.Entries, pending.ID); exists {
					entries = append(entries, entry)
				} else {
					entries = append(entries, StreamEntry{ID: pending.ID, Fields: nil})
				}
				pending.DeliveredAt = nowMS
				pending.Deliveries++
				if len(entries) == count {
					break
				}
			}
		}
		if len(entries) > 0 {
			consumerState.ActiveAt = nowMS
		}

		if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			results = append(results, StreamReadResult{Key: key, Entries: entries})
		}
	}
	return results, nil
}

func (s *Store) StreamGroupAck(key, groupName string, ids []StreamID) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return 0, streamNoGroup(groupName, key)
	}
	if e.valueType != TypeStream {
		return 0, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	gi := streamGroupIndex(state.Groups, groupName)
	if gi < 0 {
		return 0, streamNoGroup(groupName, key)
	}
	wanted := make(map[StreamID]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	group := &state.Groups[gi]
	kept := group.Pending[:0]
	var acked int64
	for _, pending := range group.Pending {
		if _, ok := wanted[pending.ID]; ok {
			acked++
			continue
		}
		kept = append(kept, pending)
	}
	if acked == 0 {
		return 0, nil
	}
	group.Pending = kept
	if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
		return 0, err
	}
	return acked, nil
}

func (s *Store) StreamGroupPendingSummary(key, groupName string) (StreamPendingSummary, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return StreamPendingSummary{}, streamNoGroup(groupName, key)
	}
	if e.valueType != TypeStream {
		return StreamPendingSummary{}, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return StreamPendingSummary{}, err
	}
	gi := streamGroupIndex(state.Groups, groupName)
	if gi < 0 {
		return StreamPendingSummary{}, streamNoGroup(groupName, key)
	}
	pending := state.Groups[gi].Pending
	result := StreamPendingSummary{Count: int64(len(pending))}
	if len(pending) == 0 {
		return result, nil
	}
	sorted := append([]streamPending(nil), pending...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID.less(sorted[j].ID) })
	minID, maxID := sorted[0].ID, sorted[len(sorted)-1].ID
	result.MinID, result.MaxID = &minID, &maxID
	counts := make(map[string]int64)
	for _, item := range pending {
		if item.Consumer != "" {
			counts[item.Consumer]++
		}
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result.Consumers = append(result.Consumers, StreamPendingConsumer{Name: name, Count: counts[name]})
	}
	return result, nil
}

func (s *Store) StreamGroupPendingRange(key, groupName string, start, end StreamRangeBound, count int, consumer string, minIdle time.Duration) ([]StreamPendingInfo, error) {
	if count <= 0 || minIdle < 0 {
		return nil, errors.New("ERR value is not an integer or out of range")
	}
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, streamNoGroup(groupName, key)
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
	nowMS := s.now().UnixMilli()
	if nowMS < 0 {
		nowMS = 0
	}
	pending := append([]streamPending(nil), state.Groups[gi].Pending...)
	sort.Slice(pending, func(i, j int) bool { return pending[i].ID.less(pending[j].ID) })
	out := make([]StreamPendingInfo, 0)
	for _, item := range pending {
		if !streamIDInRange(item.ID, start, end) || consumer != "" && item.Consumer != consumer {
			continue
		}
		idle := nowMS - item.DeliveredAt
		if idle < 0 {
			idle = 0
		}
		if time.Duration(idle)*time.Millisecond < minIdle {
			continue
		}
		out = append(out, StreamPendingInfo{ID: item.ID, Consumer: item.Consumer, IdleMillis: idle, Deliveries: item.Deliveries})
		if len(out) == count {
			break
		}
	}
	return out, nil
}
