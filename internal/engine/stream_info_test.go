package engine

import (
	"testing"
	"time"
)

func TestStreamInfoSummaryGroupsConsumersAndFull(t *testing.T) {
	s := New()
	now := time.UnixMilli(10_000)
	s.now = func() time.Time { return now }

	for i := uint64(1); i <= 3; i++ {
		id := StreamID{Millis: i}
		if _, _, err := s.StreamAdd("events", id.String(), []StreamField{{Field: []byte("v"), Value: []byte{byte('0' + i)}}}, StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, 0); err != nil {
		t.Fatal(err)
	}
	results, err := s.StreamGroupRead(
		[]string{"events"},
		"workers",
		"worker-1",
		[]StreamGroupReadCursor{{New: true}},
		1,
		false,
	)
	if err != nil || len(results) != 1 || len(results[0].Entries) != 1 {
		t.Fatalf("group read = %#v, %v", results, err)
	}

	now = time.UnixMilli(12_000)
	info, err := s.StreamInfo("events", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if info.Length != 3 || info.EntriesAdded != 3 || info.Groups != 1 {
		t.Fatalf("summary counts = %#v", info)
	}
	if info.LastGeneratedID.String() != "3-0" || info.RecordedFirstEntryID.String() != "1-0" {
		t.Fatalf("summary IDs = last:%s first:%s", info.LastGeneratedID, info.RecordedFirstEntryID)
	}
	if info.FirstEntry == nil || info.FirstEntry.ID.String() != "1-0" || info.LastEntry == nil || info.LastEntry.ID.String() != "3-0" {
		t.Fatalf("summary boundary entries = %#v %#v", info.FirstEntry, info.LastEntry)
	}

	groups, err := s.StreamGroupsInfo("events")
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %#v, %v", groups, err)
	}
	group := groups[0]
	if group.Name != "workers" || group.Consumers != 1 || group.Pending != 1 || group.LastDeliveredID.String() != "1-0" {
		t.Fatalf("group = %#v", group)
	}
	if group.EntriesRead == nil || *group.EntriesRead != 1 || group.Lag == nil || *group.Lag != 2 {
		t.Fatalf("group read/lag = %#v", group)
	}

	consumers, err := s.StreamConsumersInfo("events", "workers")
	if err != nil || len(consumers) != 1 {
		t.Fatalf("consumers = %#v, %v", consumers, err)
	}
	consumer := consumers[0]
	if consumer.Name != "worker-1" || consumer.Pending != 1 || consumer.IdleMillis != 2000 || consumer.InactiveMillis != 2000 {
		t.Fatalf("consumer = %#v", consumer)
	}

	full, err := s.StreamInfo("events", true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Entries) != 1 || full.Entries[0].ID.String() != "1-0" {
		t.Fatalf("full entries = %#v", full.Entries)
	}
	if len(full.GroupInfos) != 1 || len(full.GroupInfos[0].PendingEntries) != 1 || len(full.GroupInfos[0].ConsumerInfos) != 1 || len(full.GroupInfos[0].ConsumerInfos[0].PendingEntries) != 1 {
		t.Fatalf("full group info = %#v", full.GroupInfos)
	}
}

func TestStreamInfoErrors(t *testing.T) {
	s := New()
	if _, err := s.StreamInfo("missing", false, 10); err == nil || err.Error() != "ERR no such key" {
		t.Fatalf("missing error = %v", err)
	}
	if err := s.Set("plain", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamInfo("plain", false, 10); err == nil || err.Error()[:9] != "WRONGTYPE" {
		t.Fatalf("wrongtype error = %v", err)
	}
}
