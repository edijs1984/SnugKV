package server

import (
	"encoding/binary"
	"errors"
	"math"
	"snugkv/internal/engine"
	"strconv"
)

const (
	streamItemFlagDeleted    = int64(1)
	streamItemFlagSameFields = int64(2)
)

func streamIDBytes(id engine.StreamID) []byte {
	out := make([]byte, 16)
	binary.BigEndian.PutUint64(out[:8], id.Millis)
	binary.BigEndian.PutUint64(out[8:], id.Sequence)
	return out
}

func parseStreamIDBytes(raw []byte) (engine.StreamID, error) {
	if len(raw) != 16 {
		return engine.StreamID{}, errors.New("ERR Bad data format")
	}
	return engine.StreamID{
		Millis: binary.BigEndian.Uint64(raw[:8]),
		Sequence: binary.BigEndian.Uint64(raw[8:]),
	}, nil
}

func listpackInt(value []byte) (int64, error) {
	n, ok := listpackCanonicalInt(value)
	if !ok {
		return 0, errors.New("ERR Bad data format")
	}
	return n, nil
}

func encodeStreamListpack(entries []engine.StreamEntry) ([]byte, engine.StreamID, error) {
	if len(entries) == 0 {
		return nil, engine.StreamID{}, errors.New("ERR empty STREAM node")
	}
	master := entries[0].ID
	masterFields := entries[0].Fields
	values := make([][]byte, 0, 8+len(entries)*8)
	values = append(values,
		[]byte(strconvI64(int64(len(entries)))),
		[]byte("0"),
		[]byte(strconvI64(int64(len(masterFields)))),
	)
	for _, field := range masterFields {
		values = append(values, field.Field)
	}
	values = append(values, []byte("0"))

	for _, entry := range entries {
		same := len(entry.Fields) == len(masterFields)
		if same {
			for i := range masterFields {
				if string(entry.Fields[i].Field) != string(masterFields[i].Field) {
					same = false
					break
				}
			}
		}
		flags := int64(0)
		if same {
			flags |= streamItemFlagSameFields
		}
		values = append(values,
			[]byte(strconvI64(flags)),
			[]byte(strconvU64(entry.ID.Millis-master.Millis)),
			[]byte(strconvU64(entry.ID.Sequence-master.Sequence)),
		)
		if !same {
			values = append(values, []byte(strconvI64(int64(len(entry.Fields)))))
		}
		for _, field := range entry.Fields {
			if !same {
				values = append(values, field.Field)
			}
			values = append(values, field.Value)
		}
		lpCount := int64(len(entry.Fields)) + 3
		if !same {
			lpCount += int64(len(entry.Fields)) + 1
		}
		values = append(values, []byte(strconvI64(lpCount)))
	}
	lp, err := encodeRedisListpack(values)
	return lp, master, err
}

