package engine

import (
	"math/rand"
	"snugkv/internal/arena"
	"snugkv/internal/codec"
	"testing"
	"unsafe"
)

func TestPackedEntryIs16Bytes(t *testing.T) {
	if got := unsafe.Sizeof(packedEntry{}); got != 16 {
		t.Fatalf("packed entry = %d bytes, want 16", got)
	}
}

func TestPackedEntryRoundTripsEveryField(t *testing.T) {
	var a arena.Arena
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		var ref arena.Ref
		switch rng.Intn(3) {
		case 0:
			ref = a.Alloc(make([]byte, 1+rng.Intn(300)))
		case 1:
			ref, _ = a.AllocInline([]byte{byte(i), 2, 3, 4, 5, 6, 7, byte(i >> 8)}[:1+rng.Intn(8)])
		}
		want := SnugValue{
			ref:       ref,
			rawLength: rng.Uint32(),
			codecID:   codec.ID(rng.Intn(int(maxPackedCodec) + 1)),
			valueType: ValueType(rng.Intn(int(maxPackedTypeID) + 1)),
			hasExpiry: rng.Intn(2) == 0,
			rawStable: rng.Intn(2) == 0,
		}
		got := packEntry(want).unpack()
		if got != want {
			t.Fatalf("round trip %d:\n got  %+v\n want %+v", i, got, want)
		}
	}
}

func TestPackedEntryZeroIsEmptyEntry(t *testing.T) {
	if got := (packedEntry{}).unpack(); got != (SnugValue{}) {
		t.Fatalf("zero packed entry unpacks to %+v", got)
	}
	if got := packEntry(SnugValue{}); got != (packedEntry{}) {
		t.Fatalf("empty entry packs to %+v", got)
	}
}

func TestEveryValueTypeAndCodecFitsPackedEntry(t *testing.T) {
	if uint64(TypeVectorSet) > maxPackedTypeID {
		t.Fatalf("value type %d exceeds the %d the packed entry holds", TypeVectorSet, maxPackedTypeID)
	}
	// The highest codec ID in use is ULID (12); the field holds up to 63.
	if uint64(codec.ULID) > maxPackedCodec {
		t.Fatalf("codec %d exceeds the %d the packed entry holds", codec.ULID, maxPackedCodec)
	}
}
