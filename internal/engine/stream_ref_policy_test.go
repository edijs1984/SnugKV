package engine

import "testing"

func prepareStreamPolicyState(t *testing.T, groups ...string) *Store {
	t.Helper()
	s, err := NewWithShards(4)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1-0", "2-0", "3-0", "4-0"} {
		if _, _, err := s.StreamAdd("events", id, streamFields("v", id), StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range groups {
		if err := s.StreamGroupCreate("events", group, "0-0", false, 0); err != nil {
			t.Fatal(err)
		}
		results, err := s.StreamGroupRead(
			[]string{"events"}, group, "c-"+group,
			[]StreamGroupReadCursor{{New: true}}, 100, false,
		)
		if err != nil || len(results) != 1 || len(results[0].Entries) != 4 {
			t.Fatalf("group read %s = %#v, %v", group, results, err)
		}
	}
	return s
}

func pendingCount(t *testing.T, s *Store, group string) int64 {
	t.Helper()
	summary, err := s.StreamGroupPendingSummary("events", group)
	if err != nil {
		t.Fatal(err)
	}
	return summary.Count
}

func TestStreamDeleteExReferencePolicies(t *testing.T) {
	s := prepareStreamPolicyState(t, "g1", "g2")

	statuses, err := s.StreamDeleteEx("events", []StreamID{{Millis: 1}}, StreamRefKeep)
	if err != nil || len(statuses) != 1 || statuses[0] != 1 {
		t.Fatalf("KEEPREF statuses=%v err=%v", statuses, err)
	}
	if pendingCount(t, s, "g1") != 4 || pendingCount(t, s, "g2") != 4 {
		t.Fatal("KEEPREF removed PEL references")
	}

	statuses, err = s.StreamDeleteEx("events", []StreamID{{Millis: 1}}, StreamRefDelete)
	if err != nil || statuses[0] != -1 {
		t.Fatalf("dangling DELREF statuses=%v err=%v", statuses, err)
	}
	if pendingCount(t, s, "g1") != 3 || pendingCount(t, s, "g2") != 3 {
		t.Fatal("DELREF did not clean dangling references")
	}

	statuses, err = s.StreamDeleteEx("events", []StreamID{{Millis: 2}}, StreamRefAcked)
	if err != nil || statuses[0] != 2 {
		t.Fatalf("pending ACKED statuses=%v err=%v", statuses, err)
	}
	if _, err := s.StreamGroupAck("events", "g1", []StreamID{{Millis: 2}}); err != nil {
		t.Fatal(err)
	}
	statuses, err = s.StreamDeleteEx("events", []StreamID{{Millis: 2}}, StreamRefAcked)
	if err != nil || statuses[0] != 2 {
		t.Fatalf("partially acked statuses=%v err=%v", statuses, err)
	}
	if _, err := s.StreamGroupAck("events", "g2", []StreamID{{Millis: 2}}); err != nil {
		t.Fatal(err)
	}
	statuses, err = s.StreamDeleteEx("events", []StreamID{{Millis: 2}}, StreamRefAcked)
	if err != nil || statuses[0] != 1 {
		t.Fatalf("fully acked statuses=%v err=%v", statuses, err)
	}

	noGroups := prepareStreamPolicyState(t)
	statuses, err = noGroups.StreamDeleteEx("events", []StreamID{{Millis: 1}}, StreamRefAcked)
	if err != nil || statuses[0] != 2 {
		t.Fatalf("no-group ACKED statuses=%v err=%v", statuses, err)
	}
}

func TestStreamAckDeleteReferencePolicies(t *testing.T) {
	s := prepareStreamPolicyState(t, "g1", "g2")

	statuses, err := s.StreamAckDelete("events", "g1", []StreamID{{Millis: 1}}, StreamRefAcked)
	if err != nil || statuses[0] != 2 {
		t.Fatalf("first ACKED statuses=%v err=%v", statuses, err)
	}
	if pendingCount(t, s, "g1") != 3 || pendingCount(t, s, "g2") != 4 {
		t.Fatal("XACKDEL did not acknowledge target group only")
	}
	statuses, err = s.StreamAckDelete("events", "g2", []StreamID{{Millis: 1}}, StreamRefAcked)
	if err != nil || statuses[0] != 1 {
		t.Fatalf("final ACKED statuses=%v err=%v", statuses, err)
	}

	statuses, err = s.StreamAckDelete("events", "g1", []StreamID{{Millis: 2}}, StreamRefKeep)
	if err != nil || statuses[0] != 1 {
		t.Fatalf("KEEPREF statuses=%v err=%v", statuses, err)
	}
	if pendingCount(t, s, "g1") != 2 || pendingCount(t, s, "g2") != 3 {
		t.Fatal("KEEPREF did not preserve other-group PEL reference")
	}
	statuses, err = s.StreamAckDelete("events", "g2", []StreamID{{Millis: 2}}, StreamRefDelete)
	if err != nil || statuses[0] != 1 {
		t.Fatalf("dangling DELREF statuses=%v err=%v", statuses, err)
	}
	if pendingCount(t, s, "g2") != 2 {
		t.Fatal("XACKDEL DELREF did not clean dangling reference")
	}
}

func TestStreamTrimReferencePolicies(t *testing.T) {
	keep := prepareStreamPolicyState(t, "g1")
	trimmed, err := keep.StreamTrimMaxLenWithPolicy("events", 2, 0, StreamRefKeep)
	if err != nil || trimmed != 2 || pendingCount(t, keep, "g1") != 4 {
		t.Fatalf("KEEPREF trim=%d pending=%d err=%v", trimmed, pendingCount(t, keep, "g1"), err)
	}

	del := prepareStreamPolicyState(t, "g1")
	trimmed, err = del.StreamTrimMaxLenWithPolicy("events", 2, 0, StreamRefDelete)
	if err != nil || trimmed != 2 || pendingCount(t, del, "g1") != 2 {
		t.Fatalf("DELREF trim=%d pending=%d err=%v", trimmed, pendingCount(t, del, "g1"), err)
	}

	acked := prepareStreamPolicyState(t, "g1", "g2")
	if _, err := acked.StreamGroupAck("events", "g1", []StreamID{{Millis: 1}, {Millis: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := acked.StreamGroupAck("events", "g2", []StreamID{{Millis: 1}}); err != nil {
		t.Fatal(err)
	}
	trimmed, err = acked.StreamTrimMaxLenWithPolicy("events", 2, 0, StreamRefAcked)
	if err != nil || trimmed != 1 {
		t.Fatalf("ACKED trim=%d err=%v", trimmed, err)
	}
	length, err := acked.StreamLen("events")
	if err != nil || length != 3 {
		t.Fatalf("ACKED len=%d err=%v", length, err)
	}

	noGroups := prepareStreamPolicyState(t)
	trimmed, err = noGroups.StreamTrimMinIDWithPolicy("events", StreamID{Millis: 3}, 0, StreamRefAcked)
	if err != nil || trimmed != 2 {
		t.Fatalf("no-group ACKED trim=%d err=%v", trimmed, err)
	}
}

func TestStreamAddWithDeleteRefTrimming(t *testing.T) {
	s := prepareStreamPolicyState(t, "g1")
	id, applied, err := s.StreamAddWithPolicy(
		"events", "5-0", streamFields("v", "5-0"),
		StreamAddOptions{HasMaxLen: true, MaxLen: 2}, StreamRefDelete,
	)
	if err != nil || !applied || id.String() != "5-0" {
		t.Fatalf("XADD id=%s applied=%t err=%v", id.String(), applied, err)
	}
	if pendingCount(t, s, "g1") != 1 {
		t.Fatalf("PEL after XADD DELREF = %d, want 1", pendingCount(t, s, "g1"))
	}
	info, err := s.StreamInfo("events", false, 0)
	if err != nil || info.Length != 2 || info.MaxDeletedEntryID.String() != "0-0" {
		t.Fatalf("stream info=%#v err=%v", info, err)
	}
}
