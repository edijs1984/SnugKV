package engine

import "errors"

type StreamSnapshotConsumer struct {
	Name     string
	SeenAt   int64
	ActiveAt int64
}

type StreamSnapshotPending struct {
	ID          StreamID
	Consumer    string
	DeliveredAt int64
	Deliveries  uint64
}

type StreamSnapshotGroup struct {
	Name            string
	LastDeliveredID StreamID
	EntriesRead     int64
	Consumers       []StreamSnapshotConsumer
	Pending         []StreamSnapshotPending
}

type StreamSnapshot struct {
	LastID       StreamID
	EntriesAdded uint64
	MaxDeletedID StreamID
	Entries      []StreamEntry
	Groups       []StreamSnapshotGroup
}

func cloneStreamSnapshot(state packedStream) StreamSnapshot {
	out := StreamSnapshot{
		LastID:       state.LastID,
		EntriesAdded: state.EntriesAdded,
		MaxDeletedID: state.MaxDeletedID,
		Entries:      make([]StreamEntry, 0, len(state.Entries)),
		Groups:       make([]StreamSnapshotGroup, 0, len(state.Groups)),
	}
	for _, entry := range state.Entries {
		out.Entries = append(out.Entries, cloneStreamEntry(entry))
	}
	for _, group := range state.Groups {
		g := StreamSnapshotGroup{
			Name:            group.Name,
			LastDeliveredID: group.LastDeliveredID,
			EntriesRead:     group.EntriesRead,
			Consumers:       make([]StreamSnapshotConsumer, 0, len(group.Consumers)),
			Pending:         make([]StreamSnapshotPending, 0, len(group.Pending)),
		}
		for _, consumer := range group.Consumers {
			g.Consumers = append(g.Consumers, StreamSnapshotConsumer{
				Name: consumer.Name, SeenAt: consumer.SeenAt, ActiveAt: consumer.ActiveAt,
			})
		}
		for _, pending := range group.Pending {
			g.Pending = append(g.Pending, StreamSnapshotPending{
				ID: pending.ID, Consumer: pending.Consumer,
				DeliveredAt: pending.DeliveredAt, Deliveries: pending.Deliveries,
			})
		}
		out.Groups = append(out.Groups, g)
	}
	return out
}

func packedStreamFromSnapshot(snapshot StreamSnapshot) (packedStream, error) {
	state := packedStream{
		LastID:       snapshot.LastID,
		EntriesAdded: snapshot.EntriesAdded,
		MaxDeletedID: snapshot.MaxDeletedID,
		Entries:      make([]StreamEntry, 0, len(snapshot.Entries)),
		Groups:       make([]streamGroup, 0, len(snapshot.Groups)),
	}
	for _, entry := range snapshot.Entries {
		state.Entries = append(state.Entries, cloneStreamEntry(entry))
	}
	for _, group := range snapshot.Groups {
		g := streamGroup{
			Name:            group.Name,
			LastDeliveredID: group.LastDeliveredID,
			EntriesRead:     group.EntriesRead,
			Consumers:       make([]streamConsumer, 0, len(group.Consumers)),
			Pending:         make([]streamPending, 0, len(group.Pending)),
		}
		for _, consumer := range group.Consumers {
			g.Consumers = append(g.Consumers, streamConsumer{
				Name: consumer.Name, SeenAt: consumer.SeenAt, ActiveAt: consumer.ActiveAt,
			})
		}
		for _, pending := range group.Pending {
			g.Pending = append(g.Pending, streamPending{
				ID: pending.ID, Consumer: pending.Consumer,
				DeliveredAt: pending.DeliveredAt, Deliveries: pending.Deliveries,
			})
		}
		state.Groups = append(state.Groups, g)
	}
	if _, err := encodePackedStream(state); err != nil {
		return packedStream{}, err
	}
	return state, nil
}

func EncodeStreamSnapshot(snapshot StreamSnapshot) ([]byte, error) {
	state, err := packedStreamFromSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	return encodePackedStream(state)
}

func (s *Store) StreamSnapshot(key string) (StreamSnapshot, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return StreamSnapshot{}, false, nil
	}
	if e.valueType != TypeStream {
		return StreamSnapshot{}, false, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return StreamSnapshot{}, false, err
	}
	return cloneStreamSnapshot(state), true, nil
}

func StreamSnapshotRecordValue(snapshot StreamSnapshot) ([]byte, error) {
	value, err := EncodeStreamSnapshot(snapshot)
	if err != nil {
		return nil, errors.New("ERR invalid stream snapshot")
	}
	return value, nil
}
