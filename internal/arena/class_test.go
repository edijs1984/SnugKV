package arena

import (
	"bytes"
	"math/rand"
	"testing"
)

// Every payload must map to a block that holds it, each bucket must name one
// block size, and no two block sizes may share a bucket.
func TestClassTableIsConsistent(t *testing.T) {
	blockOf := map[int]int{}
	bucketOf := map[int]int{}
	for n := 1; n <= 70000; n++ {
		bucket, block := class(n)
		if block < n+8 {
			t.Fatalf("class(%d) block %d cannot hold payload plus header", n, block)
		}
		if bucket < 0 || bucket >= freeBucketCount {
			t.Fatalf("class(%d) bucket %d out of range", n, bucket)
		}
		if bucket == dedicatedBucket {
			// Dedicated blocks own their allocation and share one bucket; they
			// must still follow the allocator's classes.
			if block != dedicatedBlockSize(n+8) {
				t.Fatalf("class(%d) dedicated block %d", n, block)
			}
			continue
		}
		if prev, ok := blockOf[bucket]; ok && prev != block {
			t.Fatalf("bucket %d maps to blocks %d and %d", bucket, prev, block)
		}
		blockOf[bucket] = block
		if prev, ok := bucketOf[block]; ok && prev != bucket {
			t.Fatalf("block %d maps to buckets %d and %d", block, prev, bucket)
		}
		bucketOf[block] = bucket
	}
}

func TestClassMidRangeUsesSixteenByteSteps(t *testing.T) {
	for _, tt := range []struct{ payload, block int }{
		{376, 384}, {377, 400}, {384, 400}, {392, 400}, {393, 416}, {408, 416},
		{409, 432}, {425, 448}, {440, 448}, {441, 464}, {456, 464}, {457, 480},
		{488, 496}, {489, 512}, {504, 512}, {505, 544},
	} {
		if _, block := class(tt.payload); block != tt.block {
			t.Fatalf("class(%d) block = %d, want %d", tt.payload, block, tt.block)
		}
	}
}

// Dedicated blocks are sized to the Go allocator's classes so the arena's
// accounting equals what the heap really spends.
func TestDedicatedBlocksMatchGoSizeClasses(t *testing.T) {
	for _, tt := range []struct{ payload, block int }{
		{4089, 4864}, {6715, 6784}, {6904, 6912}, {6905, 8192},
		{32760, 32768}, {32761, 40960}, {40952, 40960}, {40953, 49152},
	} {
		if _, block := class(tt.payload); block != tt.block {
			t.Fatalf("class(%d) block = %d, want %d", tt.payload, block, tt.block)
		}
	}
	for n := 4089; n < 300000; n += 7 {
		_, block := class(n)
		if block < n+8 {
			t.Fatalf("class(%d) block %d too small", n, block)
		}
	}
}

// Random alloc/free churn across shared-segment and dedicated sizes: blocks of
// the 4,864..8,192 classes can sit in a shared segment's free tail, and freeing
// them must never hand a smaller block to a larger request.
func TestMixedSizeChurnKeepsValuesIntact(t *testing.T) {
	var a Arena
	rng := rand.New(rand.NewSource(3))
	type item struct {
		ref Ref
		val []byte
	}
	var live []item
	for step := 0; step < 60000; step++ {
		if len(live) > 0 && rng.Intn(100) < 48 {
			i := rng.Intn(len(live))
			a.Free(live[i].ref)
			live[i] = live[len(live)-1]
			live = live[:len(live)-1]
			continue
		}
		var n int
		switch rng.Intn(4) {
		case 0:
			n = 1 + rng.Intn(600)
		case 1:
			n = 3000 + rng.Intn(1500)
		case 2:
			n = 4800 + rng.Intn(3600)
		default:
			n = 8000 + rng.Intn(60000)
		}
		v := bytes.Repeat([]byte{byte(step)}, n)
		live = append(live, item{a.Alloc(v), v})
	}
	for i, it := range live {
		got, err := a.View(it.ref)
		if err != nil || !bytes.Equal(got, it.val) {
			t.Fatalf("item %d corrupt (err=%v)", i, err)
		}
	}
}
