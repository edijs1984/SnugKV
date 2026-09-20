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
const freeBucketCount = 130

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
	//
	// Keep the free-head table out of the Arena struct until a shard actually
	// frees an allocation. Sparse/write-once shards otherwise paid 130 uint64
	// heads (1040 bytes) even though every head stayed zero.
	free       *[freeBucketCount]uint64
	generation uint64
}

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
		block := (size + 63) &^ 63
		return 21 + (block-448)/64, block

	case size <= 1024:
		// Medium-small values are extremely common for JSON/API payloads.
		// Use 32-byte classes here to avoid excessive internal fragmentation.
		block := (size + 31) &^ 31
		return 23 + (block-544)/32, block

	case size <= 8192:
		// Medium values use ~12.5% geometric classes. Starting from 1024 keeps
		// the class progression compact while leaving enough buckets for the
		// full 32 MiB value range.
		block := 1024
		bucket := 38

		for block < size {
			step := block / 8
			if step < 128 {
				step = 128
			}

			block += step
			bucket++

			if block > 8192 {
				block = 8192
			}
		}

		return bucket, block
	}

	// Large allocations continue using ~12.5% size classes. Medium classes
	// end at bucket 56; a 32 MiB value reaches bucket 127. Buckets above that
	// are intentionally reserved for the targeted tiny classes above.
	block := 8192
	bucket := 56

	for block < size {
		step := block / 8
		if step < 1024 {
			step = 1024
		}

		block += step
		bucket++

		if bucket >= 128 {
			panic("arena allocation too large")
		}
	}

	return bucket, block
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
	if a.free != nil {
		free = *a.free
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
		address = a.free[bucket]
	}
	if address != 0 {
		seg = int(address>>32) - 1
		offset = int(uint32(address))
		a.free[bucket] = binary.LittleEndian.Uint64(a.segments[seg].data[offset+8:])
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
	a.generation++
	if a.generation == 0 {
		panic("arena generation exhausted")
	}
	binary.LittleEndian.PutUint64(a.segments[seg].data[offset:], a.generation)
	copy(a.segments[seg].data[offset+8:], value)
	return newRef(uint32(seg), uint32(offset), uint32(len(value)), a.generation)
}

func (a *Arena) View(ref Ref) ([]byte, error) {
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


// ViewLive returns the bytes for a reference that the caller already knows is
// live while holding the arena owner's synchronization lock. Unlike View, it
// deliberately skips rereading the allocation generation header.
//
// Engine shard lookups satisfy this invariant because an entry's Ref cannot be
// freed or replaced while the owning shard lock is held. General callers that
// may hold stale references must continue using View.
func (a *Arena) ViewLive(ref Ref) ([]byte, error) {
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
	if end > uint64(len(data)) {
		return nil, errors.New("invalid arena reference")
	}
	return data[start+8 : end : end], nil
}

func (a *Arena) Free(ref Ref) {
	if ref.generation == 0 {
		return
	}
	if _, err := a.View(ref); err != nil {
		panic(err)
	}
	bucket, _ := class(int(ref.length()))
	if a.free == nil {
		a.free = new([freeBucketCount]uint64)
	}
	data := a.segments[ref.segment()].data
	binary.LittleEndian.PutUint64(data[ref.offset():], 0)
	binary.LittleEndian.PutUint64(data[ref.offset()+8:], a.free[bucket])
	a.free[bucket] = (uint64(ref.segment())+1)<<32 | uint64(ref.offset())
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
	if ref.generation == 0 {
		return 0
	}

	_, block := class(int(ref.length()))
	return uint64(block)
}
