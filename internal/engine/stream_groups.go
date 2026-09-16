package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func streamGroupIndex(groups []streamGroup, name string) int {
	for i := range groups {
		if groups[i].Name == name {
			return i
		}
	}
	return -1
}

func streamConsumerIndex(consumers []streamConsumer, name string) int {
	for i := range consumers {
		if consumers[i].Name == name {
			return i
		}
	}
	return -1
}

func streamNoGroup(group, key string) error {
	return fmt.Errorf("NOGROUP No such consumer group '%s' for key name '%s'", group, key)
}

func parseStreamGroupID(spec string, last StreamID) (StreamID, error) {
	if spec == "$" {
		return last, nil
	}
	if strings.Contains(spec, "-") {
		return parseStreamIDPair(spec)
	}
	ms, err := strconv.ParseUint(spec, 10, 64)
	if err != nil {
		return StreamID{}, errors.New("ERR Invalid stream ID specified as stream command argument")
	}
	return StreamID{Millis: ms}, nil
}

func (s *Store) publishStreamStateLocked(sh *shard, key string, old entry, state packedStream) error {
	packed, err := encodePackedStream(state)
	if err != nil {
		return err
	}
	updated := streamPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, old)
	return s.publish(sh, key, updated)
}

func (s *Store) StreamGroupCreate(key, group, idSpec string, mkstream bool, entriesRead int64) error {
	if entriesRead < -1 {
		return errors.New("ERR entries-read must be valid")
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
	if !exists && !mkstream {
		return errors.New("ERR The XGROUP subcommand requires the key to exist. Note that for CREATE you may want to use the MKSTREAM option to create an empty stream automatically.")
	}

	state := packedStream{}
	if exists {
		if old.valueType != TypeStream {
			return streamWrongType()
		}
		var err error
		state, err = s.streamStateFromEntry(sh, old)
		if err != nil {
			return err
		}
	}
	if streamGroupIndex(state.Groups, group) >= 0 {
		return errors.New("BUSYGROUP Consumer Group name already exists")
	}
	id, err := parseStreamGroupID(idSpec, state.LastID)
	if err != nil {
		return err
	}
	state.Groups = append(state.Groups, streamGroup{Name: group, LastDeliveredID: id, EntriesRead: entriesRead})

	if !exists {
		packed, err := encodePackedStream(state)
		if err != nil {
			return err
		}
		return s.publish(sh, key, streamPreparedEntry(packed))
	}
	return s.publishStreamStateLocked(sh, key, old, state)
}

func (s *Store) StreamGroupDestroy(key, group string) (int64, error) {
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
	index := streamGroupIndex(state.Groups, group)
	if index < 0 {
		return 0, nil
	}
	copy(state.Groups[index:], state.Groups[index+1:])
	state.Groups = state.Groups[:len(state.Groups)-1]
	if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *Store) StreamGroupSetID(key, group, idSpec string, entriesRead *int64) error {
	if entriesRead != nil && *entriesRead < -1 {
		return errors.New("ERR entries-read must be valid")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return streamNoGroup(group, key)
	}
	if e.valueType != TypeStream {
		return streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return err
	}
	index := streamGroupIndex(state.Groups, group)
	if index < 0 {
		return streamNoGroup(group, key)
	}
	id, err := parseStreamGroupID(idSpec, state.LastID)
	if err != nil {
		return err
	}
	state.Groups[index].LastDeliveredID = id
	if entriesRead != nil {
		state.Groups[index].EntriesRead = *entriesRead
	}
	return s.publishStreamStateLocked(sh, key, e, state)
}

func (s *Store) StreamGroupCreateConsumer(key, group, consumer string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return 0, streamNoGroup(group, key)
	}
	if e.valueType != TypeStream {
		return 0, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	groupIndex := streamGroupIndex(state.Groups, group)
	if groupIndex < 0 {
		return 0, streamNoGroup(group, key)
	}
	if streamConsumerIndex(state.Groups[groupIndex].Consumers, consumer) >= 0 {
		return 0, nil
	}
	nowMS := s.now().UnixMilli()
	if nowMS < 0 {
		nowMS = 0
	}
	state.Groups[groupIndex].Consumers = append(state.Groups[groupIndex].Consumers, streamConsumer{Name: consumer, SeenAt: nowMS})
	if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *Store) StreamGroupDeleteConsumer(key, group, consumer string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return 0, streamNoGroup(group, key)
	}
	if e.valueType != TypeStream {
		return 0, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	groupIndex := streamGroupIndex(state.Groups, group)
	if groupIndex < 0 {
		return 0, streamNoGroup(group, key)
	}
	groupState := &state.Groups[groupIndex]
	consumerIndex := streamConsumerIndex(groupState.Consumers, consumer)
	if consumerIndex < 0 {
		return 0, nil
	}
	var orphanedPending int64
	for i := range groupState.Pending {
		if groupState.Pending[i].Consumer == consumer {
			orphanedPending++
			groupState.Pending[i].Consumer = ""
		}
	}
	copy(groupState.Consumers[consumerIndex:], groupState.Consumers[consumerIndex+1:])
	groupState.Consumers = groupState.Consumers[:len(groupState.Consumers)-1]
	if err := s.publishStreamStateLocked(sh, key, e, state); err != nil {
		return 0, err
	}
	return orphanedPending, nil
}
