package engine

import (
	"errors"
	"sort"
	"time"
)

type StreamClaimOptions struct {
	IdleMillis *int64
	TimeMillis *int64
	RetryCount *uint64
	Force      bool
	JustID     bool
	LastID     *StreamID
}

type StreamAutoClaimResult struct {
	Next       StreamID
	Entries    []StreamEntry
	IDs        []StreamID
	DeletedIDs []StreamID
}

func streamPendingIndex(pending []streamPending, id StreamID) int {
	for i := range pending {
		if pending[i].ID.equal(id) {
			return i
		}
	}
	return -1
}

func streamNowMillis(now time.Time) int64 {
	ms := now.UnixMilli()
	if ms < 0 {
		return 0
	}
	return ms
}

func streamPendingIdle(nowMS int64, pending streamPending) int64 {
	idle := nowMS - pending.DeliveredAt
	if idle < 0 {
		return 0
	}
	return idle
}

func claimDeliveredAt(nowMS int64, options StreamClaimOptions) (int64, error) {
	if options.IdleMillis != nil && options.TimeMillis != nil {
		return 0, errors.New("ERR IDLE and TIME options are mutually exclusive")
	}
	if options.TimeMillis != nil {
		if *options.TimeMillis < 0 {
			return 0, errors.New("ERR invalid TIME option")
		}
		return *options.TimeMillis, nil
	}
	if options.IdleMillis != nil {
		if *options.IdleMillis < 0 {
			return 0, errors.New("ERR invalid IDLE option")
		}
		if *options.IdleMillis >= nowMS {
			return 0, nil
		}
		return nowMS - *options.IdleMillis, nil
	}
	return nowMS, nil
}

func (s *Store) StreamGroupClaim(key, groupName, consumer string, minIdle time.Duration, ids []StreamID, options StreamClaimOptions) ([]StreamEntry, []StreamID, error) {
	if minIdle < 0 {
		return nil, nil, errors.New("ERR min-idle-time must be non-negative")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return nil, nil, streamNoGroup(groupName, key)
	}
	if e.valueType != TypeStream {
		return nil, nil, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return nil, nil, err
	}
	gi := streamGroupIndex(state.Groups, groupName)
	if gi < 0 {
		return nil, nil, streamNoGroup(groupName, key)
	}
	group := &state.Groups[gi]
	nowMS := streamNowMillis(s.now())
	deliveredAt, err := claimDeliveredAt(nowMS, options)
	if err != nil {
		return nil, nil, err
	}
	minIdleMS := minIdle.Milliseconds()
	changed := false
	consumerTouched := false
	entries := make([]StreamEntry, 0, len(ids))
	claimedIDs := make([]StreamID, 0, len(ids))

	for _, id := range ids {
		pi := streamPendingIndex(group.Pending, id)
		entry, exists := streamEntryByID(state.Entries, id)
		if pi < 0 {
			if !options.Force || !exists {
				continue
			}
			deliveries := uint64(1)
			if options.RetryCount != nil {
				deliveries = *options.RetryCount
			}
			group.Pending = append(group.Pending, streamPending{ID: id, Consumer: consumer, DeliveredAt: deliveredAt, Deliveries: deliveries})
			pi = len(group.Pending) - 1
			changed = true
		} else {
			if !exists {
				group.Pending = append(group.Pending[:pi], group.Pending[pi+1:]...)
				changed = true
				continue
			}
			pending := &group.Pending[pi]
			if streamPendingIdle(nowMS, *pending) < minIdleMS {
				continue
			}
			pending.Consumer = consumer
			pending.DeliveredAt = deliveredAt
			if options.RetryCount != nil {
				pending.Deliveries = *options.RetryCount
			} else if !options.JustID {
				pending.Deliveries++
			}
			changed = true
		}
		if !consumerTouched {
			ensureStreamConsumer(group, consumer, nowMS)
			consumerTouched = true
		}
		claimedIDs = append(claimedIDs, id)
		entries = append(entries, entry)
	}
	if options.LastID != nil {
		group.LastDeliveredID = *options.LastID
		changed = true
	}
	if changed {
		sort.Slice(group.Pending, func(i, j int) bool { return group.Pending[i].ID.less(group.Pending[j].ID) })
		if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
			return nil, nil, err
		}
	}
	return entries, claimedIDs, nil
}

func (s *Store) StreamGroupAutoClaim(key, groupName, consumer string, minIdle time.Duration, start StreamID, count int, justID bool) (StreamAutoClaimResult, error) {
	if minIdle < 0 || count <= 0 {
		return StreamAutoClaimResult{}, errors.New("ERR value is not an integer or out of range")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return StreamAutoClaimResult{}, streamNoGroup(groupName, key)
	}
	if e.valueType != TypeStream {
		return StreamAutoClaimResult{}, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return StreamAutoClaimResult{}, err
	}
	gi := streamGroupIndex(state.Groups, groupName)
	if gi < 0 {
		return StreamAutoClaimResult{}, streamNoGroup(groupName, key)
	}
	group := &state.Groups[gi]
	sort.Slice(group.Pending, func(i, j int) bool { return group.Pending[i].ID.less(group.Pending[j].ID) })
	nowMS := streamNowMillis(s.now())
	minIdleMS := minIdle.Milliseconds()
	result := StreamAutoClaimResult{}
	changed := false
	consumerTouched := false

	i := sort.Search(len(group.Pending), func(i int) bool { return !group.Pending[i].ID.less(start) })
	scanLimit := count * 10
	if count > int(^uint(0)>>1)/10 {
		scanLimit = int(^uint(0) >> 1)
	}
	scanned := 0
	claimed := 0
	for i < len(group.Pending) && scanned < scanLimit && claimed < count {
		scanned++
		pending := &group.Pending[i]
		entry, exists := streamEntryByID(state.Entries, pending.ID)
		if !exists {
			result.DeletedIDs = append(result.DeletedIDs, pending.ID)
			group.Pending = append(group.Pending[:i], group.Pending[i+1:]...)
			changed = true
			continue
		}
		if streamPendingIdle(nowMS, *pending) >= minIdleMS {
			pending.Consumer = consumer
			pending.DeliveredAt = nowMS
			if !justID {
				pending.Deliveries++
			}
			if !consumerTouched {
				ensureStreamConsumer(group, consumer, nowMS)
				consumerTouched = true
			}
			result.IDs = append(result.IDs, pending.ID)
			result.Entries = append(result.Entries, entry)
			changed = true
			claimed++
		}
		i++
	}
	if i < len(group.Pending) {
		result.Next = group.Pending[i].ID
	}
	if changed {
		if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
			return StreamAutoClaimResult{}, err
		}
	}
	return result, nil
}
