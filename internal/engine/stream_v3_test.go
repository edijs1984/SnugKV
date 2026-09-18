package engine

import (
	"testing"
	"time"
)

func TestStreamV3LifetimeMetadataAndMinID(t *testing.T) {
	s := New()
	field := func(v string) []StreamField {
		return []StreamField{{Field: []byte("v"), Value: []byte(v)}}
	}
	for i, id := range []string{"1-0", "2-0", "3-0", "4-0"} {
		if _, applied, err := s.StreamAdd("events", id, field(id), StreamAddOptions{}); err != nil || !applied {
			t.Fatalf("add %d = applied:%v err:%v", i, applied, err)
		}
	}
	if deleted, err := s.StreamDelete("events", []StreamID{{Millis: 2}}); err != nil || deleted != 1 {
		t.Fatalf("delete = %d, %v", deleted, err)
	}
	if trimmed, err := s.StreamTrimMinID("events", StreamID{Millis: 4}, 0); err != nil || trimmed != 2 {
		t.Fatalf("minid trim = %d, %v", trimmed, err)
	}
	if _, applied, err := s.StreamAdd("events", "5-0", field("five"), StreamAddOptions{HasMinID: true, MinID: StreamID{Millis: 5}}); err != nil || !applied {
		t.Fatalf("xadd minid = applied:%v err:%v", applied, err)
	}
	for _, id := range []string{"6-0", "7-0"} {
		if _, applied, err := s.StreamAdd("events", id, field(id), StreamAddOptions{}); err != nil || !applied {
			t.Fatalf("add %s = applied:%v err:%v", id, applied, err)
		}
	}
	if trimmed, err := s.StreamTrimMinID("events", StreamID{Millis: 8}, 1); err != nil || trimmed != 1 {
		t.Fatalf("limited minid trim = %d, %v", trimmed, err)
	}

	info, err := s.StreamInfo("events", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if info.EntriesAdded != 7 || info.Length != 2 || info.MaxDeletedEntryID != (StreamID{Millis: 2}) {
		t.Fatalf("info = %+v", info)
	}
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, 0); err != nil {
		t.Fatal(err)
	}
	groups, err := s.StreamGroupsInfo("events")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Lag == nil || *groups[0].Lag != 7 {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestStreamV3ConsumerIdleAndInactiveDiverge(t *testing.T) {
	s := New()
	now := time.UnixMilli(1000)
	s.now = func() time.Time { return now }
	fields := []StreamField{{Field: []byte("v"), Value: []byte("one")}}
	if _, _, err := s.StreamAdd("events", "1-0", fields, StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 1, false); err != nil {
		t.Fatal(err)
	}
	now = time.UnixMilli(2000)
	if result, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 1, false); err != nil || len(result) != 0 {
		t.Fatalf("empty read = %+v, %v", result, err)
	}
	consumers, err := s.StreamConsumersInfo("events", "workers")
	if err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 1 || consumers[0].IdleMillis != 0 || consumers[0].InactiveMillis != 1000 || consumers[0].SeenTime != 2000 || consumers[0].ActiveTime != 1000 {
		t.Fatalf("consumer info = %+v", consumers)
	}
}
