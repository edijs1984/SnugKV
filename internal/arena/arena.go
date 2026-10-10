// Package arena owns segmented byte allocations and generation-checked references.
// Callers synchronize all operations, including View, for the reference lifetime.
package arena

import (
	"encoding/binary"
	"errors"
)

const SegmentBytes = 8 << 10
const firstSmallSegmentBytes = 256
const tinyFirstSegmentBytes = 192
const secondSmallSegmentBytes = 1 << 10
const segmentMetadata = 24
const freeBucketCount = 137

// Ref is an opaque allocation identity. Generation prevents aliasing after reuse.
//
// The allocation location is packed into 64 bits:
//
//	25 bits segment
//	13 bits offset
//	26 bits length
//
// Normal arena segments are at most 8 KiB, so 13 offset bits are sufficient.
// 26 length bits support values up to just under 64 MiB, above SnugKV's
// 32 MiB RESP bulk limit. Generation remains a full 64 bits.
type Ref struct {
	location   uint64
	generation uint64
}

const (
	refLengthBits  = 26
	refOffsetBits  = 13
	refSegmentBits = 25

	refLengthMask  = uint64(1<<refLengthBits) - 1
	refOffsetMask  = uint64(1<<refOffsetBits) - 1
	refSegmentMask = uint64(1<<refSegmentBits) - 1

	// The generation word is GenerationBits wide so a stored entry can pack it
	// next to its other fields. Block references use all of GenerationBits-1
	// low bits; an inline reference sets the top bit and uses the next four for
	// its length, leaving 14 bits of generation.
	GenerationBits       = 19
	inlineMarker         = uint64(1) << (GenerationBits - 1)
	inlineLengthShift    = GenerationBits - 5
	inlineLengthMask     = uint64(0xF) << inlineLengthShift
	inlineGenerationMask = (uint64(1) << inlineLengthShift) - 1
	blockGenerationMask  = inlineMarker - 1
)

func newRef(segment, offset, length uint32, generation uint64) Ref {
	if uint64(segment) > refSegmentMask {
		panic("arena segment index exceeds reference capacity")
	}
	if uint64(offset) > refOffsetMask {
		panic("arena offset exceeds reference capacity")
	}
	if uint64(length) > refLengthMask {
		panic("arena value exceeds reference capacity")
	}

	location :=
		uint64(segment)<<(refOffsetBits+refLengthBits) |
			uint64(offset)<<refLengthBits |
			uint64(length)

	return Ref{
		location:   location,
		generation: generation,
	}
}

func (r Ref) segment() uint32 {
	return uint32((r.location >> (refOffsetBits + refLengthBits)) & refSegmentMask)
}

func (r Ref) offset() uint32 {
	return uint32((r.location >> refLengthBits) & refOffsetMask)
}

func (r Ref) length() uint32 {
	return uint32(r.location & refLengthMask)
}

// nextGeneration advances the allocation counter. The counter wraps and skips
// zero, which marks a free block. A stale reference is therefore detected unless
// exactly 2^18 allocations in this shard separate it from its block's reuse,
// and every optimistic rewrite re-verifies the value's content in any case.
func (a *Arena) nextGeneration() uint64 {
	a.generation++
	if a.generation > blockGenerationMask {
		a.generation = 1
	}
	return a.generation
}

// AllocInline stores up to eight payload bytes directly in Ref. It consumes a
// generation just like an arena allocation so optimistic rewrite version checks
// retain the same semantics without reserving an arena block.
func (a *Arena) AllocInline(value []byte) (Ref, bool) {
	if len(value) == 0 || len(value) > 8 {
		return Ref{}, false
	}
	generation := a.nextGeneration() & inlineGenerationMask
	var buf [8]byte
	copy(buf[:], value)
	return Ref{
		location: binary.LittleEndian.Uint64(buf[:]),
		generation: inlineMarker |
			uint64(len(value))<<inlineLengthShift |
			generation,
	}, true
}

func (r Ref) IsInline() bool {
	return r.generation&inlineMarker != 0
}

func (r Ref) InlineInto(dst []byte) ([]byte, bool) {
	if !r.IsInline() {
		return nil, false
	}
	n := int((r.generation & inlineLengthMask) >> inlineLengthShift)
	if n < 1 || n > 8 {
		return nil, false
	}
	if cap(dst) < n {
		dst = make([]byte, n)
	} else {
		dst = dst[:n]
	}
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], r.location)
	copy(dst, buf[:n])
	return dst, true
}

// segment uses the slice length as the number of committed bytes and the slice
// capacity as the reserved segment size. That keeps the descriptor to one
// 24-byte slice header instead of storing a separate used counter that padded
// the descriptor to 32 bytes on 64-bit targets.
type segment struct {
	data []byte
}

