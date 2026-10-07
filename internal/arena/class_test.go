package arena

import "testing"

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
