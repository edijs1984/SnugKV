package engine

import (
	"snugkv/internal/arena"
	"snugkv/internal/codec"
)

func codecIDOf(v uint64) codec.ID { return codec.ID(v) }

// packedEntry is the stored form of an entryData: 16 bytes instead of 24.
//
//	word 0  the arena reference's location (or an inline payload)
//	word 1  bits  0..31  rawLength
//	        bits 32..37  codec ID
//	        bits 38..42  value type
//	        bit  43      has expiry
//	        bit  44      raw-stable / hot-hash flag
//	        bits 45..63  arena generation word (arena.GenerationBits wide)
//
// Everything outside the shard's entry array keeps using entryData, so command
// code reads and writes the same named fields as before; only storage is packed.
type packedEntry struct {
	location uint64
	meta     uint64
}

const (
	packCodecShift  = 32
	packCodecMask   = uint64(1)<<6 - 1
	packTypeShift   = 38
	packTypeMask    = uint64(1)<<5 - 1
	packExpiryBit   = uint64(1) << 43
	packStableBit   = uint64(1) << 44
	packGenShift    = 45
	packGenMask     = uint64(1)<<arena.GenerationBits - 1
	packRawLenMask  = uint64(1)<<32 - 1
	maxPackedCodec  = packCodecMask
	maxPackedTypeID = packTypeMask
)

func packEntry(v SnugValue) packedEntry {
	location, generation := v.ref.Words()
	if generation > packGenMask {
		panic("arena generation does not fit a packed entry")
	}
	if uint64(v.codecID) > maxPackedCodec {
		panic("codec ID does not fit a packed entry")
	}
	if uint64(v.valueType) > maxPackedTypeID {
		panic("value type does not fit a packed entry")
	}
	meta := uint64(v.rawLength) |
		uint64(v.codecID)<<packCodecShift |
		uint64(v.valueType)<<packTypeShift |
		generation<<packGenShift
	if v.hasExpiry {
		meta |= packExpiryBit
	}
	if v.rawStable {
		meta |= packStableBit
	}
	return packedEntry{location: location, meta: meta}
}

func (p packedEntry) unpack() SnugValue {
	return SnugValue{
		ref:       arena.RefFromWords(p.location, (p.meta>>packGenShift)&packGenMask),
		rawLength: uint32(p.meta & packRawLenMask),
		codecID:   codecIDOf((p.meta >> packCodecShift) & packCodecMask),
		valueType: ValueType((p.meta >> packTypeShift) & packTypeMask),
		hasExpiry: p.meta&packExpiryBit != 0,
		rawStable: p.meta&packStableBit != 0,
	}
}
