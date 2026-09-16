package engine

import "testing"

func TestStreamExportRestorePreservesEntriesAndTopID(t *testing.T) {
	source, _ := NewWithShards(4)
	if _, _, err := source.StreamAdd("events", "10-0", streamFields("type", "created"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.StreamAdd("events", "11-0", streamFields("type", "updated"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := source.StreamDelete("events", []StreamID{{Millis: 11}}); err != nil || deleted != 1 {
		t.Fatalf("delete=%d err=%v", deleted, err)
	}

	records := source.Export([]string{"events"})
	if len(records) != 1 || ValueType(records[0].ValueType) != TypeStream {
		t.Fatalf("export=%#v", records)
	}

	restored, _ := NewWithShards(4)
	if err := restored.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	if got := restored.Type("events"); got != "stream" {
		t.Fatalf("restored TYPE=%q", got)
	}
	start, _ := ParseStreamRangeBound("-", true)
	end, _ := ParseStreamRangeBound("+", false)
	items, err := restored.StreamRange("events", start, end, 10, false)
	if err != nil || len(items) != 1 || items[0].ID.String() != "10-0" {
		t.Fatalf("restored entries=%#v err=%v", items, err)
	}
	if _, _, err := restored.StreamAdd("events", "10-1", streamFields("type", "invalid"), StreamAddOptions{}); err == nil {
		t.Fatal("restore lost last-generated ID 11-0")
	}
	id, _, err := restored.StreamAdd("events", "11-*", streamFields("type", "next"), StreamAddOptions{})
	if err != nil || id.String() != "11-1" {
		t.Fatalf("post-restore ID=%s err=%v", id.String(), err)
	}
}
