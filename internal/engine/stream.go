package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"
)

const maxPackedStreamBytes = 32 << 20

var packedStreamHeader = [...]byte{'S', 'X', 1}

type StreamID struct {
	Millis   uint64
	Sequence uint64
}

func (id StreamID) String() string {
	return strconv.FormatUint(id.Millis, 10) + "-" + strconv.FormatUint(id.Sequence, 10)
}

func (id StreamID) less(other StreamID) bool {
	return id.Millis < other.Millis || id.Millis == other.Millis && id.Sequence < other.Sequence
}

func (id StreamID) equal(other StreamID) bool {
	return id.Millis == other.Millis && id.Sequence == other.Sequence
}

type StreamField struct {
	Field []byte
	Value []byte
}

type StreamEntry struct {
	ID     StreamID
	Fields []StreamField
}

type packedStream struct {
	LastID  StreamID
	Entries []StreamEntry
}

type StreamAddOptions struct {
	NoMkStream bool
	MaxLen     int
	HasMaxLen  bool
}

type StreamRangeBound struct {
	ID        StreamID
	Exclusive bool
}

func streamWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func appendStreamUvarint(dst []byte, value uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	return append(dst, buf[:n]...)
}

func readStreamUvarint(data []byte, offset *int) (uint64, error) {
	if *offset >= len(data) {
		return 0, errors.New("invalid packed stream")
	}
	value, n := binary.Uvarint(data[*offset:])
	if n <= 0 {
		return 0, errors.New("invalid packed stream")
	}
	*offset += n
	return value, nil
}

func encodePackedStream(state packedStream) ([]byte, error) {
	capacity := len(packedStreamHeader) + 16 + binary.MaxVarintLen64
	for _, item := range state.Entries {
		capacity += 16 + binary.MaxVarintLen64
		if len(item.Fields) == 0 {
			return nil, errors.New("ERR stream entry requires at least one field-value pair")
		}
		for _, field := range item.Fields {
			capacity += len(field.Field) + len(field.Value) + 2*binary.MaxVarintLen64
			if capacity > maxPackedStreamBytes {
				return nil, errors.New("ERR stream exceeds 32 MiB limit")
			}
		}
	}
	out := make([]byte, 0, capacity)
	out = append(out, packedStreamHeader[:]...)
	var fixed [16]byte
	binary.BigEndian.PutUint64(fixed[:8], state.LastID.Millis)
	binary.BigEndian.PutUint64(fixed[8:], state.LastID.Sequence)
	out = append(out, fixed[:]...)
	out = appendStreamUvarint(out, uint64(len(state.Entries)))
	var previous StreamID
	for i, item := range state.Entries {
		if i > 0 && !previous.less(item.ID) {
			return nil, errors.New("ERR stream entries are not ordered")
		}
		if state.LastID.less(item.ID) {
			return nil, errors.New("ERR stream last-generated ID precedes an entry")
		}
		binary.BigEndian.PutUint64(fixed[:8], item.ID.Millis)
		binary.BigEndian.PutUint64(fixed[8:], item.ID.Sequence)
		out = append(out, fixed[:]...)
		out = appendStreamUvarint(out, uint64(len(item.Fields)))
		for _, field := range item.Fields {
			out = appendStreamUvarint(out, uint64(len(field.Field)))
			out = append(out, field.Field...)
			out = appendStreamUvarint(out, uint64(len(field.Value)))
			out = append(out, field.Value...)
		}
		previous = item.ID
	}
	if len(out) > maxPackedStreamBytes {
		return nil, errors.New("ERR stream exceeds 32 MiB limit")
	}
	return out, nil
}

