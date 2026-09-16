package engine

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

type streamConsumer struct {
	Name     string
	SeenAt   int64
	ActiveAt int64
}

type streamPending struct {
	ID          StreamID
	Consumer    string
	DeliveredAt int64
	Deliveries  uint64
}

type streamGroup struct {
	Name            string
	LastDeliveredID StreamID
	EntriesRead     int64
	Consumers       []streamConsumer
	Pending         []streamPending
}

func appendStreamString(dst []byte, value string) []byte {
	dst = appendStreamUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func readStreamString(data []byte, offset *int) (string, error) {
	n, err := readStreamUvarint(data, offset)
	if err != nil || n > uint64(len(data)-*offset) {
		return "", errors.New("invalid packed stream")
	}
	end := *offset + int(n)
	value := string(data[*offset:end])
	*offset = end
	return value, nil
}

func appendStreamID(dst []byte, id StreamID) []byte {
	var fixed [16]byte
	binary.BigEndian.PutUint64(fixed[:8], id.Millis)
	binary.BigEndian.PutUint64(fixed[8:], id.Sequence)
	return append(dst, fixed[:]...)
}

func readStreamID(data []byte, offset *int) (StreamID, error) {
	if *offset+16 > len(data) {
		return StreamID{}, errors.New("invalid packed stream")
	}
	id := StreamID{
		Millis:   binary.BigEndian.Uint64(data[*offset : *offset+8]),
		Sequence: binary.BigEndian.Uint64(data[*offset+8 : *offset+16]),
	}
	*offset += 16
	return id, nil
}

func encodeEntriesRead(value int64) uint64 {
	if value < 0 {
		return math.MaxUint64
	}
	return uint64(value)
}

func decodeEntriesRead(value uint64) (int64, error) {
	if value == math.MaxUint64 {
		return -1, nil
	}
	if value > math.MaxInt64 {
		return 0, errors.New("invalid packed stream")
	}
	return int64(value), nil
}

func appendStreamGroups(dst []byte, groups []streamGroup) ([]byte, error) {
	dst = appendStreamUvarint(dst, uint64(len(groups)))
	seenGroups := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if _, exists := seenGroups[group.Name]; exists {
			return nil, errors.New("ERR duplicate stream consumer group")
		}
		seenGroups[group.Name] = struct{}{}
		dst = appendStreamString(dst, group.Name)
		dst = appendStreamID(dst, group.LastDeliveredID)
		dst = appendStreamUvarint(dst, encodeEntriesRead(group.EntriesRead))
		dst = appendStreamUvarint(dst, uint64(len(group.Consumers)))
		seenConsumers := make(map[string]struct{}, len(group.Consumers))
		for _, consumer := range group.Consumers {
			if _, exists := seenConsumers[consumer.Name]; exists || consumer.SeenAt < 0 || consumer.ActiveAt < 0 {
				return nil, errors.New("ERR invalid stream consumer metadata")
			}
			seenConsumers[consumer.Name] = struct{}{}
			dst = appendStreamString(dst, consumer.Name)
			dst = appendStreamUvarint(dst, uint64(consumer.SeenAt))
			dst = appendStreamUvarint(dst, uint64(consumer.ActiveAt))
		}
		pending := append([]streamPending(nil), group.Pending...)
		sort.Slice(pending, func(i, j int) bool { return pending[i].ID.less(pending[j].ID) })
		dst = appendStreamUvarint(dst, uint64(len(pending)))
		var previous StreamID
		for i, item := range pending {
			if i > 0 && !previous.less(item.ID) || item.DeliveredAt < 0 {
				return nil, errors.New("ERR invalid stream pending metadata")
			}
			if item.Consumer != "" {
				if _, exists := seenConsumers[item.Consumer]; !exists {
					return nil, errors.New("ERR stream pending consumer does not exist")
				}
			}
			dst = appendStreamID(dst, item.ID)
			dst = appendStreamString(dst, item.Consumer)
			dst = appendStreamUvarint(dst, uint64(item.DeliveredAt))
			dst = appendStreamUvarint(dst, item.Deliveries)
			previous = item.ID
		}
		if len(dst) > maxPackedStreamBytes {
			return nil, errors.New("ERR stream exceeds 32 MiB limit")
		}
	}
	return dst, nil
}

