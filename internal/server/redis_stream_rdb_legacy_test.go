package server

import (
	"math"
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


func appendLegacyStreamGroups(body []byte, streamType byte, group engine.StreamSnapshotGroup) []byte {
	body = appendRDBLen(body, 1)
	body = appendRDBRawString(body, []byte(group.Name))
	body = appendRDBLen(body, group.LastDeliveredID.Millis)
	body = appendRDBLen(body, group.LastDeliveredID.Sequence)
	if streamType >= redisRDBTypeStreamListpacks2 {
		if group.EntriesRead < 0 {
			body = appendRDBLen(body, math.MaxUint64)
		} else {
			body = appendRDBLen(body, uint64(group.EntriesRead))
		}
	}

	body = appendRDBLen(body, uint64(len(group.Pending)))
	for _, pending := range group.Pending {
		body = append(body, streamIDBytes(pending.ID)...)
		body = appendRDBFixedI64(body, pending.DeliveredAt)
		body = appendRDBLen(body, pending.Deliveries)
	}

	body = appendRDBLen(body, uint64(len(group.Consumers)))
	for _, consumer := range group.Consumers {
		body = appendRDBRawString(body, []byte(consumer.Name))
		body = appendRDBFixedI64(body, consumer.SeenAt)
		if streamType >= keyRDBTypeStreamListpacks3 {
			body = appendRDBFixedI64(body, consumer.ActiveAt)
		}
		count := 0
		for _, pending := range group.Pending {
			if pending.Consumer == consumer.Name {
				count++
			}
		}
		body = appendRDBLen(body, uint64(count))
		for _, pending := range group.Pending {
			if pending.Consumer == consumer.Name {
				body = append(body, streamIDBytes(pending.ID)...)
			}
		}
	}
	return body
}

func TestDecodeRedisLegacyStreamGroupsType15(t *testing.T) {
	entries := []engine.StreamEntry{
		{
			ID: engine.StreamID{Millis: 3000, Sequence: 0},
			Fields: []engine.StreamField{{Field: []byte("f"), Value: []byte("one")}},
		},
		{
			ID: engine.StreamID{Millis: 3001, Sequence: 0},
			Fields: []engine.StreamField{{Field: []byte("f"), Value: []byte("two")}},
		},
	}
	var body []byte
	lp, master, err := encodeStreamListpack(entries)
	if err != nil {
		t.Fatal(err)
	}
	body = appendRDBLen(body, 1)
	body = appendRDBRawString(body, streamIDBytes(master))
	body = appendRDBRawString(body, lp)
	body = appendRDBLen(body, uint64(len(entries)))
	body = appendRDBLen(body, entries[1].ID.Millis)
	body = appendRDBLen(body, entries[1].ID.Sequence)

	group := engine.StreamSnapshotGroup{
		Name:            "g",
		LastDeliveredID: entries[0].ID,
		EntriesRead:     -1,
		Pending: []engine.StreamSnapshotPending{{
			ID:          entries[0].ID,
			DeliveredAt: 12345,
			Deliveries:  2,
			Consumer:    "c",
		}},
		Consumers: []engine.StreamSnapshotConsumer{{
			Name:     "c",
			SeenAt:   777,
			ActiveAt: 999,
		}},
	}
	body = appendLegacyStreamGroups(body, redisRDBTypeStreamListpacks, group)

	pos := 0
	got, err := decodeStreamDumpVersion(body, &pos, redisRDBTypeStreamListpacks)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 1 {
		t.Fatalf("groups=%d want=1", len(got.Groups))
	}
	g := got.Groups[0]
	if g.EntriesRead != 1 {
		t.Fatalf("entries-read=%d want=1", g.EntriesRead)
	}
	if len(g.Consumers) != 1 || g.Consumers[0].ActiveAt != g.Consumers[0].SeenAt {
		t.Fatalf("consumer=%+v", g.Consumers)
	}
	if len(g.Pending) != 1 || g.Pending[0].Consumer != "c" {
		t.Fatalf("pending=%+v", g.Pending)
	}
}

func TestDecodeRedisLegacyStreamGroupsType19(t *testing.T) {
	entries := []engine.StreamEntry{
		{
			ID: engine.StreamID{Millis: 4000, Sequence: 0},
			Fields: []engine.StreamField{{Field: []byte("f"), Value: []byte("one")}},
		},
	}
	var body []byte
	lp, master, err := encodeStreamListpack(entries)
	if err != nil {
		t.Fatal(err)
	}
	body = appendRDBLen(body, 1)
	body = appendRDBRawString(body, streamIDBytes(master))
	body = appendRDBRawString(body, lp)
	body = appendRDBLen(body, uint64(len(entries)))
	body = appendRDBLen(body, entries[0].ID.Millis)
	body = appendRDBLen(body, entries[0].ID.Sequence)
	body = appendRDBLen(body, entries[0].ID.Millis)
	body = appendRDBLen(body, entries[0].ID.Sequence)
	body = appendRDBLen(body, 0)
	body = appendRDBLen(body, 0)
	body = appendRDBLen(body, 3)

	group := engine.StreamSnapshotGroup{
		Name:            "g2",
		LastDeliveredID: entries[0].ID,
		EntriesRead:     7,
		Consumers: []engine.StreamSnapshotConsumer{{
			Name:     "c2",
			SeenAt:   888,
			ActiveAt: 999,
		}},
	}
	body = appendLegacyStreamGroups(body, redisRDBTypeStreamListpacks2, group)

	pos := 0
	got, err := decodeStreamDumpVersion(body, &pos, redisRDBTypeStreamListpacks2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 1 {
		t.Fatalf("groups=%d want=1", len(got.Groups))
	}
	g := got.Groups[0]
	if g.EntriesRead != 7 {
		t.Fatalf("entries-read=%d want=7", g.EntriesRead)
	}
	if len(g.Consumers) != 1 || g.Consumers[0].ActiveAt != g.Consumers[0].SeenAt {
		t.Fatalf("consumer=%+v", g.Consumers)
	}
}