type Arena struct {
	segments []segment
	// Buckets 128 and 129 are reserved for exact 24- and 88-byte blocks used
	// by tiny native-container payloads. Large 32 MiB values top out at 127.
	// Buckets 130-136 hold the 16-byte steps between 385 and 512 bytes.
	//
	// Keep the free-head table out of the Arena struct until a shard actually
	// frees an allocation. Sparse/write-once shards otherwise paid 130 uint64
	// heads (1040 bytes) even though every head stayed zero.
	free       *freeTable
	generation uint64
}

// freeTable is allocated lazily on the first real free so write-once shards do
// not pay for it. holes lists segment slots whose dedicated block was freed and
// whose backing memory was returned to the Go heap; they are reused for the
// next dedicated allocation so the segment slice does not grow without bound.
type freeTable struct {
	heads [freeBucketCount]uint64
	holes []uint32
}

// dedicatedBlock reports whether a block of this size owns a whole segment.
// Blocks above half a segment cannot share one, so freeing them can release
// the segment instead of parking it on a per-class free list that only the
// exact same class can ever reuse (a growing value passes through every class
// once and would otherwise leave one idle block behind per class per shard).
func dedicatedBlock(block int) bool { return block > SegmentBytes/2 }

func class(n int) (int, int) {
	size := n + 8

	if size < 16 {
		size = 16
	}

	// Targeted exact classes avoid 33% overhead for raw 16-byte singleton SETs
	// and trim the common ~80-byte front-coded SET range without disturbing the
	// established HASH/general-purpose class numbering.
	if size > 16 && size <= 24 {
		return 128, 24
	}
	if size > 80 && size <= 88 {
		return 129, 88
	}

	switch {
	case size <= 48:
		block := (size + 15) &^ 15
		return block/16 - 1, block

	case size <= 56:
		// A 48-byte logical value needs 56 bytes including the arena header.
		// Keep an exact class here instead of rounding to 64 bytes. This is a
		// common packed single-field HASH size.
		return 3, 56

	case size <= 128:
		block := (size + 15) &^ 15
		return 4 + (block-64)/16, block

	case size <= 256:
		block := (size + 31) &^ 31
		return 9 + (block-160)/32, block

	case size <= 384:
		// Tight 16-byte classes in the range containing small packed HASHes.
		// A 356-byte HASH needs 364 bytes including the arena header and now
		// lands in a 368-byte block rather than a 384-byte block.
		block := (size + 15) &^ 15
		return 13 + (block-272)/16, block

	case size <= 512:
		block := (size + 15) &^ 15
		if block == 448 || block == 512 {
			return 21 + (block-448)/64, block
		}
		// 16-byte steps keep a 407-byte record (a 384-byte value plus key and
		// header) in a 416-byte block instead of 448. Buckets 21 and 22 keep
		// the established 448/512 classes; the intermediate sizes take the
		// appended buckets 130-136 so existing numbering never shifts.
		return 130 + (block-400)/16, block

	case size <= 1024:
		// Medium-small values are extremely common for JSON/API payloads.
		// Use 32-byte classes here to avoid excessive internal fragmentation.
		block := (size + 31) &^ 31
		return 23 + (block-544)/32, block

	case size <= SegmentBytes/2:
		// Values up to half a segment still share 8 KiB segments, so their
		// classes stay ~12.5% geometric steps that pack well. Starting from
		// 1024 keeps the progression compact.
		block := 1024
		bucket := 38

		for block < size {
			step := block / 8
			if step < 128 {
				step = 128
			}

			block += step
			bucket++

			if block > SegmentBytes/2 {
				block = SegmentBytes / 2
			}
		}

		return bucket, block
	}

	// Larger blocks own a whole Go allocation, and the Go allocator rounds every
	// request up to its own size class (small objects) or to 8 KiB pages (large
	// ones). Rounding to anything else wastes memory twice: a block of 7,168
	// bytes still costs 8,192. Sizing the block to the allocator's class makes
	// the arena's accounting equal the heap's real cost and lets a value that
	// just fits a class (a 6,723-byte list lands in the 6,784 class) use it.
	// A block no larger than a segment can still land in the free tail of a
	// shared segment, and freeing it there parks it on a free list, so those
	// classes (up to 8,192 bytes) get a bucket each. Anything larger always owns
	// its segment, is returned to the heap on free, and shares one bucket.
	if size > maxArenaBlock {
		panic("arena allocation too large")
	}
	block := dedicatedBlockSize(size)
	if block <= SegmentBytes {
		for i, class := range goSmallSizeClasses {
			if class == block {
				return sharedLargeBucketBase + i, block
			}
		}
	}
	return dedicatedBucket, block
}

// sharedLargeBucketBase starts the buckets for the 4,864..8,192 byte classes,
// which follow bucket 50 (the 4,096-byte block); seven classes use 51..57.
const sharedLargeBucketBase = 51

