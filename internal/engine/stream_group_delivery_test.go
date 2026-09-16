package engine

import (
	"testing"
	"time"
)

func addTestStreamEntry(t *testing.T, s *Store, key, id string) {
	t.Helper()
	if _, _, err := s.StreamAdd(key, id, []StreamField{{Field: []byte("v"), Value: []byte(id)}}, StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestStreamGroupReadPendingHistoryAndAck(t *testing.T) {
	s := New()
	now := time.UnixMilli(1000)
	s.now = func() time.Time { return now }
	addTestStreamEntry(t, s, "events", "1-0")
	addTestStreamEntry(t, s, "events", "2-0")
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, 0); err != nil {
		t.Fatal(err)
	}

	results, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Entries) != 1 || results[0].Entries[0].ID.String() != "1-0" {
		t.Fatalf("new read = %#v", results)
	}

	summary, err := s.StreamGroupPendingSummary("events", "workers")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Count != 1 || len(summary.Consumers) != 1 || summary.Consumers[0].Name != "c1" || summary.Consumers[0].Count != 1 {
		t.Fatalf("summary = %#v", summary)
	}

	now = now.Add(5 * time.Second)
	start, _ := ParseStreamRangeBound("-", true)
	end, _ := ParseStreamRangeBound("+", false)
	pending, err := s.StreamGroupPendingRange("events", "workers", start, end, 10, "c1", 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].IdleMillis != 5000 || pending[0].Deliveries != 1 {
		t.Fatalf("pending before history = %#v", pending)
	}

	history, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{ID: StreamID{}}}, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || len(history[0].Entries) != 1 || history[0].Entries[0].ID.String() != "1-0" {
		t.Fatalf("history = %#v", history)
	}
	pending, err = s.StreamGroupPendingRange("events", "workers", start, end, 10, "c1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].IdleMillis != 0 || pending[0].Deliveries != 2 {
		t.Fatalf("pending after history = %#v", pending)
	}

	acked, err := s.StreamGroupAck("events", "workers", []StreamID{{Millis: 1}})
	if err != nil || acked != 1 {
		t.Fatalf("ack = %d, %v", acked, err)
	}
	summary, err = s.StreamGroupPendingSummary("events", "workers")
	if err != nil || summary.Count != 0 {
		t.Fatalf("summary after ack = %#v, %v", summary, err)
	}
}

func TestStreamGroupReadNOACKAdvancesGroupWithoutPending(t *testing.T) {
	s := New()
	addTestStreamEntry(t, s, "events", "1-0")
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, 0); err != nil {
		t.Fatal(err)
	}
	results, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 10, true)
	if err != nil || len(results) != 1 || len(results[0].Entries) != 1 {
		t.Fatalf("NOACK read = %#v, %v", results, err)
	}
	summary, err := s.StreamGroupPendingSummary("events", "workers")
	if err != nil || summary.Count != 0 {
		t.Fatalf("NOACK pending = %#v, %v", summary, err)
	}
	results, err = s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 10, false)
	if err != nil || len(results) != 0 {
		t.Fatalf("second new read = %#v, %v", results, err)
	}
}

func TestStreamGroupHistoryReturnsNullPayloadForDeletedEntry(t *testing.T) {
	s := New()
	addTestStreamEntry(t, s, "events", "1-0")
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 10, false); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.StreamDelete("events", []StreamID{{Millis: 1}}); err != nil || deleted != 1 {
		t.Fatalf("delete = %d, %v", deleted, err)
	}
	results, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{ID: StreamID{}}}, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Entries) != 1 || results[0].Entries[0].Fields != nil {
		t.Fatalf("deleted pending history = %#v", results)
	}
}

func TestStreamGroupDeleteConsumerOrphansPendingUntilAck(t *testing.T) {
	s := New()
	addTestStreamEntry(t, s, "events", "1-0")
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 10, false); err != nil {
		t.Fatal(err)
	}
	orphaned, err := s.StreamGroupDeleteConsumer("events", "workers", "c1")
	if err != nil || orphaned != 1 {
		t.Fatalf("delete consumer = %d, %v", orphaned, err)
	}
	summary, err := s.StreamGroupPendingSummary("events", "workers")
	if err != nil || summary.Count != 1 || len(summary.Consumers) != 0 {
		t.Fatalf("orphan summary = %#v, %v", summary, err)
	}
	acked, err := s.StreamGroupAck("events", "workers", []StreamID{{Millis: 1}})
	if err != nil || acked != 1 {
		t.Fatalf("ack orphan = %d, %v", acked, err)
	}
}
