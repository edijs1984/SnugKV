package engine

import (
	"errors"
	"sort"
)

type StreamInfoPending struct {
	ID          StreamID
	Consumer    string
	DeliveredAt int64
	Deliveries  uint64
}

type StreamInfoConsumer struct {
	Name           string
	Pending        int64
	IdleMillis     int64
	InactiveMillis int64
	SeenTime       int64
	ActiveTime     int64
	PendingEntries []StreamInfoPending
}

type StreamInfoGroup struct {
	Name            string
	Consumers       int64
	Pending         int64
	LastDeliveredID StreamID
	EntriesRead     *int64
	Lag             *int64
	PendingEntries  []StreamInfoPending
	ConsumerInfos   []StreamInfoConsumer
}

type StreamInfoResult struct {
	Length               int64
	RadixTreeKeys        int64
	RadixTreeNodes       int64
	Groups               int64
	LastGeneratedID      StreamID
	MaxDeletedEntryID    StreamID
	EntriesAdded         int64
	RecordedFirstEntryID StreamID
	FirstEntry           *StreamEntry
	LastEntry            *StreamEntry
	Entries               []StreamEntry
	GroupInfos            []StreamInfoGroup
}

func cloneStreamEntryPtr(entry StreamEntry) *StreamEntry {
	clone := cloneStreamEntry(entry)
	return &clone
}

func cappedStreamInfoPending(items []streamPending, count int) []StreamInfoPending {
	pending := append([]streamPending(nil), items...)
	sort.Slice(pending, func(i, j int) bool { return pending[i].ID.less(pending[j].ID) })
	if count > 0 && len(pending) > count {
		pending = pending[:count]
	}
	out := make([]StreamInfoPending, 0, len(pending))
	for _, item := range pending {
		out = append(out, StreamInfoPending{
			ID:          item.ID,
			Consumer:    item.Consumer,
			DeliveredAt: item.DeliveredAt,
			Deliveries:  item.Deliveries,
		})
	}
	return out
}

func streamInfoConsumer(group streamGroup, consumer streamConsumer, nowMS int64, count int) StreamInfoConsumer {
	pending := make([]streamPending, 0)
	for _, item := range group.Pending {
		if item.Consumer == consumer.Name {
			pending = append(pending, item)
		}
	}
	idle := nowMS - consumer.SeenAt
	if idle < 0 {
		idle = 0
	}
	// Packed stream v2 stores one consumer interaction timestamp. Until the
	// lifetime-metadata format lands, expose the same timestamp for Redis 7.2's
	// inactive field rather than inventing a second value.
	return StreamInfoConsumer{
		Name:           consumer.Name,
		Pending:        int64(len(pending)),
		IdleMillis:     idle,
		InactiveMillis: idle,
		SeenTime:       consumer.SeenAt,
		ActiveTime:     consumer.SeenAt,
		PendingEntries: cappedStreamInfoPending(pending, count),
	}
}

func streamInfoGroup(state packedStream, group streamGroup, nowMS int64, count int, full bool) StreamInfoGroup {
	result := StreamInfoGroup{
		Name:            group.Name,
		Consumers:       int64(len(group.Consumers)),
		Pending:         int64(len(group.Pending)),
		LastDeliveredID: group.LastDeliveredID,
	}
	if group.EntriesRead >= 0 {
		entriesRead := group.EntriesRead
		result.EntriesRead = &entriesRead
		// With the current packed format, the exact lifetime entries-added
		// counter is not persisted yet. A caught-up group is always known to
		// have zero lag; otherwise use the live-entry count only when the read
		// counter is within that range. The format-v3 follow-up will make this
		// exact across deletion and trimming histories.
		if group.LastDeliveredID.equal(state.LastID) {
			lag := int64(0)
			result.Lag = &lag
		} else if entriesRead <= int64(len(state.Entries)) {
			lag := int64(len(state.Entries)) - entriesRead
			result.Lag = &lag
		}
	}
	if !full {
		return result
	}
	result.PendingEntries = cappedStreamInfoPending(group.Pending, count)
	result.ConsumerInfos = make([]StreamInfoConsumer, 0, len(group.Consumers))
	for _, consumer := range group.Consumers {
		result.ConsumerInfos = append(result.ConsumerInfos, streamInfoConsumer(group, consumer, nowMS, count))
	}
	return result
}

func (s *Store) streamInfoState(key string) (packedStream, int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	now := s.now()
	if !ok || sh.expired(key, e, now) {
		return packedStream{}, 0, errors.New("ERR no such key")
	}
	if e.valueType != TypeStream {
		return packedStream{}, 0, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return packedStream{}, 0, err
	}
	return state, streamNowMillis(now), nil
}

func (s *Store) StreamInfo(key string, full bool, count int) (StreamInfoResult, error) {
	if count < 0 {
		return StreamInfoResult{}, errors.New("ERR value is not an integer or out of range")
	}
	state, nowMS, err := s.streamInfoState(key)
	if err != nil {
		return StreamInfoResult{}, err
	}
	result := StreamInfoResult{
		Length:            int64(len(state.Entries)),
		Groups:            int64(len(state.Groups)),
		LastGeneratedID:   state.LastID,
		EntriesAdded:      int64(len(state.Entries)),
		MaxDeletedEntryID: StreamID{},
	}
	if len(state.Entries) > 0 {
		result.RadixTreeKeys = 1
		result.RadixTreeNodes = 1
		result.RecordedFirstEntryID = state.Entries[0].ID
		result.FirstEntry = cloneStreamEntryPtr(state.Entries[0])
		result.LastEntry = cloneStreamEntryPtr(state.Entries[len(state.Entries)-1])
	}
	if full {
		entries := state.Entries
		if count > 0 && len(entries) > count {
			entries = entries[:count]
		}
		result.Entries = make([]StreamEntry, 0, len(entries))
		for _, entry := range entries {
			result.Entries = append(result.Entries, cloneStreamEntry(entry))
		}
		result.GroupInfos = make([]StreamInfoGroup, 0, len(state.Groups))
		for _, group := range state.Groups {
			result.GroupInfos = append(result.GroupInfos, streamInfoGroup(state, group, nowMS, count, true))
		}
	}
	return result, nil
}

func (s *Store) StreamGroupsInfo(key string) ([]StreamInfoGroup, error) {
	state, nowMS, err := s.streamInfoState(key)
	if err != nil {
		return nil, err
	}
	out := make([]StreamInfoGroup, 0, len(state.Groups))
	for _, group := range state.Groups {
		out = append(out, streamInfoGroup(state, group, nowMS, 0, false))
	}
	return out, nil
}

func (s *Store) StreamConsumersInfo(key, groupName string) ([]StreamInfoConsumer, error) {
	state, nowMS, err := s.streamInfoState(key)
	if err != nil {
		return nil, err
	}
	gi := streamGroupIndex(state.Groups, groupName)
	if gi < 0 {
		return nil, streamNoGroup(groupName, key)
	}
	group := state.Groups[gi]
	out := make([]StreamInfoConsumer, 0, len(group.Consumers))
	for _, consumer := range group.Consumers {
		out = append(out, streamInfoConsumer(group, consumer, nowMS, 0))
	}
	return out, nil
}