// dedicatedBucket is the free-list bucket reported for blocks above one
// segment. Their frees return memory to the heap, so the list stays empty.
const dedicatedBucket = 58

// maxArenaBlock bounds a single arena block: a 32 MiB value plus its header
// and rounding.
const maxArenaBlock = 33<<20 + 8<<10

// goSmallSizeClasses lists the Go allocator's size classes above 4,096 bytes up
// to the largest small object (32 KiB).
var goSmallSizeClasses = [...]int{
	4864, 5376, 6144, 6528, 6784, 6912, 8192, 9472, 9728, 10240, 10880,
	12288, 13568, 14336, 16384, 18432, 19072, 20480, 21760, 24576, 27264,
	28672, 32768,
}

const goPageBytes = 8 << 10

func dedicatedBlockSize(size int) int {
	for _, class := range goSmallSizeClasses {
		if size <= class {
			return class
		}
	}
	return (size + goPageBytes - 1) &^ (goPageBytes - 1)
}

// segmentSizeForBlock keeps small allocations on 8 KiB segments after the
// sparse-growth stages, where packing many values amortizes metadata well. For
// medium blocks it sizes a segment to an exact multiple of the block class so a
// segment cannot end with a large permanently unusable tail. Allocations larger
// than 8 KiB keep their dedicated block-sized segment.
func segmentSizeForBlock(block int) int {
	if block > SegmentBytes {
		return block
	}
	if block <= 1024 {
		return SegmentBytes
	}

	blocks := SegmentBytes / block
	if blocks < 1 {
		return block
	}
	return blocks * block
}

// segmentSizeForAllocation stages small-value growth so sparse shards reserve
// only what they are likely to use: a tighter 192-byte first segment for the
// exact 24-byte tiny-value class, roughly 256 bytes for other small classes,
// roughly 1 KiB for the second segment, then the normal dense segment policy.
// Sparse stages are rounded to exact block multiples.
func segmentSizeForAllocation(block, existingSegments int) int {
	if block > 1024 || existingSegments >= 2 {
		return segmentSizeForBlock(block)
	}

	target := firstSmallSegmentBytes
	if existingSegments == 0 && block == 24 {
		target = tinyFirstSegmentBytes
	}
	if existingSegments == 1 {
		target = secondSmallSegmentBytes
	}

	if target < block {
		return block
	}

	blocks := target / block
	if blocks < 1 {
		return block
	}
	return blocks * block
}

func (a *Arena) MemoryBytes() uint64 {
	total := uint64(cap(a.segments) * segmentMetadata)
	for _, s := range a.segments {
		total += uint64(cap(s.data))
	}
	return total
}

// SegmentCount exposes the number of physical arena segments for diagnostics.
func (a *Arena) SegmentCount() int { return len(a.segments) }

// GrowthFor simulates allocation without changing state. Old allocations remain
// live during planning, bounding peak old/new storage during publication.
func (a *Arena) GrowthFor(lengths []int) uint64 {
	var free [freeBucketCount]uint64
	holes := 0
	if a.free != nil {
		free = a.free.heads
		holes = len(a.free.holes)
	}
	count, capacity := len(a.segments), cap(a.segments)
	used, size := 0, 0
	if count > 0 {
		last := a.segments[count-1]
		used = len(last.data)
		size = cap(last.data)
	}
	var growth uint64
	for _, n := range lengths {
		if n == 0 {
			continue
		}
		bucket, block := class(n)
		if free[bucket] != 0 {
			address := free[bucket]
			seg, offset := int(address>>32)-1, int(uint32(address))
			free[bucket] = binary.LittleEndian.Uint64(a.segments[seg].data[offset+8:])
			continue
		}
		if dedicatedBlock(block) && holes > 0 {
			holes--
			growth += uint64(segmentSizeForBlock(block))
			continue
		}
		if size-used < block {
			size = segmentSizeForAllocation(block, count)
			used = 0
			growth += uint64(size)
			if count == capacity {
				next := capacity * 2
				if next == 0 {
					next = 1
				}
				growth += uint64((next - capacity) * segmentMetadata)
				capacity = next
			}
			count++
		}
		used += block
	}
	return growth
}

