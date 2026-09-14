// Package arena owns segmented byte allocations and generation-checked references.
// Callers synchronize all operations, including View, for the reference lifetime.
package arena

import (
	"encoding/binary"
	"errors"
)

const SegmentBytes = 8 << 10
const segmentMetadata = 32

// Ref is an opaque allocation identity. Generation prevents aliasing after reuse.
//
// The allocation location is packed into 64 bits:
//
//	25 bits segment
//	13 bits offset
//	26 bits length
//
// Normal arena segments are 8 KiB, so 13 offset bits are sufficient.
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

type segment struct {
	data []byte
	used uint32
}
type Arena struct {
	segments   []segment
	free       [128]uint64
	generation uint64
}

func class(n int) (int, int) {
	size := n + 8

	if size < 16 {
		size = 16
	}

	switch {
	case size <= 128:
		block := (size + 15) &^ 15
		return block/16 - 1, block

	case size <= 256:
		block := (size + 31) &^ 31
		return 8 + (block-160)/32, block

	case size <= 512:
		block := (size + 63) &^ 63
		return 12 + (block-320)/64, block

	case size <= 1024:
		// Medium-small values are extremely common for JSON/API payloads.
		// Use 32-byte classes here to avoid excessive internal fragmentation.
		//
		// Example:
		//   777-byte payload + 8-byte arena header = 785 bytes
		//   old 128-byte classes -> 896-byte block
		//   new 32-byte classes  -> 800-byte block
		block := (size + 31) &^ 31
		return 16 + (block-544)/32, block

	case size <= 2048:
		block := (size + 255) &^ 255
		return 32 + (block-1280)/256, block

	case size <= 4096:
		block := (size + 511) &^ 511
		return 36 + (block-2560)/512, block

	case size <= 8192:
		block := (size + 1023) &^ 1023
		return 40 + (block-5120)/1024, block
	}

	// Large allocations use ~12.5% size classes instead of powers of two.
	// This keeps worst-case internal fragmentation much lower.
	block := 8192
	bucket := 44

	for block < size {
		step := block / 8
		if step < 1024 {
			step = 1024
		}

		block += step
		bucket++

		if bucket >= len(Arena{}.free) {
			panic("arena allocation too large")
		}
	}

	return bucket, block
}
func (a *Arena) MemoryBytes() uint64 {
	total := uint64(cap(a.segments) * segmentMetadata)
	for _, s := range a.segments {
		total += uint64(cap(s.data))
	}
	return total
}

// GrowthFor simulates allocation without changing state. Old allocations remain
// live during planning, bounding peak old/new storage during publication.
func (a *Arena) GrowthFor(lengths []int) uint64 {
	free := a.free
	count, capacity := len(a.segments), cap(a.segments)
	used, size := 0, 0
	if count > 0 {
		last := a.segments[count-1]
		used = int(last.used)
		size = len(last.data)
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
			size = SegmentBytes
			if block > size {
				size = block
			}
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
	if address := a.free[bucket]; address != 0 {
		seg = int(address>>32) - 1
		offset = int(uint32(address))
		a.free[bucket] = binary.LittleEndian.Uint64(a.segments[seg].data[offset+8:])
	} else {
		seg = len(a.segments) - 1
		if seg < 0 || len(a.segments[seg].data)-int(a.segments[seg].used) < block {
			size := SegmentBytes
			if block > size {
				size = block
			}
			if len(a.segments) == cap(a.segments) {
				capacity := 2 * cap(a.segments)
				if capacity == 0 {
					capacity = 1
				}
				next := make([]segment, len(a.segments), capacity)
				copy(next, a.segments)
				a.segments = next
			}
			a.segments = append(a.segments, segment{data: make([]byte, size)})
			seg = len(a.segments) - 1
		}
		offset = int(a.segments[seg].used)
		a.segments[seg].used += uint32(block)
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
func (a *Arena) Free(ref Ref) {
	if ref.generation == 0 {
		return
	}
	if _, err := a.View(ref); err != nil {
		panic(err)
	}
	bucket, _ := class(int(ref.length()))
	data := a.segments[ref.segment()].data
	binary.LittleEndian.PutUint64(data[ref.offset():], 0)
	binary.LittleEndian.PutUint64(data[ref.offset()+8:], a.free[bucket])
	a.free[bucket] = (uint64(ref.segment())+1)<<32 | uint64(ref.offset())
}

// AllocationBytes returns the physical arena block reserved for ref.
func (a *Arena) AllocationBytes(ref Ref) uint64 {
	if ref.generation == 0 {
		return 0
	}

	_, block := class(int(ref.length()))

	return uint64(block)
}
