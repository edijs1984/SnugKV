package engine

import (
	"errors"

	"snugkv/internal/persistence"
)

var errBadStreamDelta = errors.New("ERR recovered STREAM delta is invalid")

// parseStreamEntryBlock decodes count packed entries, which must be strictly
// ascending and fill data exactly.
func parseStreamEntryBlock(data []byte, count int) ([]StreamEntry, error) {
	if count < 0 || count > len(data)/17 {
		if count != 0 || len(data) != 0 {
			return nil, errBadStreamDelta
		}
	}
	entries := make([]StreamEntry, 0, count)
	offset := 0
	var previous StreamID
	for i := 0; i < count; i++ {
		id, err := readStreamID(data, &offset)
		if err != nil || i > 0 && !previous.less(id) {
			return nil, errBadStreamDelta
		}
		n, err := readStreamUvarint(data, &offset)
		if err != nil || n == 0 || n > uint64(len(data)) {
			return nil, errBadStreamDelta
		}
		fields := make([]StreamField, 0, int(n))
		for j := uint64(0); j < n; j++ {
			var parts [2][]byte
			for k := range parts {
				l, err := readStreamUvarint(data, &offset)
				if err != nil || l > uint64(len(data)-offset) {
					return nil, errBadStreamDelta
				}
				parts[k] = append([]byte(nil), data[offset:offset+int(l)]...)
				offset += int(l)
			}
			fields = append(fields, StreamField{Field: parts[0], Value: parts[1]})
		}
		entries = append(entries, StreamEntry{ID: id, Fields: fields})
		previous = id
	}
	if offset != len(data) {
		return nil, errBadStreamDelta
	}
	return entries, nil
}

func pairID(p [2]uint64) StreamID { return StreamID{Millis: p[0], Sequence: p[1]} }

func maxStreamID(a, b StreamID) StreamID {
	if a.less(b) {
		return b
	}
	return a
}

// applyStreamDelta applies one stream delta record. Deltas are convergent:
// replaying one over a state that already contains it, or a newer one, leaves
// the stream consistent once the later deltas are replayed too.
func (s *Store) applyStreamDelta(rec persistence.Record, force bool) error {
	d := rec.Stream
	if len(d.Delete)%16 != 0 {
		return errBadStreamDelta
	}
	entries, err := parseStreamEntryBlock(d.Append, d.AppendCount)
	if err != nil {
		return err
	}
	var groups []streamGroup
	if d.GroupsSet {
		offset := 0
		groups, err = readStreamGroups(d.Groups, &offset, packedStreamHeaderV3[2])
		if err != nil || offset != len(d.Groups) {
			return errBadStreamDelta
		}
	}
	lastID, maxDel := pairID(d.LastID), pairID(d.MaxDeletedID)
	admission := enforceMemoryLimit
	if force {
		admission = allowOverMemoryLimit
	}
	key := string(rec.Key)
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := s.now()
	old, exists := sh.get(key)
	if exists && (sh.expired(key, old, now) || old.valueType != TypeStream) {
		if !d.Create {
			return errBadStreamDelta
		}
	}
	if d.Create {
		if d.EntriesAdded < uint64(len(entries)) || lastID.less(maxDel) {
			return errBadStreamDelta
		}
		if n := len(entries); n > 0 && lastID.less(entries[n-1].ID) {
			return errBadStreamDelta
		}
		var expires stamp
		if rec.ExpiresAtMS != 0 {
			expires = stamp(rec.ExpiresAtMS)
		}
		return s.installStreamLocked(sh, key, packedStream{
			Entries: entries, LastID: lastID, MaxDeletedID: maxDel, EntriesAdded: d.EntriesAdded, Groups: groups,
		}, expires, admission)
	}
	if !exists {
		return errBadStreamDelta
	}
	state, err := s.streamStateFromEntry(sh, key, old)
	if err != nil {
		return err
	}
	body := state.log
	if !force {
		if err := s.admitStreamGrowth(uint64(len(d.Append)) + 4096); err != nil {
			return err
		}
	}
	before := body.memory()
	floor := body.LastID
	for _, e := range entries {
		if !floor.less(e.ID) {
			continue
		}
		body.Append(e.ID, e.Fields)
	}
	if d.HasRemoveThrough {
		if n := body.CountThrough(pairID(d.RemoveThrough)); n > 0 {
			body.TrimFront(n)
		}
	}
	if len(d.Delete) > 0 {
		ids := make(map[StreamID]struct{}, len(d.Delete)/16)
		for off := 0; off < len(d.Delete); {
			id, _ := readStreamID(d.Delete, &off)
			ids[id] = struct{}{}
		}
		body.Delete(ids)
	}
	body.LastID = maxStreamID(body.LastID, lastID)
	body.MaxDeletedID = maxStreamID(body.MaxDeletedID, maxDel)
	if d.EntriesAdded > body.EntriesAdded {
		body.EntriesAdded = d.EntriesAdded
	}
	s.settleStreamMemory(before, body.memory())
	if last, ok := body.LastEntryID(); ok && body.LastID.less(last) || body.EntriesAdded < uint64(body.length) {
		return errBadStreamDelta
	}
	if d.GroupsSet {
		state.LastID, state.EntriesAdded, state.MaxDeletedID = body.LastID, body.EntriesAdded, body.MaxDeletedID
		state.Groups = groups
		return s.publishStreamStateLocked(sh, key, old, state)
	}
	return nil
}

// restoreWithStreamDeltas handles a batch that contains stream deltas. Records
// are applied in order; runs of ordinary records go through Restore.
func (s *Store) restoreWithStreamDeltas(records []persistence.Record, force bool) error {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Reset {
			s.resetForRecovery()
			records = records[i+1:]
			break
		}
	}
	start := 0
	flush := func(end int) error {
		if end > start {
			if err := s.Restore(records[start:end], force); err != nil {
				return err
			}
		}
		return nil
	}
	for i := range records {
		if records[i].Stream == nil {
			continue
		}
		if err := flush(i); err != nil {
			return err
		}
		start = i + 1
		if err := s.applyStreamDelta(records[i], force); err != nil {
			return err
		}
	}
	return flush(len(records))
}

// streamExportInlineMax is the largest stream exported as one ordinary record.
// Bigger streams are exported as a create record followed by append records, so
// no single record has to hold the whole stream.
var streamExportInlineMax uint64 = 16 << 20

var streamExportPiece = 4 << 20

func (s *Store) streamExportRecords(key string, st streamState, expiresAtMS int64) ([]persistence.Record, error) {
	groups, err := appendStreamGroups(nil, st.Groups)
	if err != nil {
		return nil, err
	}
	var out []persistence.Record
	st.log.packedPieces(streamExportPiece, func(data []byte, count int, last StreamID, final bool) {
		// A piece only claims the progress up to its own last entry, so later
		// pieces are not mistaken for entries the stream already passed.
		d := &persistence.StreamDelta{
			Create: len(out) == 0, Append: data, AppendCount: count,
			LastID: idPair(last), EntriesAdded: st.EntriesAdded,
		}
		if final {
			d.LastID, d.MaxDeletedID = idPair(st.LastID), idPair(st.MaxDeletedID)
		}
		if d.Create {
			d.Groups, d.GroupsSet = groups, true
		}
		rec := persistence.Record{Key: []byte(key), ValueType: uint8(TypeStream), Stream: d}
		if d.Create {
			rec.ExpiresAtMS = expiresAtMS
		}
		out = append(out, rec)
	})
	if len(out) == 0 {
		return nil, errors.New("ERR empty stream export")
	}
	return out, nil
}
