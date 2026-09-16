package engine

import (
	"errors"
	"strconv"
	"strings"
)

type StreamReadCursor struct {
	ID     StreamID
	Latest bool
}

type StreamReadResult struct {
	Key     string
	Entries []StreamEntry
}

func ParseStreamReadCursor(text string) (StreamReadCursor, error) {
	if text == "$" {
		return StreamReadCursor{Latest: true}, nil
	}
	if strings.Contains(text, "-") {
		id, err := parseStreamIDPair(text)
		if err != nil {
			return StreamReadCursor{}, err
		}
		return StreamReadCursor{ID: id}, nil
	}
	ms, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return StreamReadCursor{}, errors.New("ERR Invalid stream ID specified as stream command argument")
	}
	return StreamReadCursor{ID: StreamID{Millis: ms}}, nil
}

// ResolveStreamReadCursors snapshots the current top ID for every '$' cursor.
// Missing streams resolve to 0-0. Existing non-stream keys return WRONGTYPE.
func (s *Store) ResolveStreamReadCursors(keys []string, cursors []StreamReadCursor) ([]StreamID, error) {
	if len(keys) != len(cursors) {
		return nil, errors.New("ERR mismatched stream key/ID count")
	}
	unlock := s.lockAll()
	defer unlock()
	now := s.now()
	resolved := make([]StreamID, len(cursors))
	for i, cursor := range cursors {
		if !cursor.Latest {
			resolved[i] = cursor.ID
			continue
		}
		sh := s.shardFor(keys[i])
		e, ok := sh.get(keys[i])
		if !ok {
			continue
		}
		if sh.expired(keys[i], e, now) {
			s.remove(sh, keys[i])
			continue
		}
		if e.valueType != TypeStream {
			return nil, streamWrongType()
		}
		state, err := s.streamStateFromEntry(sh, e)
		if err != nil {
			return nil, err
		}
		resolved[i] = state.LastID
	}
	return resolved, nil
}

// StreamReadAfter returns entries with IDs strictly greater than the supplied
// cursor for each stream. Results preserve the input stream order; streams with
// no matching entries are omitted.
func (s *Store) StreamReadAfter(keys []string, ids []StreamID, count int) ([]StreamReadResult, error) {
	if len(keys) != len(ids) {
		return nil, errors.New("ERR mismatched stream key/ID count")
	}
	if count <= 0 {
		return nil, errors.New("ERR count should be greater than 0")
	}
	unlock := s.lockAll()
	defer unlock()
	now := s.now()
	results := make([]StreamReadResult, 0, len(keys))
	for i, key := range keys {
		sh := s.shardFor(key)
		e, ok := sh.get(key)
		if !ok {
			continue
		}
		if sh.expired(key, e, now) {
			s.remove(sh, key)
			continue
		}
		if e.valueType != TypeStream {
			return nil, streamWrongType()
		}
		state, err := s.streamStateFromEntry(sh, e)
		if err != nil {
			return nil, err
		}
		capacity := count
		if capacity > len(state.Entries) {
			capacity = len(state.Entries)
		}
		entries := make([]StreamEntry, 0, capacity)
		for _, item := range state.Entries {
			if !ids[i].less(item.ID) {
				continue
			}
			entries = append(entries, cloneStreamEntry(item))
			if len(entries) == count {
				break
			}
		}
		if len(entries) > 0 {
			results = append(results, StreamReadResult{Key: key, Entries: entries})
		}
	}
	return results, nil
}
