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

func TestStreamGroupInfoInfersEntriesReadFromZero(t *testing.T) {
	s := New()

	if _, _, err := s.StreamAdd(
		"events",
		"*",
		[]StreamField{
			{
				Field: []byte("f"),
				Value: []byte("1"),
			},
		},
		StreamAddOptions{},
	); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.StreamAdd(
		"events",
		"*",
		[]StreamField{
			{
				Field: []byte("f"),
				Value: []byte("2"),
			},
		},
		StreamAddOptions{},
	); err != nil {
		t.Fatal(err)
	}

	if err := s.StreamGroupCreate(
		"events",
		"workers",
		"0-0",
		false,
		-1,
	); err != nil {
		t.Fatal(err)
	}

	groups, err :=
		s.StreamGroupsInfo("events")
	if err != nil {
		t.Fatal(err)
	}

	if len(groups) != 1 {
		t.Fatalf(
			"groups = %d, want 1",
			len(groups),
		)
	}

	if groups[0].EntriesRead != nil {
		t.Fatalf(
			"entries-read = %#v, want nil",
			groups[0].EntriesRead,
		)
	}

	if groups[0].Lag == nil ||
		*groups[0].Lag != 2 {
		t.Fatalf(
			"lag = %#v, want 2",
			groups[0].Lag,
		)
	}

	results, err := s.StreamGroupRead(
		[]string{"events"},
		"workers",
		"c1",
		[]StreamGroupReadCursor{
			{New: true},
		},
		1,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 ||
		len(results[0].Entries) != 1 {
		t.Fatalf(
			"results = %#v",
			results,
		)
	}

	groups, err =
		s.StreamGroupsInfo("events")
	if err != nil {
		t.Fatal(err)
	}

	if groups[0].EntriesRead == nil ||
		*groups[0].EntriesRead != 1 {
		t.Fatalf(
			"entries-read after read = %#v, want 1",
			groups[0].EntriesRead,
		)
	}

	if groups[0].Lag == nil ||
		*groups[0].Lag != 1 {
		t.Fatalf(
			"lag after read = %#v, want 1",
			groups[0].Lag,
		)
	}
}

func TestStreamGroupUnknownEntriesReadRecoversAtEnd(t *testing.T) {
	s := New()

	first, _, err := s.StreamAdd(
		"events",
		"*",
		[]StreamField{
			{
				Field: []byte("f"),
				Value: []byte("1"),
			},
		},
		StreamAddOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.StreamAdd(
		"events",
		"*",
		[]StreamField{
			{
				Field: []byte("f"),
				Value: []byte("2"),
			},
		},
		StreamAddOptions{},
	); err != nil {
		t.Fatal(err)
	}

	// An arbitrary ID intentionally leaves entries-read unknown.
	arbitrary := first.String()

	if err := s.StreamGroupCreate(
		"events",
		"workers",
		arbitrary,
		false,
		-1,
	); err != nil {
		t.Fatal(err)
	}

	// Force the unknown state for this recovery test because a first-entry
	// position may itself be inferable on a pristine stream.
	state, _, err :=
		s.streamInfoState("events")
	if err != nil {
		t.Fatal(err)
	}

	if len(state.Groups) != 1 {
		t.Fatal("missing group")
	}

	key := "events"
	sh := s.shardFor(key)
	sh.mu.Lock()

	entry, ok := sh.get(key)
	if !ok {
		sh.mu.Unlock()
		t.Fatal("missing stream")
	}

	state.Groups[0].EntriesRead = -1

	if err := s.publishStreamStateLocked(
		sh,
		key,
		entry,
		state,
	); err != nil {
		sh.mu.Unlock()
		t.Fatal(err)
	}

	sh.mu.Unlock()

	if _, err := s.StreamGroupRead(
		[]string{"events"},
		"workers",
		"c1",
		[]StreamGroupReadCursor{
			{New: true},
		},
		10,
		false,
	); err != nil {
		t.Fatal(err)
	}

	groups, err :=
		s.StreamGroupsInfo("events")
	if err != nil {
		t.Fatal(err)
	}

	if groups[0].EntriesRead == nil ||
		*groups[0].EntriesRead != 2 {
		t.Fatalf(
			"recovered entries-read = %#v, want 2",
			groups[0].EntriesRead,
		)
	}

	if groups[0].Lag == nil ||
		*groups[0].Lag != 0 {
		t.Fatalf(
			"recovered lag = %#v, want 0",
			groups[0].Lag,
		)
	}
}
