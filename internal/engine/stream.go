package engine

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
)

const maxPackedStreamBytes = 32 << 20

var packedStreamHeaderV1 = [...]byte{'S', 'X', 1}
var packedStreamHeaderV2 = [...]byte{'S', 'X', 2}
var packedStreamHeaderV3 = [...]byte{'S', 'X', 3}

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
	LastID       StreamID
	EntriesAdded uint64
	MaxDeletedID StreamID
	Entries      []StreamEntry
	Groups       []streamGroup
}

type StreamAddOptions struct {
	NoMkStream bool
	MaxLen     int
	HasMaxLen  bool
	MinID      StreamID
	HasMinID   bool
	Limit      int
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
	if state.EntriesAdded < uint64(len(state.Entries)) || state.LastID.less(state.MaxDeletedID) {
		return nil, errors.New("ERR invalid stream lifetime metadata")
	}
	capacity := len(packedStreamHeaderV3) + 32 + 2*binary.MaxVarintLen64
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
	out = append(out, packedStreamHeaderV3[:]...)
	out = appendStreamID(out, state.LastID)
	out = appendStreamUvarint(out, state.EntriesAdded)
	out = appendStreamID(out, state.MaxDeletedID)
	out = appendStreamUvarint(out, uint64(len(state.Entries)))
	var previous StreamID
	for i, item := range state.Entries {
		if i > 0 && !previous.less(item.ID) {
			return nil, errors.New("ERR stream entries are not ordered")
		}
		if state.LastID.less(item.ID) {
			return nil, errors.New("ERR stream last-generated ID precedes an entry")
		}
		out = appendStreamID(out, item.ID)
		out = appendStreamUvarint(out, uint64(len(item.Fields)))
		for _, field := range item.Fields {
			out = appendStreamUvarint(out, uint64(len(field.Field)))
			out = append(out, field.Field...)
			out = appendStreamUvarint(out, uint64(len(field.Value)))
			out = append(out, field.Value...)
		}
		previous = item.ID
	}
	var err error
	out, err = appendStreamGroups(out, state.Groups)
	if err != nil {
		return nil, err
	}
	if len(out) > maxPackedStreamBytes {
		return nil, errors.New("ERR stream exceeds 32 MiB limit")
	}
	return out, nil
}