func (a *Arena) Alloc(value []byte) Ref {
	if len(value) == 0 {
		return Ref{}
	}
	bucket, block := class(len(value))
	var seg, offset int
	var address uint64
	if a.free != nil {
		address = a.free.heads[bucket]
	}
	if address != 0 {
		seg = int(address>>32) - 1
		offset = int(uint32(address))
		a.free.heads[bucket] = binary.LittleEndian.Uint64(a.segments[seg].data[offset+8:])
	} else if dedicatedBlock(block) && a.free != nil && len(a.free.holes) > 0 {
		seg = int(a.free.holes[len(a.free.holes)-1])
		a.free.holes = a.free.holes[:len(a.free.holes)-1]
		a.segments[seg].data = make([]byte, block, segmentSizeForBlock(block))
		offset = 0
	} else {
		seg = len(a.segments) - 1
		if seg < 0 || cap(a.segments[seg].data)-len(a.segments[seg].data) < block {
			size := segmentSizeForAllocation(block, len(a.segments))
			if len(a.segments) == cap(a.segments) {
				capacity := 2 * cap(a.segments)
				if capacity == 0 {
					capacity = 1
				}
				next := make([]segment, len(a.segments), capacity)
				copy(next, a.segments)
				a.segments = next
			}
			a.segments = append(a.segments, segment{data: make([]byte, 0, size)})
			seg = len(a.segments) - 1
		}
		offset = len(a.segments[seg].data)
		a.segments[seg].data = a.segments[seg].data[:offset+block]
	}
	generation := a.nextGeneration()
	binary.LittleEndian.PutUint64(a.segments[seg].data[offset:], generation)
	copy(a.segments[seg].data[offset+8:], value)
	return newRef(uint32(seg), uint32(offset), uint32(len(value)), generation)
}

func (a *Arena) View(ref Ref) ([]byte, error) {
	if ref.IsInline() {
		out, ok := ref.InlineInto(nil)
		if !ok {
			return nil, errors.New("invalid inline reference")
		}
		return out, nil
	}
	if ref.generation == 0 {
		if ref.length() == 0 {
			return []byte{}, nil
		}
		return nil, errors.New("invalid empty reference")
	}
	if int(ref.segment()) >= len(a.segments) {
		return nil, errors.New("invalid segment")
	}
	data := a.segments[ref.segment()].data
	start := uint64(ref.offset())
	end := start + 8 + uint64(ref.length())
	if end > uint64(len(data)) || binary.LittleEndian.Uint64(data[start:]) != ref.generation {
		return nil, errors.New("stale arena reference")
	}
	return data[start+8 : end : end], nil
}


// ViewKnownLive returns the payload for a ref that the caller has already
// obtained from a live arena-backed entry while holding the owning shard lock.
// It retains structural bounds checks but skips the generation-header reload:
// under that lock the entry cannot be freed/reused concurrently. Callers must
// not use this for arbitrary or externally supplied refs.
func (a *Arena) ViewKnownLive(ref Ref) ([]byte, error) {
	if ref.IsInline() {
		out, ok := ref.InlineInto(nil)
		if !ok {
			return nil, errors.New("invalid inline reference")
		}
		return out, nil
	}
	if ref.generation == 0 {
		if ref.length() == 0 {
			return []byte{}, nil
		}
		return nil, errors.New("invalid empty reference")
	}
	seg := ref.segment()
	if int(seg) >= len(a.segments) {
		return nil, errors.New("invalid segment")
	}
	data := a.segments[seg].data
	start := uint64(ref.offset()) + 8
	end := start + uint64(ref.length())
	if end > uint64(len(data)) {
		return nil, errors.New("invalid arena reference")
	}
	return data[start:end:end], nil
}


// Free releases ref. It returns the number of heap bytes handed back to the Go
// heap (non-zero only for dedicated large blocks), so the caller can keep its
// accounting equal to MemoryBytes.
func (a *Arena) Free(ref Ref) uint64 {
	if ref.IsInline() {
		return 0
	}
	if ref.generation == 0 {
		return 0
	}
	if _, err := a.View(ref); err != nil {
		panic(err)
	}
	bucket, block := class(int(ref.length()))
	seg := ref.segment()
	data := a.segments[seg].data
	if a.free == nil {
		a.free = new(freeTable)
	}
	if dedicatedBlock(block) && ref.offset() == 0 && cap(data) == block && len(data) == block {
		released := uint64(cap(data))
		a.segments[seg].data = nil
		a.free.holes = append(a.free.holes, seg)
		return released
	}
	binary.LittleEndian.PutUint64(data[ref.offset():], 0)
	binary.LittleEndian.PutUint64(data[ref.offset()+8:], a.free.heads[bucket])
	a.free.heads[bucket] = (uint64(seg)+1)<<32 | uint64(ref.offset())
	return 0
}

// AllocationBytesForLength returns the physical arena block that would be
// reserved for a value of the given logical length.
func AllocationBytesForLength(length int) uint64 {
	if length == 0 {
		return 0
	}
	_, block := class(length)
	return uint64(block)
}

// AllocationBytes returns the physical arena block reserved for ref.
func (a *Arena) AllocationBytes(ref Ref) uint64 {
	if ref.IsInline() {
		return 0
	}
	if ref.generation == 0 {
		return 0
	}

	_, block := class(int(ref.length()))
	return uint64(block)
}
