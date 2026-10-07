package engine

import (
	"math"
	"testing"
)

func TestIndexedZSetRecordRoundTrip(t *testing.T) {
	scores := []float64{0, 1, -1, 2, -2, 63, 64, -64, 1e6, -1e6, 1<<52 - 1, -(1<<52 - 1), 1 << 52, 1 << 60, 1.5, -0.25, math.MaxFloat64, math.SmallestNonzeroFloat64, math.Inf(1), math.Inf(-1), 1e15, 4503599627370495}
	for _, sc := range scores {
		for _, m := range [][]byte{nil, []byte("a"), make([]byte, 300)} {
			rec := indexedZSetRecordBytes(m, sc)
			member, got, end, err := indexedZSetRecordKnown(rec, 0, len(rec), 0)
			if err != nil || got != sc || string(member) != string(m) || end != len(rec) {
				t.Fatalf("score %v member len %d: got %v end %d/%d err %v", sc, len(m), got, end, len(rec), err)
			}
		}
	}
}
