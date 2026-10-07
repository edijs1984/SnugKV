package arena

import "testing"

// A value that grows through every size class must not leave one idle block
// per class behind: dedicated large blocks are returned to the heap on free and
// the segment slot is reused.
func TestFreeReleasesDedicatedBlocks(t *testing.T) {
	var a Arena
	payload := make([]byte, 0, 64<<10)
	var ref Ref
	for n := 4000; n <= 60000; n += 700 {
		payload = payload[:n]
		next := a.Alloc(payload)
		if ref.generation != 0 {
			a.Free(ref)
		}
		ref = next
	}
	_, block := class(int(ref.length()))
	live := uint64(block)
	if got := a.MemoryBytes(); got > live+uint64(cap(a.segments)*segmentMetadata)+2*uint64(block) {
		t.Fatalf("arena holds %d bytes for one %d byte live block", got, live)
	}
	if v, err := a.View(ref); err != nil || len(v) != int(ref.length()) {
		t.Fatalf("live ref unreadable: %v", err)
	}
	if n := len(a.segments); n > 6 {
		t.Fatalf("segment slots grew to %d; holes are not reused", n)
	}
}

func TestReleasedSegmentReferenceIsStale(t *testing.T) {
	var a Arena
	big := make([]byte, 6000)
	r1 := a.Alloc(big)
	if released := a.Free(r1); released == 0 {
		t.Fatal("dedicated block was not released")
	}
	if _, err := a.View(r1); err == nil {
		t.Fatal("stale ref viewable after release")
	}
	r2 := a.Alloc(big)
	if _, err := a.View(r1); err == nil {
		t.Fatal("stale ref aliased a reused segment")
	}
	if _, err := a.View(r2); err != nil {
		t.Fatal(err)
	}
}

func TestGrowthForMatchesAllocWithHoles(t *testing.T) {
	var a Arena
	r := a.Alloc(make([]byte, 6000))
	a.Free(r)
	before := a.TotalMemoryBytes()
	projected := a.GrowthFor([]int{6000})
	a.Alloc(make([]byte, 6000))
	if got := a.TotalMemoryBytes() - before; got != projected {
		t.Fatalf("projected %d actual %d", projected, got)
	}
}
