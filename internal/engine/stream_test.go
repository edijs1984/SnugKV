package engine

import (
	"testing"
	"time"
)

func streamFields(values ...string) []StreamField {
	out := make([]StreamField, 0, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		out = append(out, StreamField{Field: []byte(values[i]), Value: []byte(values[i+1])})
	}
	return out
}

func TestPackedStreamRoundTrip(t *testing.T) {
	state := packedStream{
		LastID: StreamID{Millis: 2, Sequence: 3},
		Entries: []StreamEntry{
			{ID: StreamID{Millis: 1, Sequence: 0}, Fields: streamFields("a", "1", "b", "2")},
			{ID: StreamID{Millis: 2, Sequence: 3}, Fields: streamFields("c", "three")},
		},
	}
	packed, err := encodePackedStream(state)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePackedStream(packed)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.LastID.String() != "2-3" || len(decoded.Entries) != 2 || decoded.Entries[0].ID.String() != "1-0" || decoded.Entries[1].ID.String() != "2-3" {
		t.Fatalf("decoded state = %#v", decoded)
	}
	if string(decoded.Entries[0].Fields[1].Field) != "b" || string(decoded.Entries[0].Fields[1].Value) != "2" {
		t.Fatalf("decoded fields = %#v", decoded.Entries[0].Fields)
	}
}

func TestStreamAddRangeDeleteTrimAndType(t *testing.T) {
	s, err := NewWithShards(4)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, value string
	}{
		{"1-0", "one"},
		{"2-0", "two"},
		{"3-0", "three"},
	} {
		id, applied, err := s.StreamAdd("events", tc.id, streamFields("v", tc.value), StreamAddOptions{})
		if err != nil || !applied || id.String() != tc.id {
			t.Fatalf("XADD %s id=%s applied=%t err=%v", tc.id, id.String(), applied, err)
		}
	}
	if got := s.Type("events"); got != "stream" {
		t.Fatalf("TYPE = %q, want stream", got)
	}
	if valueType, ok := s.ValueTypeOf("events"); !ok || valueType != TypeStream {
		t.Fatalf("ValueTypeOf = %v %t", valueType, ok)
	}
	if n, err := s.StreamLen("events"); err != nil || n != 3 {
		t.Fatalf("XLEN = %d err=%v", n, err)
	}
	start, _ := ParseStreamRangeBound("1-1", true)
	end, _ := ParseStreamRangeBound("+", false)
	items, err := s.StreamRange("events", start, end, 10, false)
	if err != nil || len(items) != 2 || items[0].ID.String() != "2-0" || items[1].ID.String() != "3-0" {
		t.Fatalf("XRANGE = %#v err=%v", items, err)
	}
	items, err = s.StreamRange("events", start, end, 1, true)
	if err != nil || len(items) != 1 || items[0].ID.String() != "3-0" {
		t.Fatalf("XREVRANGE = %#v err=%v", items, err)
	}
	deleted, err := s.StreamDelete("events", []StreamID{{Millis: 2}})
	if err != nil || deleted != 1 {
		t.Fatalf("XDEL = %d err=%v", deleted, err)
	}
	trimmed, err := s.StreamTrimMaxLen("events", 1, 0)
	if err != nil || trimmed != 1 {
		t.Fatalf("XTRIM = %d err=%v", trimmed, err)
	}
	allStart, _ := ParseStreamRangeBound("-", true)
	allEnd, _ := ParseStreamRangeBound("+", false)
	items, err = s.StreamRange("events", allStart, allEnd, 10, false)
	if err != nil || len(items) != 1 || items[0].ID.String() != "3-0" {
		t.Fatalf("post-trim range = %#v err=%v", items, err)
	}
}

func TestStreamKeepsTopIDWhenAllEntriesDisappear(t *testing.T) {
	s, _ := NewWithShards(4)
	if _, _, err := s.StreamAdd("events", "9-0", streamFields("a", "b"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.StreamDelete("events", []StreamID{{Millis: 9}})
	if err != nil || deleted != 1 {
		t.Fatalf("delete=%d err=%v", deleted, err)
	}
	if got := s.Type("events"); got != "stream" {
		t.Fatalf("empty stream type=%q", got)
	}
	if n, _ := s.StreamLen("events"); n != 0 {
		t.Fatalf("empty stream len=%d", n)
	}
	if _, _, err := s.StreamAdd("events", "8-0", streamFields("a", "c"), StreamAddOptions{}); err == nil {
		t.Fatal("XADD reused ID below deleted top ID")
	}
	id, _, err := s.StreamAdd("events", "9-*", streamFields("a", "d"), StreamAddOptions{})
	if err != nil || id.String() != "9-1" {
		t.Fatalf("post-delete partial ID=%s err=%v", id.String(), err)
	}
}

func TestStreamIDsAndNoMkStream(t *testing.T) {
	s, _ := NewWithShards(4)
	if _, applied, err := s.StreamAdd("missing", "*", streamFields("a", "b"), StreamAddOptions{NoMkStream: true}); err != nil || applied {
		t.Fatalf("NOMKSTREAM applied=%t err=%v", applied, err)
	}
	if _, _, err := s.StreamAdd("events", "0-0", streamFields("a", "b"), StreamAddOptions{}); err == nil {
		t.Fatal("0-0 accepted")
	}
	if _, _, err := s.StreamAdd("events", "5-0", streamFields("a", "b"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StreamAdd("events", "5-0", streamFields("a", "c"), StreamAddOptions{}); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	id, _, err := s.StreamAdd("events", "5-*", streamFields("a", "d"), StreamAddOptions{})
	if err != nil || id.String() != "5-1" {
		t.Fatalf("partial ID = %s err=%v", id.String(), err)
	}
}

func TestStreamPreservesTTLAndRename(t *testing.T) {
	s, _ := NewWithShards(4)
	if _, _, err := s.StreamAdd("events", "1-0", streamFields("a", "b"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("events", time.Hour) {
		t.Fatal("EXPIRE failed")
	}
	if _, _, err := s.StreamAdd("events", "2-0", streamFields("c", "d"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if ttl := s.TTL("events"); ttl <= 0 {
		t.Fatalf("TTL lost: %d", ttl)
	}
	handled, renamed, err := s.RenameStream("events", "renamed", false)
	if err != nil || !handled || !renamed {
		t.Fatalf("rename handled=%t renamed=%t err=%v", handled, renamed, err)
	}
	if s.Type("events") != "none" || s.Type("renamed") != "stream" || s.TTL("renamed") <= 0 {
		t.Fatalf("rename state source=%s destination=%s ttl=%d", s.Type("events"), s.Type("renamed"), s.TTL("renamed"))
	}
}

func TestStreamWrongType(t *testing.T) {
	s, _ := NewWithShards(4)
	if err := s.Set("key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamLen("key"); err == nil {
		t.Fatal("XLEN accepted string")
	}
}