func decodePackedStream(data []byte) (packedStream, error) {
	if len(data) < len(packedStreamHeaderV1)+16 || data[0] != 'S' || data[1] != 'X' {
		return packedStream{}, errors.New("invalid packed stream")
	}
	version := data[2]
	if version != packedStreamHeaderV1[2] && version != packedStreamHeaderV2[2] && version != packedStreamHeaderV3[2] {
		return packedStream{}, errors.New("invalid packed stream")
	}
	offset := len(packedStreamHeaderV1)
	lastID, err := readStreamID(data, &offset)
	if err != nil {
		return packedStream{}, err
	}
	state := packedStream{LastID: lastID}
	if version == packedStreamHeaderV3[2] {
		state.EntriesAdded, err = readStreamUvarint(data, &offset)
		if err != nil {
			return packedStream{}, err
		}
		state.MaxDeletedID, err = readStreamID(data, &offset)
		if err != nil || state.LastID.less(state.MaxDeletedID) {
			return packedStream{}, errors.New("invalid packed stream")
		}
	}
	count64, err := readStreamUvarint(data, &offset)
	if err != nil || count64 > uint64(maxPackedStreamBytes) {
		return packedStream{}, errors.New("invalid packed stream")
	}
	if version == packedStreamHeaderV3[2] && state.EntriesAdded < count64 {
		return packedStream{}, errors.New("invalid packed stream")
	}
	state.Entries = make([]StreamEntry, 0, int(count64))
	var previous StreamID
	for i := 0; i < int(count64); i++ {
		id, err := readStreamID(data, &offset)
		if err != nil {
			return packedStream{}, err
		}
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
	if version >= packedStreamHeaderV2[2] {
		state.Groups, err = readStreamGroups(data, &offset, version)
		if err != nil {
			return packedStream{}, err
		}
	}
	if version < packedStreamHeaderV3[2] {
		state.EntriesAdded = count64
		for _, group := range state.Groups {
			if group.EntriesRead >= 0 && uint64(group.EntriesRead) > state.EntriesAdded {
				state.EntriesAdded = uint64(group.EntriesRead)
			}
			for _, pending := range group.Pending {
				if !packedStreamHasEntry(state.Entries, pending.ID) && state.MaxDeletedID.less(pending.ID) {
					state.MaxDeletedID = pending.ID
				}
			}
		}
	}
	if offset != len(data) {
		return packedStream{}, errors.New("invalid packed stream trailing data")
	}
	return state, nil
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

func noteStreamDeleted(state *streamState, id StreamID) {
	if state.MaxDeletedID.less(id) {
		state.MaxDeletedID = id
	}
}

// streamAddGrowth is the memory a new entry is expected to need.
func streamAddGrowth(size int) uint64 { return uint64(size) + 64 }

func (s *Store) StreamAdd(key, idSpec string, fields []StreamField, options StreamAddOptions) (StreamID, bool, error) {
	if len(fields) == 0 {
		return StreamID{}, false, errors.New("ERR wrong number of arguments for 'xadd' command")
	}
	if options.HasMaxLen && options.MaxLen < 0 || options.Limit < 0 || options.HasMaxLen && options.HasMinID {
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
	if state.EntriesAdded == math.MaxUint64 {
		return StreamID{}, false, errors.New("ERR stream entries-added counter overflow")
	}
	size := streamEntrySize(fields)
	if state.log.dataBytes+uint64(size)+streamStubAllowance(state) > maxPackedStreamBytes {
		return StreamID{}, false, errStreamTooLarge
	}
	if err := s.admitStreamGrowth(streamAddGrowth(size)); err != nil {
		return StreamID{}, false, err
	}

	before := state.log.memory()
	if !exists {
		before = 0
	}
	state.log.Append(id, fields)
	state.LastID = id
	state.EntriesAdded++
	state.syncLog()
	if options.HasMaxLen {
		remove := state.log.Len() - options.MaxLen
		if remove > 0 {
			if options.Limit > 0 && remove > options.Limit {
				remove = options.Limit
			}
			state.log.TrimFront(remove)
		}
	} else if options.HasMinID {
		if remove := state.log.CountBefore(options.MinID, options.Limit); remove > 0 {
			state.log.TrimFront(remove)
		}
	}
	if !exists {
		if err := s.createStreamLocked(sh, key, state, expiresAt); err != nil {
			return StreamID{}, false, err
		}
		return id, true, nil
	}
	s.settleStreamMemory(before, state.log.memory())
	return id, true, nil
}

// streamStubAllowance bounds the size of the stored part for limit checks
// without encoding it on every append.
func streamStubAllowance(state streamState) uint64 {
	n := uint64(64)
	for i := range state.Groups {
		g := &state.Groups[i]
		n += uint64(len(g.Name)) + 40 + uint64(len(g.Consumers))*24
		for j := range g.Consumers {
			n += uint64(len(g.Consumers[j].Name))
		}
		n += uint64(len(g.Pending)) * 40
	}
	return n
}

func (s *Store) StreamLen(key string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, nil
	}
	if e.valueType != TypeStream {
		return 0, streamWrongType()
	}
	body := s.streamLogs.get(key)
	if body == nil {
		return 0, errStreamLogMissing
	}
	return int64(body.Len()), nil
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
	body := s.streamLogs.get(key)
	if body == nil {
		return nil, errStreamLogMissing
	}
	if count == 0 {
		return []StreamEntry{}, nil
	}
	out := make([]StreamEntry, 0)
	if reverse {
		body.ForEachReverse(end.ID, func(item StreamEntry) bool {
			if item.ID.less(start.ID) {
				return false
			}
			if streamIDInRange(item.ID, start, end) {
				out = append(out, item)
				if len(out) >= count {
					return false
				}
			}
			return true
		})
		return out, nil
	}
	body.ForEachFrom(start.ID, true, func(item StreamEntry) bool {
		if end.ID.less(item.ID) {
			return false
		}
		if streamIDInRange(item.ID, start, end) {
			out = append(out, item)
			if len(out) >= count {
				return false
			}
		}
		return true
	})
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
	body := s.streamLogs.get(key)
	if body == nil {
		return 0, errStreamLogMissing
	}
	existing := make(map[StreamID]struct{}, len(ids))
	for _, id := range ids {
		if body.Has(id) {
			existing[id] = struct{}{}
		}
	}
	if len(existing) == 0 {
		return 0, nil
	}
	before := body.memory()
	deleted := body.Delete(existing)
	for id := range existing {
		if body.MaxDeletedID.less(id) {
			body.MaxDeletedID = id
		}
	}
	s.settleStreamMemory(before, body.memory())
	return int64(deleted), nil
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
	body := s.streamLogs.get(key)
	if body == nil {
		return 0, errStreamLogMissing
	}
	remove := body.Len() - maxLen
	if remove <= 0 {
		return 0, nil
	}
	if limit > 0 && remove > limit {
		remove = limit
	}
	before := body.memory()
	removed := body.TrimFront(remove)
	s.settleStreamMemory(before, body.memory())
	return int64(removed), nil
}

func (s *Store) StreamTrimMinID(key string, minID StreamID, limit int) (int64, error) {
	if limit < 0 {
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
	body := s.streamLogs.get(key)
	if body == nil {
		return 0, errStreamLogMissing
	}
	remove := body.CountBefore(minID, limit)
	if remove == 0 {
		return 0, nil
	}
	before := body.memory()
	removed := body.TrimFront(remove)
	s.settleStreamMemory(before, body.memory())
	return int64(removed), nil
}

func packedStreamHasEntry(entries []StreamEntry, id StreamID) bool {
	i := sort.Search(len(entries), func(i int) bool { return !entries[i].ID.less(id) })
	return i < len(entries) && entries[i].ID.equal(id)
}