func decodePackedStream(data []byte) (packedStream, error) {
	if len(data) < len(packedStreamHeader)+16 || !bytes.Equal(data[:len(packedStreamHeader)], packedStreamHeader[:]) {
		return packedStream{}, errors.New("invalid packed stream")
	}
	offset := len(packedStreamHeader)
	state := packedStream{LastID: StreamID{
		Millis:   binary.BigEndian.Uint64(data[offset : offset+8]),
		Sequence: binary.BigEndian.Uint64(data[offset+8 : offset+16]),
	}}
	offset += 16
	count64, err := readStreamUvarint(data, &offset)
	if err != nil || count64 > uint64(maxPackedStreamBytes) {
		return packedStream{}, errors.New("invalid packed stream")
	}
	state.Entries = make([]StreamEntry, 0, int(count64))
	var previous StreamID
	for i := 0; i < int(count64); i++ {
		if offset+16 > len(data) {
			return packedStream{}, errors.New("invalid packed stream")
		}
		id := StreamID{
			Millis:   binary.BigEndian.Uint64(data[offset : offset+8]),
			Sequence: binary.BigEndian.Uint64(data[offset+8 : offset+16]),
		}
		offset += 16
		if i > 0 && !previous.less(id) || state.LastID.less(id) {
			return packedStream{}, errors.New("invalid packed stream order")
		}
		fieldCount, err := readStreamUvarint(data, &offset)
		if err != nil || fieldCount == 0 || fieldCount > uint64(maxPackedStreamBytes) {
			return packedStream{}, errors.New("invalid packed stream")
		}
		fields := make([]StreamField, 0, int(fieldCount))
		for j := uint64(0); j < fieldCount; j++ {
			fieldLen, err := readStreamUvarint(data, &offset)
			if err != nil || fieldLen > uint64(len(data)-offset) {
				return packedStream{}, errors.New("invalid packed stream")
			}
			fieldEnd := offset + int(fieldLen)
			field := append([]byte(nil), data[offset:fieldEnd]...)
			offset = fieldEnd
			valueLen, err := readStreamUvarint(data, &offset)
			if err != nil || valueLen > uint64(len(data)-offset) {
				return packedStream{}, errors.New("invalid packed stream")
			}
			valueEnd := offset + int(valueLen)
			value := append([]byte(nil), data[offset:valueEnd]...)
			offset = valueEnd
			fields = append(fields, StreamField{Field: field, Value: value})
		}
		state.Entries = append(state.Entries, StreamEntry{ID: id, Fields: fields})
		previous = id
	}
	if offset != len(data) {
		return packedStream{}, errors.New("invalid packed stream trailing data")
	}
	return state, nil
}

func streamPreparedEntry(packed []byte) preparedEntry {
	return preparedEntry{
		entry: entry{valueType: TypeStream, rawLength: uint32(len(packed))},
		data:  append([]byte(nil), packed...),
	}
}

func (s *Store) streamStateFromEntry(sh *shard, e entry) (packedStream, error) {
	return decodePackedStream(sh.encoded(e))
}

func (s *Store) streamLogicalValue(sh *shard, e entry) ([]byte, error) {
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	return encodePackedStream(state)
}

func parseStreamIDPair(text string) (StreamID, error) {
	parts := strings.Split(text, "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return StreamID{}, errors.New("ERR Invalid stream ID specified as stream command argument")
	}
	ms, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return StreamID{}, errors.New("ERR Invalid stream ID specified as stream command argument")
	}
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return StreamID{}, errors.New("ERR Invalid stream ID specified as stream command argument")
	}
	return StreamID{Millis: ms, Sequence: seq}, nil
}

func ParseStreamID(text string) (StreamID, error) { return parseStreamIDPair(text) }

func ParseStreamRangeBound(text string, start bool) (StreamRangeBound, error) {
	bound := StreamRangeBound{}
	if text == "-" {
		return bound, nil
	}
	if text == "+" {
		bound.ID = StreamID{Millis: math.MaxUint64, Sequence: math.MaxUint64}
		return bound, nil
	}
	if strings.HasPrefix(text, "(") {
		bound.Exclusive = true
		text = text[1:]
		if text == "" {
			return StreamRangeBound{}, errors.New("ERR Invalid stream ID specified as stream command argument")
		}
	}
	if !strings.Contains(text, "-") {
		ms, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return StreamRangeBound{}, errors.New("ERR Invalid stream ID specified as stream command argument")
		}
		bound.ID.Millis = ms
		if !start {
			bound.ID.Sequence = math.MaxUint64
		}
		return bound, nil
	}
	id, err := parseStreamIDPair(text)
	if err != nil {
		return StreamRangeBound{}, err
	}
	bound.ID = id
	return bound, nil
}

func nextAutomaticStreamID(nowMS uint64, last StreamID) (StreamID, error) {
	if nowMS < last.Millis {
		nowMS = last.Millis
	}
	if nowMS == last.Millis {
		if last.Sequence == math.MaxUint64 {
			return StreamID{}, errors.New("ERR stream sequence number overflow")
		}
		return StreamID{Millis: nowMS, Sequence: last.Sequence + 1}, nil
	}
	id := StreamID{Millis: nowMS}
	if id.Millis == 0 {
		id.Sequence = 1
	}
	return id, nil
}

func nextPartialStreamID(ms uint64, last StreamID) (StreamID, error) {
	if ms < last.Millis {
		return StreamID{}, errors.New("ERR The ID specified in XADD is equal or smaller than the target stream top item")
	}
	if ms == last.Millis {
		if last.Sequence == math.MaxUint64 {
			return StreamID{}, errors.New("ERR stream sequence number overflow")
		}
		return StreamID{Millis: ms, Sequence: last.Sequence + 1}, nil
	}
	id := StreamID{Millis: ms}
	if ms == 0 {
		id.Sequence = 1
	}
	return id, nil
}