func decodeStreamListpack(raw []byte, master engine.StreamID) ([]engine.StreamEntry, error) {
	values, err := decodeRedisListpack(raw)
	if err != nil || len(values) < 5 {
		return nil, errors.New("ERR Bad data format")
	}
	pos := 0
	liveCount, err := listpackInt(values[pos]); pos++
	if err != nil || liveCount < 0 {
		return nil, errors.New("ERR Bad data format")
	}
	deletedCount, err := listpackInt(values[pos]); pos++
	if err != nil || deletedCount < 0 {
		return nil, errors.New("ERR Bad data format")
	}
	masterFieldsCount, err := listpackInt(values[pos]); pos++
	if err != nil || masterFieldsCount < 0 || masterFieldsCount > int64(len(values)) {
		return nil, errors.New("ERR Bad data format")
	}
	masterFields := make([][]byte, 0, masterFieldsCount)
	for i := int64(0); i < masterFieldsCount; i++ {
		if pos >= len(values) {
			return nil, errors.New("ERR Bad data format")
		}
		masterFields = append(masterFields, values[pos])
		pos++
	}
	if pos >= len(values) {
		return nil, errors.New("ERR Bad data format")
	}
	terminator, err := listpackInt(values[pos]); pos++
	if err != nil || terminator != 0 {
		return nil, errors.New("ERR Bad data format")
	}

	total := liveCount + deletedCount
	entries := make([]engine.StreamEntry, 0, liveCount)
	for i := int64(0); i < total; i++ {
		if pos+3 > len(values) {
			return nil, errors.New("ERR Bad data format")
		}
		flags, err := listpackInt(values[pos]); pos++
		if err != nil {
			return nil, errors.New("ERR Bad data format")
		}
		msDelta, err := listpackInt(values[pos]); pos++
		if err != nil || msDelta < 0 {
			return nil, errors.New("ERR Bad data format")
		}
		seqDelta, err := listpackInt(values[pos]); pos++
		if err != nil || seqDelta < 0 {
			return nil, errors.New("ERR Bad data format")
		}
		if uint64(msDelta) > math.MaxUint64-master.Millis || uint64(seqDelta) > math.MaxUint64-master.Sequence {
			return nil, errors.New("ERR Bad data format")
		}
		id := engine.StreamID{
			Millis: master.Millis + uint64(msDelta),
			Sequence: master.Sequence + uint64(seqDelta),
		}

		var fields []engine.StreamField
		if flags&streamItemFlagSameFields != 0 {
			fields = make([]engine.StreamField, 0, len(masterFields))
			for _, name := range masterFields {
				if pos >= len(values) {
					return nil, errors.New("ERR Bad data format")
				}
				fields = append(fields, engine.StreamField{Field: append([]byte(nil), name...), Value: append([]byte(nil), values[pos]...)})
				pos++
			}
		} else {
			if pos >= len(values) {
				return nil, errors.New("ERR Bad data format")
			}
			count, err := listpackInt(values[pos]); pos++
			if err != nil || count < 0 || count > int64(len(values)) {
				return nil, errors.New("ERR Bad data format")
			}
			fields = make([]engine.StreamField, 0, count)
			for j := int64(0); j < count; j++ {
				if pos+2 > len(values) {
					return nil, errors.New("ERR Bad data format")
				}
				fields = append(fields, engine.StreamField{
					Field: append([]byte(nil), values[pos]...),
					Value: append([]byte(nil), values[pos+1]...),
				})
				pos += 2
			}
		}
		if pos >= len(values) {
			return nil, errors.New("ERR Bad data format")
		}
		lpCount, err := listpackInt(values[pos]); pos++
		if err != nil || lpCount <= 0 {
			return nil, errors.New("ERR Bad data format")
		}
		if flags&streamItemFlagDeleted == 0 {
			entries = append(entries, engine.StreamEntry{ID: id, Fields: fields})
		}
	}
	if pos != len(values) || int64(len(entries)) != liveCount {
		return nil, errors.New("ERR Bad data format")
	}
	return entries, nil
}

func strconvI64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func strconvU64(v uint64) string {
	return strconv.FormatUint(v, 10)
}

func appendRDBFixedI64(dst []byte, value int64) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(value))
	return append(dst, buf[:]...)
}

func readRDBFixedI64(data []byte, pos *int) (int64, error) {
	if *pos+8 > len(data) {
		return 0, errors.New("ERR Bad data format")
	}
	v := int64(binary.LittleEndian.Uint64(data[*pos : *pos+8]))
	*pos += 8
	return v, nil
}

func appendStreamGroupsRDB(out []byte, groups []engine.StreamSnapshotGroup) []byte {
	out = appendRDBLen(out, uint64(len(groups)))
	for _, group := range groups {
		out = appendRDBRawString(out, []byte(group.Name))
		out = appendRDBLen(out, group.LastDeliveredID.Millis)
		out = appendRDBLen(out, group.LastDeliveredID.Sequence)
		if group.EntriesRead < 0 {
			out = appendRDBLen(out, math.MaxUint64)
		} else {
			out = appendRDBLen(out, uint64(group.EntriesRead))
		}

		out = appendRDBLen(out, uint64(len(group.Pending)))
		for _, pending := range group.Pending {
			out = append(out, streamIDBytes(pending.ID)...)
			out = appendRDBFixedI64(out, pending.DeliveredAt)
			out = appendRDBLen(out, pending.Deliveries)
		}

		out = appendRDBLen(out, uint64(len(group.Consumers)))
		for _, consumer := range group.Consumers {
			out = appendRDBRawString(out, []byte(consumer.Name))
			out = appendRDBFixedI64(out, consumer.SeenAt)
			out = appendRDBFixedI64(out, consumer.ActiveAt)

			count := 0
			for _, pending := range group.Pending {
				if pending.Consumer == consumer.Name {
					count++
				}
			}
			out = appendRDBLen(out, uint64(count))
			for _, pending := range group.Pending {
				if pending.Consumer == consumer.Name {
					out = append(out, streamIDBytes(pending.ID)...)
				}
			}
		}
	}
	return out
}

