package engine

import (
	"testing"
	"time"
)

func TestStreamGroupClaimOwnershipIdleAndRetryCount(t *testing.T) {
	s := New()
	now := time.UnixMilli(1000)
	s.now = func() time.Time { return now }
	for _, id := range []string{"1-0", "2-0"} {
		if _, _, err := s.StreamAdd("events", id, []StreamField{{Field: []byte("v"), Value: []byte(id)}}, StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 2, false); err != nil {
		t.Fatal(err)
	}

	now = time.UnixMilli(1100)
	id := StreamID{Millis: 1}
	entries, ids, err := s.StreamGroupClaim("events", "workers", "c2", 50*time.Millisecond, []StreamID{id}, StreamClaimOptions{})
	if err != nil || len(entries) != 1 || len(ids) != 1 || ids[0] != id {
		t.Fatalf("claim = %#v %#v %v", entries, ids, err)
	}
	pending, err := s.StreamGroupPendingRange("events", "workers", StreamRangeBound{}, StreamRangeBound{ID: StreamID{Millis: ^uint64(0), Sequence: ^uint64(0)}}, 10, "c2", 0)
	if err != nil || len(pending) != 1 || pending[0].Deliveries != 2 {
		t.Fatalf("claimed pending = %#v, %v", pending, err)
	}

	now = time.UnixMilli(1200)
	_, ids, err = s.StreamGroupClaim("events", "workers", "c3", 0, []StreamID{id}, StreamClaimOptions{JustID: true})
	if err != nil || len(ids) != 1 {
		t.Fatalf("justid claim = %#v, %v", ids, err)
	}
	pending, err = s.StreamGroupPendingRange("events", "workers", StreamRangeBound{}, StreamRangeBound{ID: StreamID{Millis: ^uint64(0), Sequence: ^uint64(0)}}, 10, "c3", 0)
	if err != nil || len(pending) != 1 || pending[0].Deliveries != 2 {
		t.Fatalf("justid deliveries = %#v, %v", pending, err)
	}

	now = time.UnixMilli(1300)
	zero := uint64(0)
	_, _, err = s.StreamGroupClaim("events", "workers", "c4", 0, []StreamID{id}, StreamClaimOptions{RetryCount: &zero})
	if err != nil {
		t.Fatal(err)
	}
	pending, err = s.StreamGroupPendingRange("events", "workers", StreamRangeBound{}, StreamRangeBound{ID: StreamID{Millis: ^uint64(0), Sequence: ^uint64(0)}}, 10, "c4", 0)
	if err != nil || len(pending) != 1 || pending[0].Deliveries != 0 {
		t.Fatalf("retrycount zero = %#v, %v", pending, err)
	}
}

func TestStreamGroupClaimForceAndDeletedPendingCleanup(t *testing.T) {
	s := New()
	now := time.UnixMilli(1000)
	s.now = func() time.Time { return now }
	for _, id := range []string{"1-0", "2-0"} {
		if _, _, err := s.StreamAdd("events", id, []StreamField{{Field: []byte("v"), Value: []byte(id)}}, StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 1, false); err != nil {
		t.Fatal(err)
	}

	forceID := StreamID{Millis: 2}
	entries, _, err := s.StreamGroupClaim("events", "workers", "c2", 0, []StreamID{forceID}, StreamClaimOptions{Force: true})
	if err != nil || len(entries) != 1 {
		t.Fatalf("force claim = %#v, %v", entries, err)
	}
	if deleted, err := s.StreamDelete("events", []StreamID{{Millis: 1}}); err != nil || deleted != 1 {
		t.Fatalf("delete = %d, %v", deleted, err)
	}
	now = time.UnixMilli(1100)
	entries, ids, err := s.StreamGroupClaim("events", "workers", "c3", 0, []StreamID{{Millis: 1}}, StreamClaimOptions{})
	if err != nil || len(entries) != 0 || len(ids) != 0 {
		t.Fatalf("deleted claim = %#v %#v %v", entries, ids, err)
	}
	summary, err := s.StreamGroupPendingSummary("events", "workers")
	if err != nil || summary.Count != 1 {
		t.Fatalf("pending after cleanup = %#v, %v", summary, err)
	}
}

func TestStreamGroupAutoClaimCursorDeletedAndJustID(t *testing.T) {
	s := New()
	now := time.UnixMilli(1000)
	s.now = func() time.Time { return now }
	for _, id := range []string{"1-0", "2-0", "3-0"} {
		if _, _, err := s.StreamAdd("events", id, []StreamField{{Field: []byte("v"), Value: []byte(id)}}, StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamGroupRead([]string{"events"}, "workers", "c1", []StreamGroupReadCursor{{New: true}}, 3, false); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.StreamDelete("events", []StreamID{{Millis: 2}}); err != nil || deleted != 1 {
		t.Fatalf("delete = %d, %v", deleted, err)
	}
	now = time.UnixMilli(1200)

	first, err := s.StreamGroupAutoClaim("events", "workers", "c2", 100*time.Millisecond, StreamID{}, 1, false)
	if err != nil || len(first.Entries) != 1 || first.IDs[0] != (StreamID{Millis: 1}) || first.Next != (StreamID{Millis: 2}) {
		t.Fatalf("first autoclaim = %#v, %v", first, err)
	}
	second, err := s.StreamGroupAutoClaim("events", "workers", "c2", 100*time.Millisecond, first.Next, 10, true)
	if err != nil || len(second.IDs) != 1 || second.IDs[0] != (StreamID{Millis: 3}) || len(second.DeletedIDs) != 1 || second.DeletedIDs[0] != (StreamID{Millis: 2}) || second.Next != (StreamID{}) {
		t.Fatalf("second autoclaim = %#v, %v", second, err)
	}
	pending, err := s.StreamGroupPendingRange("events", "workers", StreamRangeBound{}, StreamRangeBound{ID: StreamID{Millis: ^uint64(0), Sequence: ^uint64(0)}}, 10, "c2", 0)
	if err != nil || len(pending) != 2 || pending[0].Deliveries != 2 || pending[1].Deliveries != 1 {
		t.Fatalf("autoclaim delivery counts = %#v, %v", pending, err)
	}
}