func readStreamGroups(data []byte, offset *int, version byte) ([]streamGroup, error) {
	count, err := readStreamUvarint(data, offset)
	if err != nil || count > uint64(maxPackedStreamBytes) {
		return nil, errors.New("invalid packed stream")
	}
	groups := make([]streamGroup, 0, int(count))
	seenGroups := make(map[string]struct{}, int(count))
	for i := uint64(0); i < count; i++ {
		name, err := readStreamString(data, offset)
		if err != nil {
			return nil, err
		}
		if _, exists := seenGroups[name]; exists {
			return nil, errors.New("invalid packed stream")
		}
		seenGroups[name] = struct{}{}
		lastID, err := readStreamID(data, offset)
		if err != nil {
			return nil, err
		}
		entriesReadRaw, err := readStreamUvarint(data, offset)
		if err != nil {
			return nil, err
		}
		entriesRead, err := decodeEntriesRead(entriesReadRaw)
		if err != nil {
			return nil, err
		}
		consumerCount, err := readStreamUvarint(data, offset)
		if err != nil || consumerCount > uint64(maxPackedStreamBytes) {
			return nil, errors.New("invalid packed stream")
		}
		group := streamGroup{Name: name, LastDeliveredID: lastID, EntriesRead: entriesRead}
		group.Consumers = make([]streamConsumer, 0, int(consumerCount))
		seenConsumers := make(map[string]struct{}, int(consumerCount))
		for j := uint64(0); j < consumerCount; j++ {
			consumerName, err := readStreamString(data, offset)
			if err != nil {
				return nil, err
			}
			if _, exists := seenConsumers[consumerName]; exists {
				return nil, errors.New("invalid packed stream")
			}
			seenConsumers[consumerName] = struct{}{}
			seenAt, err := readStreamUvarint(data, offset)
			if err != nil || seenAt > math.MaxInt64 {
				return nil, errors.New("invalid packed stream")
			}
			activeAt := seenAt
			if version >= packedStreamHeaderV3[2] {
				activeAt, err = readStreamUvarint(data, offset)
				if err != nil || activeAt > math.MaxInt64 {
					return nil, errors.New("invalid packed stream")
				}
			}
			group.Consumers = append(group.Consumers, streamConsumer{Name: consumerName, SeenAt: int64(seenAt), ActiveAt: int64(activeAt)})
		}
		pendingCount, err := readStreamUvarint(data, offset)
		if err != nil || pendingCount > uint64(maxPackedStreamBytes) {
			return nil, errors.New("invalid packed stream")
		}
		group.Pending = make([]streamPending, 0, int(pendingCount))
		var previous StreamID
		for j := uint64(0); j < pendingCount; j++ {
			id, err := readStreamID(data, offset)
			if err != nil || j > 0 && !previous.less(id) {
				return nil, errors.New("invalid packed stream")
			}
			consumer, err := readStreamString(data, offset)
			if err != nil {
				return nil, err
			}
			if consumer != "" {
				if _, exists := seenConsumers[consumer]; !exists {
					return nil, errors.New("invalid packed stream")
				}
			}
			deliveredAt, err := readStreamUvarint(data, offset)
			if err != nil || deliveredAt > math.MaxInt64 {
				return nil, errors.New("invalid packed stream")
			}
			deliveries, err := readStreamUvarint(data, offset)
			if err != nil {
				return nil, errors.New("invalid packed stream")
			}
			group.Pending = append(group.Pending, streamPending{ID: id, Consumer: consumer, DeliveredAt: int64(deliveredAt), Deliveries: deliveries})
			previous = id
		}
		groups = append(groups, group)
	}
	return groups, nil
}
