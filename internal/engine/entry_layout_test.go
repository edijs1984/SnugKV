package engine

import (
	"testing"
	"time"
)

func TestEntryLayoutStaysCompact(t *testing.T) {
	if got, want := entryStructBytes, uint64(40); got != want {
		t.Fatalf("entry struct size = %d, want %d", got, want)
	}
	if got, want := entryMetaBytes, uint64(32); got != want {
		t.Fatalf("entry metadata size = %d, want %d", got, want)
	}
}

func TestActivityStampUsesSecondPrecision(t *testing.T) {
	input := time.Unix(1_789_000_123, 987_654_321)
	stamp := activityStampOf(input)

	if stamp.IsZero() {
		t.Fatal("non-zero time produced zero activity stamp")
	}

	got := stamp.Time()
	want := time.Unix(input.Unix(), 0)
	if !got.Equal(want) {
		t.Fatalf("activity stamp time = %s, want %s", got, want)
	}
}
