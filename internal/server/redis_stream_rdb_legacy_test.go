package server

import (
	"testing"

	"snugkv/internal/engine"
)

func encodeLegacyStreamBody(t *testing.T, streamType byte, entries []engine.StreamEntry, last engine.StreamID, first engine.StreamID, maxDeleted engine.StreamID, entriesAdded uint64) []byte {
	t.Helper()
	var body []byte
	if len(entries) == 0 {
		body = appendRDBLen(body, 0)
	} else {
		lp, master, err := encodeStreamListpack(entries)
		if err != nil {
			t.Fatal(err)
		}
		body = appendRDBLen(body, 1)
		body = appendRDBRawString(body, streamIDBytes(master))
		body = appendRDBRawString(body, lp)
	}
	body = appendRDBLen(body, uint64(len(entries)))
	body = appendRDBLen(body, last.Millis)
	body = appendRDBLen(body, last.Sequence)
	if streamType >= redisRDBTypeStreamListpacks2 {
		body = appendRDBLen(body, first.Millis)
		body = appendRDBLen(body, first.Sequence)
		body = appendRDBLen(body, maxDeleted.Millis)
		body = appendRDBLen(body, maxDeleted.Sequence)
		body = appendRDBLen(body, entriesAdded)
	}
	body = appendRDBLen(body, 0)
	return body
}

func TestDecodeRedisLegacyStreamListpacksType15(t *testing.T) {
	entries := []engine.StreamEntry{
		{
			ID: engine.StreamID{Millis: 1000, Sequence: 0},
			Fields: []engine.StreamField{{Field: []byte("f"), Value: []byte("one")}},
		},
		{
			ID: engine.StreamID{Millis: 1001, Sequence: 2},
			Fields: []engine.StreamField{{Field: []byte("f"), Value: []byte("two")}},
		},
	}
	body := encodeLegacyStreamBody(
		t,
		redisRDBTypeStreamListpacks,
		entries,
		entries[1].ID,
		engine.StreamID{},
		engine.StreamID{},
		0,
	)
	pos := 0
	got, err := decodeStreamDumpVersion(body, &pos, redisRDBTypeStreamListpacks)
	if err != nil {
		t.Fatal(err)
	}
	if pos != len(body) {
		t.Fatalf("consumed=%d want=%d", pos, len(body))
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries=%d want=2", len(got.Entries))
	}
	if got.LastID != entries[1].ID {
		t.Fatalf("last=%+v want=%+v", got.LastID, entries[1].ID)
	}
	if got.EntriesAdded != 2 {
		t.Fatalf("entries-added=%d want=2", got.EntriesAdded)
	}
	if got.MaxDeletedID != (engine.StreamID{}) {
		t.Fatalf("max-deleted=%+v want zero", got.MaxDeletedID)
	}
}

func TestDecodeRedisLegacyStreamListpacksType19(t *testing.T) {
	entries := []engine.StreamEntry{
		{
			ID: engine.StreamID{Millis: 2000, Sequence: 1},
			Fields: []engine.StreamField{{Field: []byte("a"), Value: []byte("x")}},
		},
		{
			ID: engine.StreamID{Millis: 2005, Sequence: 3},
			Fields: []engine.StreamField{{Field: []byte("b"), Value: []byte("y")}},
		},
	}
	first := entries[0].ID
	last := entries[1].ID
	maxDeleted := engine.StreamID{Millis: 1999, Sequence: 9}
	body := encodeLegacyStreamBody(
		t,
		redisRDBTypeStreamListpacks2,
		entries,
		last,
		first,
		maxDeleted,
		7,
	)
	pos := 0
	got, err := decodeStreamDumpVersion(body, &pos, redisRDBTypeStreamListpacks2)
	if err != nil {
		t.Fatal(err)
	}
	if pos != len(body) {
		t.Fatalf("consumed=%d want=%d", pos, len(body))
	}
	if got.LastID != last {
		t.Fatalf("last=%+v want=%+v", got.LastID, last)
	}
	if got.MaxDeletedID != maxDeleted {
		t.Fatalf("max-deleted=%+v want=%+v", got.MaxDeletedID, maxDeleted)
	}
	if got.EntriesAdded != 7 {
		t.Fatalf("entries-added=%d want=7", got.EntriesAdded)
	}
}