func decodeStreamGroupsRDB(body []byte, pos *int) ([]engine.StreamSnapshotGroup, error) {
	groupCount, encoded, err := readRDBLen(body, pos)
	if err != nil || encoded || groupCount > uint64(len(body)) {
		return nil, errors.New("ERR Bad data format")
	}
	groups := make([]engine.StreamSnapshotGroup, 0, groupCount)
	for i := uint64(0); i < groupCount; i++ {
		name, err := decodeRDBString(body, pos)
		if err != nil {
			return nil, errors.New("ERR Bad data format")
		}
		lastMS, enc, err := readRDBLen(body, pos)
		if err != nil || enc {
			return nil, errors.New("ERR Bad data format")
		}
		lastSeq, enc, err := readRDBLen(body, pos)
		if err != nil || enc {
			return nil, errors.New("ERR Bad data format")
		}
		entriesReadRaw, enc, err := readRDBLen(body, pos)
		if err != nil || enc {
			return nil, errors.New("ERR Bad data format")
		}
		entriesRead := int64(-1)
		if entriesReadRaw != math.MaxUint64 {
			if entriesReadRaw > math.MaxInt64 {
				return nil, errors.New("ERR Bad data format")
			}
			entriesRead = int64(entriesReadRaw)
		}
		group := engine.StreamSnapshotGroup{
			Name: string(name),
			LastDeliveredID: engine.StreamID{Millis: lastMS, Sequence: lastSeq},
			EntriesRead: entriesRead,
		}

		pendingCount, enc, err := readRDBLen(body, pos)
		if err != nil || enc || pendingCount > uint64(len(body)) {
			return nil, errors.New("ERR Bad data format")
		}
		type rawPending struct {
			item engine.StreamSnapshotPending
		}
		pending := make([]rawPending, 0, pendingCount)
		for j := uint64(0); j < pendingCount; j++ {
			if *pos+16 > len(body) {
				return nil, errors.New("ERR Bad data format")
			}
			id, err := parseStreamIDBytes(body[*pos : *pos+16])
			*pos += 16
			if err != nil {
				return nil, err
			}
			delivered, err := readRDBFixedI64(body, pos)
			if err != nil {
				return nil, err
			}
			deliveries, enc, err := readRDBLen(body, pos)
			if err != nil || enc {
				return nil, errors.New("ERR Bad data format")
			}
			pending = append(pending, rawPending{item: engine.StreamSnapshotPending{
				ID: id, DeliveredAt: delivered, Deliveries: deliveries,
			}})
		}

		consumerCount, enc, err := readRDBLen(body, pos)
		if err != nil || enc || consumerCount > uint64(len(body)) {
			return nil, errors.New("ERR Bad data format")
		}
		for j := uint64(0); j < consumerCount; j++ {
			consumerName, err := decodeRDBString(body, pos)
			if err != nil {
				return nil, errors.New("ERR Bad data format")
			}
			seenAt, err := readRDBFixedI64(body, pos)
			if err != nil {
				return nil, err
			}
			activeAt, err := readRDBFixedI64(body, pos)
			if err != nil {
				return nil, err
			}
			// Redis persists -1 for a consumer that has never been active.
			// SnugKV uses zero internally for the same state.
			if activeAt < 0 {
				activeAt = 0
			}
			group.Consumers = append(group.Consumers, engine.StreamSnapshotConsumer{
				Name: string(consumerName), SeenAt: seenAt, ActiveAt: activeAt,
			})
			localCount, enc, err := readRDBLen(body, pos)
			if err != nil || enc || localCount > pendingCount {
				return nil, errors.New("ERR Bad data format")
			}
			for k := uint64(0); k < localCount; k++ {
				if *pos+16 > len(body) {
					return nil, errors.New("ERR Bad data format")
				}
				id, err := parseStreamIDBytes(body[*pos : *pos+16])
				*pos += 16
				if err != nil {
					return nil, err
				}
				found := false
				for p := range pending {
					if pending[p].item.ID == id {
						if pending[p].item.Consumer != "" {
							return nil, errors.New("ERR Bad data format")
						}
						pending[p].item.Consumer = string(consumerName)
						found = true
						break
					}
				}
				if !found {
					return nil, errors.New("ERR Bad data format")
				}
			}
		}
		for _, p := range pending {
			group.Pending = append(group.Pending, p.item)
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func encodeStreamDump(snapshot engine.StreamSnapshot) ([]byte, error) {
	out := []byte{keyRDBTypeStreamListpacks3}
	if len(snapshot.Entries) == 0 {
		out = appendRDBLen(out, 0)
	} else {
		lp, master, err := encodeStreamListpack(snapshot.Entries)
		if err != nil {
			return nil, err
		}
		out = appendRDBLen(out, 1)
		out = appendRDBRawString(out, streamIDBytes(master))
		out = appendRDBRawString(out, lp)
	}

	out = appendRDBLen(out, uint64(len(snapshot.Entries)))
	out = appendRDBLen(out, snapshot.LastID.Millis)
	out = appendRDBLen(out, snapshot.LastID.Sequence)
	if len(snapshot.Entries) > 0 {
		out = appendRDBLen(out, snapshot.Entries[0].ID.Millis)
		out = appendRDBLen(out, snapshot.Entries[0].ID.Sequence)
	} else {
		out = appendRDBLen(out, 0)
		out = appendRDBLen(out, 0)
	}
	out = appendRDBLen(out, snapshot.MaxDeletedID.Millis)
	out = appendRDBLen(out, snapshot.MaxDeletedID.Sequence)
	out = appendRDBLen(out, snapshot.EntriesAdded)
	out = appendStreamGroupsRDB(out, snapshot.Groups)
	return appendKeyDumpTrailer(out)
}

func decodeStreamDump(body []byte, pos *int) (engine.StreamSnapshot, error) {
	nodeCount, enc, err := readRDBLen(body, pos)
	if err != nil || enc || nodeCount > uint64(len(body)) {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	snapshot := engine.StreamSnapshot{}
	for i := uint64(0); i < nodeCount; i++ {
		keyRaw, err := decodeRDBString(body, pos)
		if err != nil {
			return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
		}
		master, err := parseStreamIDBytes(keyRaw)
		if err != nil {
			return engine.StreamSnapshot{}, err
		}
		lpRaw, err := decodeRDBString(body, pos)
		if err != nil {
			return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
		}
		entries, err := decodeStreamListpack(lpRaw, master)
		if err != nil {
			return engine.StreamSnapshot{}, err
		}
		snapshot.Entries = append(snapshot.Entries, entries...)
	}

	length, enc, err := readRDBLen(body, pos)
	if err != nil || enc {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	lastMS, enc, err := readRDBLen(body, pos)
	if err != nil || enc {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	lastSeq, enc, err := readRDBLen(body, pos)
	if err != nil || enc {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	firstMS, enc, err := readRDBLen(body, pos)
	if err != nil || enc {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	firstSeq, enc, err := readRDBLen(body, pos)
	if err != nil || enc {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	maxDelMS, enc, err := readRDBLen(body, pos)
	if err != nil || enc {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	maxDelSeq, enc, err := readRDBLen(body, pos)
	if err != nil || enc {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	entriesAdded, enc, err := readRDBLen(body, pos)
	if err != nil || enc {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	groups, err := decodeStreamGroupsRDB(body, pos)
	if err != nil {
		return engine.StreamSnapshot{}, err
	}
	if uint64(len(snapshot.Entries)) != length {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	if len(snapshot.Entries) > 0 && snapshot.Entries[0].ID != (engine.StreamID{Millis:firstMS, Sequence:firstSeq}) {
		return engine.StreamSnapshot{}, errors.New("ERR Bad data format")
	}
	snapshot.LastID = engine.StreamID{Millis:lastMS, Sequence:lastSeq}
	snapshot.MaxDeletedID = engine.StreamID{Millis:maxDelMS, Sequence:maxDelSeq}
	snapshot.EntriesAdded = entriesAdded
	snapshot.Groups = groups
	return snapshot, nil
}
