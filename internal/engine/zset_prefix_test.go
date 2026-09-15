package engine

import "testing"

func TestZSetMemberPrefixEncoding(t *testing.T) {
	items := []ZSetItem{
		zitem(0, "m:00000000xxxxxx"),
		zitem(1, "m:00000001xxxxxx"),
		zitem(2, "m:00000002xxxxxx"),
		zitem(3, "m:00000003xxxxxx"),
	}

	packed, err := encodePackedZSet(items)
	if err != nil {
		t.Fatal(err)
	}
	if got := zsetEncodingName(packed); got != "packed-int-delta-prefix" {
		t.Fatalf("encoding=%q", got)
	}

	// Score-only SZ2 would be 4 + 4 score bytes + 4*(1+16) = 76 bytes.
	if len(packed) >= 76 {
		t.Fatalf("prefix encoding did not improve storage: %d", len(packed))
	}

	decoded, err := decodePackedZSet(packed)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(items) {
		t.Fatalf("decoded len=%d want=%d", len(decoded), len(items))
	}
	for i := range items {
		if decoded[i].Score != items[i].Score || string(decoded[i].Member) != string(items[i].Member) {
			t.Fatalf("decoded[%d]=%v want=%v", i, decoded[i], items[i])
		}
	}
}

func TestZSetMemberPrefixFallbackForDispersedMembers(t *testing.T) {
	items := []ZSetItem{
		zitem(0, "aaaaaaaaaaaaaaaa"),
		zitem(1, "bbbbbbbbbbbbbbbb"),
		zitem(2, "cccccccccccccccc"),
		zitem(3, "dddddddddddddddd"),
	}
	packed, err := encodePackedZSet(items)
	if err != nil {
		t.Fatal(err)
	}
	if got := zsetEncodingName(packed); got != "packed-int-delta" {
		t.Fatalf("encoding=%q", got)
	}
	if len(packed) != 76 {
		t.Fatalf("packed bytes=%d want=76", len(packed))
	}
}

func TestZSetFloatScoresCanUseMemberPrefix(t *testing.T) {
	items := []ZSetItem{
		zitem(1.25, "user:0000000001"),
		zitem(2.50, "user:0000000002"),
		zitem(3.75, "user:0000000003"),
	}
	packed, err := encodePackedZSet(items)
	if err != nil {
		t.Fatal(err)
	}
	if got := zsetEncodingName(packed); got != "packed-float64-prefix" {
		t.Fatalf("encoding=%q", got)
	}
	decoded, err := decodePackedZSet(packed)
	if err != nil {
		t.Fatal(err)
	}
	for i := range items {
		if decoded[i].Score != items[i].Score || string(decoded[i].Member) != string(items[i].Member) {
			t.Fatalf("decoded[%d]=%v want=%v", i, decoded[i], items[i])
		}
	}
}

func TestZSetPrefixEncodingSurvivesStoreExportRestore(t *testing.T) {
	s := New()
	items := []ZSetItem{
		zitem(10, "account:00000001"),
		zitem(20, "account:00000002"),
		zitem(30, "account:00000003"),
	}
	if _, _, _, err := s.ZSetAdd("z", items, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	stats, found, err := s.ZSetStorageStats("z")
	if err != nil || !found {
		t.Fatalf("stats found=%v err=%v", found, err)
	}
	if stats.Encoding != "packed-int-delta-prefix" {
		t.Fatalf("encoding=%q", stats.Encoding)
	}

	records := s.Export([]string{"z"})
	restored := New()
	if err := restored.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	got, err := restored.ZSetRange("z", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(items) {
		t.Fatalf("restored len=%d want=%d", len(got), len(items))
	}
	for i := range items {
		if got[i].Score != items[i].Score || string(got[i].Member) != string(items[i].Member) {
			t.Fatalf("restored[%d]=%v want=%v", i, got[i], items[i])
		}
	}
}
