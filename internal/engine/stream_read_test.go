package engine

import "testing"

func TestStreamReadAfterMultipleStreamsAndCount(t *testing.T) {
	s, _ := NewWithShards(4)
	for _, tc := range []struct {
		key, id, value string
	}{
		{"a", "1-0", "a1"},
		{"a", "2-0", "a2"},
		{"a", "3-0", "a3"},
		{"b", "5-0", "b5"},
		{"b", "6-0", "b6"},
	} {
		if _, _, err := s.StreamAdd(tc.key, tc.id, streamFields("v", tc.value), StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	results, err := s.StreamReadAfter(
		[]string{"a", "b", "missing"},
		[]StreamID{{Millis: 1}, {Millis: 5}, {}},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results=%#v", results)
	}
	if results[0].Key != "a" || len(results[0].Entries) != 1 || results[0].Entries[0].ID.String() != "2-0" {
		t.Fatalf("stream a=%#v", results[0])
	}
	if results[1].Key != "b" || len(results[1].Entries) != 1 || results[1].Entries[0].ID.String() != "6-0" {
		t.Fatalf("stream b=%#v", results[1])
	}
}

func TestResolveStreamReadLatestCursor(t *testing.T) {
	s, _ := NewWithShards(4)
	if _, _, err := s.StreamAdd("events", "7-2", streamFields("v", "old"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	cursor, err := ParseStreamReadCursor("$")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.ResolveStreamReadCursors([]string{"events", "missing"}, []StreamReadCursor{cursor, cursor})
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].String() != "7-2" || resolved[1].String() != "0-0" {
		t.Fatalf("resolved=%v", resolved)
	}
	if _, _, err := s.StreamAdd("events", "7-3", streamFields("v", "new"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	results, err := s.StreamReadAfter([]string{"events"}, resolved[:1], 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Entries) != 1 || results[0].Entries[0].ID.String() != "7-3" {
		t.Fatalf("results=%#v", results)
	}
}

func TestParseStreamReadCursor(t *testing.T) {
	cursor, err := ParseStreamReadCursor("5")
	if err != nil || cursor.Latest || cursor.ID.String() != "5-0" {
		t.Fatalf("single millisecond cursor=%#v err=%v", cursor, err)
	}
	cursor, err = ParseStreamReadCursor("5-9")
	if err != nil || cursor.Latest || cursor.ID.String() != "5-9" {
		t.Fatalf("full cursor=%#v err=%v", cursor, err)
	}
	cursor, err = ParseStreamReadCursor("$")
	if err != nil || !cursor.Latest {
		t.Fatalf("latest cursor=%#v err=%v", cursor, err)
	}
	if _, err := ParseStreamReadCursor("bad"); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestStreamReadWrongType(t *testing.T) {
	s, _ := NewWithShards(4)
	if err := s.Set("plain", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamReadAfter([]string{"plain"}, []StreamID{{}}, 1); err == nil {
		t.Fatal("XREAD accepted string key")
	}
	latest, _ := ParseStreamReadCursor("$")
	if _, err := s.ResolveStreamReadCursors([]string{"plain"}, []StreamReadCursor{latest}); err == nil {
		t.Fatal("XREAD $ accepted string key")
	}
}