func resolveStreamAddID(spec string, nowMS uint64, last StreamID) (StreamID, error) {
	if spec == "*" {
		return nextAutomaticStreamID(nowMS, last)
	}
	if strings.HasSuffix(spec, "-*") {
		ms, err := strconv.ParseUint(strings.TrimSuffix(spec, "-*"), 10, 64)
		if err != nil {
			return StreamID{}, errors.New("ERR Invalid stream ID specified as stream command argument")
		}
		return nextPartialStreamID(ms, last)
	}
	id, err := parseStreamIDPair(spec)
	if err != nil {
		return StreamID{}, err
	}
	if id.Millis == 0 && id.Sequence == 0 {
		return StreamID{}, errors.New("ERR The ID specified in XADD must be greater than 0-0")
	}
	if !last.less(id) && (last.Millis != 0 || last.Sequence != 0) {
		return StreamID{}, errors.New("ERR The ID specified in XADD is equal or smaller than the target stream top item")
	}
	return id, nil
}

func cloneStreamFields(fields []StreamField) []StreamField {
	out := make([]StreamField, len(fields))
	for i, field := range fields {
		out[i] = StreamField{Field: append([]byte(nil), field.Field...), Value: append([]byte(nil), field.Value...)}
	}
	return out
}

func (s *Store) StreamAdd(key, idSpec string, fields []StreamField, options StreamAddOptions) (StreamID, bool, error) {
	if len(fields) == 0 {
		return StreamID{}, false, errors.New("ERR wrong number of arguments for 'xadd' command")
	}
	if options.HasMaxLen && options.MaxLen < 0 {
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
	state.Entries = append(state.Entries, StreamEntry{ID: id, Fields: cloneStreamFields(fields)})
	if options.HasMaxLen && len(state.Entries) > options.MaxLen {
		state.Entries = state.Entries[len(state.Entries)-options.MaxLen:]
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

func (s *Store) StreamLen(key string) (int64, error) {
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
	return int64(len(state.Entries)), nil
}

func streamIDInRange(id StreamID, start, end StreamRangeBound) bool {
	if id.less(start.ID) || start.Exclusive && id.equal(start.ID) {
		return false
	}
	if end.ID.less(id) || end.Exclusive && id.equal(end.ID) {
		return false
	}
	return true
}

func cloneStreamEntry(entry StreamEntry) StreamEntry {
	return StreamEntry{ID: entry.ID, Fields: cloneStreamFields(entry.Fields)}
}

func (s *Store) StreamRange(key string, start, end StreamRangeBound, count int, reverse bool) ([]StreamEntry, error) {
	if count < 0 {
		return nil, errors.New("ERR COUNT must be greater than 0")
	}
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return []StreamEntry{}, nil
	}
	if e.valueType != TypeStream {
		return nil, streamWrongType()
	}
	state, err := s.streamStateFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return []StreamEntry{}, nil
	}
	out := make([]StreamEntry, 0)
	if reverse {
		for i := len(state.Entries) - 1; i >= 0; i-- {
			if streamIDInRange(state.Entries[i].ID, start, end) {
				out = append(out, cloneStreamEntry(state.Entries[i]))
				if len(out) >= count {
					break
				}
			}
		}
		return out, nil
	}
	for i := range state.Entries {
		if streamIDInRange(state.Entries[i].ID, start, end) {
			out = append(out, cloneStreamEntry(state.Entries[i]))
			if len(out) >= count {
				break
			}
		}
	}
	return out, nil
}

func (s *Store) StreamDelete(key string, ids []StreamID) (int64, error) {
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
	wanted := make(map[StreamID]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	kept := state.Entries[:0]
	var deleted int64
	for _, item := range state.Entries {
		if _, remove := wanted[item.ID]; remove {
			deleted++
			continue
		}
		kept = append(kept, item)
	}
	if deleted == 0 {
		return 0, nil
	}
	state.Entries = kept
	packed, err := encodePackedStream(state)
	if err != nil {
		return 0, err
	}
	updated := streamPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return deleted, nil
}

func (s *Store) StreamTrimMaxLen(key string, maxLen, limit int) (int64, error) {
	if maxLen < 0 || limit < 0 {
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
	remove := len(state.Entries) - maxLen
	if remove <= 0 {
		return 0, nil
	}
	if limit > 0 && remove > limit {
		remove = limit
	}
	state.Entries = state.Entries[remove:]
	packed, err := encodePackedStream(state)
	if err != nil {
		return 0, err
	}
	updated := streamPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return int64(remove), nil
}
