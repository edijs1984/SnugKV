package engine

import (
	"encoding/binary"
	"strings"
	"testing"
)

func legacyEmptyStreamV1(last StreamID) []byte {
	out := []byte{'S', 'X', 1}
	var fixed [16]byte
	binary.BigEndian.PutUint64(fixed[:8], last.Millis)
	binary.BigEndian.PutUint64(fixed[8:], last.Sequence)
	out = append(out, fixed[:]...)
	out = appendStreamUvarint(out, 0)
	return out
}

func legacyEmptyStreamV2(last StreamID) []byte {
	out := []byte{'S', 'X', 2}
	out = appendStreamID(out, last)
	out = appendStreamUvarint(out, 0) // entries
	out = appendStreamUvarint(out, 0) // groups
	return out
}

func TestPackedStreamConsumerGroupsRoundTripAndV1Compatibility(t *testing.T) {
	for name, legacy := range map[string][]byte{
		"v1": legacyEmptyStreamV1(StreamID{Millis: 7, Sequence: 2}),
		"v2": legacyEmptyStreamV2(StreamID{Millis: 7, Sequence: 2}),
	} {
		decoded, err := decodePackedStream(legacy)
		if err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
		if decoded.LastID != (StreamID{Millis: 7, Sequence: 2}) || decoded.EntriesAdded != 0 || len(decoded.Groups) != 0 {
			t.Fatalf("%s legacy decode = %#v", name, decoded)
		}
	}

	state := packedStream{
		LastID:       StreamID{Millis: 9},
		EntriesAdded: 4,
		Groups: []streamGroup{{
			Name:            "workers",
			LastDeliveredID: StreamID{Millis: 8, Sequence: 3},
			EntriesRead:     4,
			Consumers:       []streamConsumer{{Name: "c1", SeenAt: 1234, ActiveAt: 1200}},
			Pending: []streamPending{{
				ID:          StreamID{Millis: 8, Sequence: 3},
				Consumer:    "c1",
				DeliveredAt: 1200,
				Deliveries:  2,
			}},
		}},
	}
	packed, err := encodePackedStream(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(packed) < 3 || packed[2] != packedStreamHeaderV3[2] {
		t.Fatalf("packed version = %v", packed[:3])
	}
	roundTrip, err := decodePackedStream(packed)
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.EntriesAdded != 4 || len(roundTrip.Groups) != 1 || roundTrip.Groups[0].Name != "workers" || roundTrip.Groups[0].EntriesRead != 4 {
		t.Fatalf("round trip groups = %#v", roundTrip)
	}
	if len(roundTrip.Groups[0].Consumers) != 1 || len(roundTrip.Groups[0].Pending) != 1 {
		t.Fatalf("round trip group metadata = %#v", roundTrip.Groups[0])
	}
	consumer := roundTrip.Groups[0].Consumers[0]
	if consumer.SeenAt != 1234 || consumer.ActiveAt != 1200 {
		t.Fatalf("round trip consumer = %#v", consumer)
	}
}

func TestStreamGroupLifecycleAndPersistence(t *testing.T) {
	s := New()
	if _, _, err := s.StreamAdd("events", "1-0", []StreamField{{Field: []byte("type"), Value: []byte("login")}}, StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.StreamGroupCreate("events", "workers", "$", false, -1); err != nil {
		t.Fatal(err)
	}
	if err := s.StreamGroupCreate("events", "workers", "0-0", false, -1); err == nil || !strings.Contains(err.Error(), "BUSYGROUP") {
		t.Fatalf("duplicate group error = %v", err)
	}
	created, err := s.StreamGroupCreateConsumer("events", "workers", "c1")
	if err != nil || created != 1 {
		t.Fatalf("create consumer = %d, %v", created, err)
	}
	created, err = s.StreamGroupCreateConsumer("events", "workers", "c1")
	if err != nil || created != 0 {
		t.Fatalf("repeat create consumer = %d, %v", created, err)
	}
	entriesRead := int64(0)
	if err := s.StreamGroupSetID("events", "workers", "0-0", &entriesRead); err != nil {
		t.Fatal(err)
	}

	records := s.Export([]string{"events"})
	restored := New()
	if err := restored.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	created, err = restored.StreamGroupCreateConsumer("events", "workers", "c1")
	if err != nil || created != 0 {
		t.Fatalf("restored consumer = %d, %v", created, err)
	}
	created, err = restored.StreamGroupCreateConsumer("events", "workers", "c2")
	if err != nil || created != 1 {
		t.Fatalf("restored new consumer = %d, %v", created, err)
	}
	pending, err := restored.StreamGroupDeleteConsumer("events", "workers", "c1")
	if err != nil || pending != 0 {
		t.Fatalf("delete consumer = %d, %v", pending, err)
	}
	destroyed, err := restored.StreamGroupDestroy("events", "workers")
	if err != nil || destroyed != 1 {
		t.Fatalf("destroy group = %d, %v", destroyed, err)
	}
	destroyed, err = restored.StreamGroupDestroy("events", "workers")
	if err != nil || destroyed != 0 {
		t.Fatalf("repeat destroy group = %d, %v", destroyed, err)
	}
}

func TestStreamGroupCreateMKStream(t *testing.T) {
	s := New()
	if err := s.StreamGroupCreate("new-stream", "workers", "$", false, -1); err == nil {
		t.Fatal("expected missing-stream error")
	}
	if err := s.StreamGroupCreate("new-stream", "workers", "$", true, -1); err != nil {
		t.Fatal(err)
	}
	length, err := s.StreamLen("new-stream")
	if err != nil || length != 0 {
		t.Fatalf("stream len = %d, %v", length, err)
	}
	if err := s.StreamGroupSetID("new-stream", "missing", "0-0", nil); err == nil || !strings.HasPrefix(err.Error(), "NOGROUP ") {
		t.Fatalf("missing group error = %v", err)
	}
}
