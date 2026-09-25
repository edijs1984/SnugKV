// Package index provides a collision-safe open-addressed index with explicit capacity.
package index

import (
	"math/bits"
	"unsafe"
)

const (
	stateEmpty      = uint64(0)
	stateLive       = uint64(1)
	stateDeleted    = uint64(2)
	stateShift       = 62
	fingerprintShift = 58
	fingerprintMask  = uint64(0x0f)
	keyLengthMask    = uint64(1<<26) - 1
	initialCapacity  = 4
)

// slot is intentionally 16 bytes on 64-bit targets:
//
//   - keyData keeps the immutable Go string bytes alive and gives exact-key
//     comparison without retaining a full 16-byte string header in every slot;
//   - meta packs 2 bits of state, a 4-bit hash fingerprint, 26 bits of key
//     length, and the full uint32 value.
//
// SnugKV's RESP bulk/key limit is 32 MiB, comfortably below the ~64 MiB
// 26-bit key-length ceiling. The fingerprint rejects most probe candidates
// before touching key bytes; exact comparison still makes hash collisions safe.
type slot[V ~uint32] struct {
	keyData *byte
	meta    uint64
}

type Table[V ~uint32] struct {
	slots      []slot[V]
	count      uint32
	tinyFilter uint32
}

func hashMix(h, word uint64) uint64 {
	const (
		mul1 = uint64(0x9e3779b185ebca87)
		mul2 = uint64(0xc2b2ae3d27d4eb4f)
	)
	h ^= word * mul2
	return bits.RotateLeft64(h, 27)*mul1 + mul2
}

func hashFinalize(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

// Hash is optimized for RESP keys: consume eight bytes per iteration instead
// of the previous byte-at-a-time FNV loop. The index remains collision-safe
// because every candidate match is still verified against the exact key bytes.
func Hash(key string) uint64 {
	const seed = uint64(0xa0761d6478bd642f)
	h := seed ^ uint64(len(key))*0x9e3779b185ebca87

	if len(key) >= 8 {
		base := unsafe.Pointer(unsafe.StringData(key))
		i := 0
		for ; i+8 <= len(key); i += 8 {
			word := *(*uint64)(unsafe.Add(base, i))
			h = hashMix(h, word)
		}
		if i < len(key) {
			var tail uint64
			shift := uint(0)
			for ; i < len(key); i++ {
				tail |= uint64(key[i]) << shift
				shift += 8
			}
			h = hashMix(h, tail^uint64(len(key)))
		}
	} else {
		var tail uint64
		for i := 0; i < len(key); i++ {
			tail |= uint64(key[i]) << (uint(i) * 8)
		}
		h = hashMix(h, tail^uint64(len(key)))
	}
	return hashFinalize(h)
}

func HashBytes(key []byte) uint64 {
	const seed = uint64(0xa0761d6478bd642f)
	h := seed ^ uint64(len(key))*0x9e3779b185ebca87

	if len(key) >= 8 {
		base := unsafe.Pointer(unsafe.SliceData(key))
		i := 0
		for ; i+8 <= len(key); i += 8 {
			word := *(*uint64)(unsafe.Add(base, i))
			h = hashMix(h, word)
		}
		if i < len(key) {
			var tail uint64
			shift := uint(0)
			for ; i < len(key); i++ {
				tail |= uint64(key[i]) << shift
				shift += 8
			}
			h = hashMix(h, tail^uint64(len(key)))
		}
	} else {
		var tail uint64
		for i, b := range key {
			tail |= uint64(b) << (uint(i) * 8)
		}
		h = hashMix(h, tail^uint64(len(key)))
	}
	return hashFinalize(h)
}

func tinyFilterBits(hash uint64) uint32 {
	return 1<<uint32(hash&31) | 1<<uint32((hash>>32)&31)
}

func hashFingerprint(hash uint64) uint64 {
	return (hash >> fingerprintShift) & fingerprintMask
}

func New[V ~uint32]() *Table[V] { return &Table[V]{} }
func (t *Table[V]) Len() int     { return int(t.count) }

func slotBytes[V ~uint32]() uint64 {
	return uint64(unsafe.Sizeof(slot[V]{}))
}

func (s *slot[V]) state() uint64 {
	return s.meta >> stateShift
}

func (s *slot[V]) value() V {
	return V(uint32(s.meta))
}

func (s *slot[V]) keyLen() int {
	return int((s.meta >> 32) & keyLengthMask)
}

func (s *slot[V]) key() string {
	n := s.keyLen()
	if n == 0 {
		return ""
	}
	return unsafe.String(s.keyData, n)
}

func (s *slot[V]) setLive(key string, value V, hash uint64) {
	if uint64(len(key)) > keyLengthMask {
		panic("index key too large")
	}
	if len(key) == 0 {
		s.keyData = nil
	} else {
		s.keyData = unsafe.StringData(key)
	}
	s.meta = stateLive<<stateShift |
		hashFingerprint(hash)<<fingerprintShift |
		uint64(len(key))<<32 |
		uint64(uint32(value))
}

func (s *slot[V]) setDeleted() {
	s.keyData = nil
	s.meta = stateDeleted << stateShift
}

func (t *Table[V]) CapacityBytes() uint64 {
	return uint64(cap(t.slots)) * slotBytes[V]()
}

func capacityAccepts(n, capacity int) bool {
	// Tiny shard indexes are bounded enough that filling all four slots is a
	// worthwhile memory trade. Missing lookups can scan at most four slots,
	// while the fifth insertion still grows before it is published. Larger
	// tables keep the established 80% ceiling to bound probe lengths.
	if capacity == initialCapacity {
		return n <= capacity
	}
	return n <= capacity*8/10
}

func (t *Table[V]) capacityFor(n int) int {
	capacity := len(t.slots)
	if n == 0 {
		return capacity
	}
	if capacity == 0 {
		capacity = initialCapacity
	}
	for !capacityAccepts(n, capacity) {
		capacity *= 2
	}
	return capacity
}

func (t *Table[V]) GrowthBytes(additional int) uint64 {
	return uint64(t.capacityFor(int(t.count)+additional)-len(t.slots)) * slotBytes[V]()
}

func (t *Table[V]) Get(key string) (V, bool) {
	return t.GetHashed(key, Hash(key))
}

func (t *Table[V]) GetHashed(key string, hash uint64) (V, bool) {
	var zero V
	if len(t.slots) == 0 {
		return zero, false
	}
	if len(t.slots) == initialCapacity && t.count == initialCapacity {
		bits := tinyFilterBits(hash)
		if t.tinyFilter&bits != bits {
			return zero, false
		}
	}
	mask := uint64(len(t.slots) - 1)
	keyLen := uint64(len(key))
	fingerprint := hashFingerprint(hash)
	for n := 0; n < len(t.slots); n++ {
		s := &t.slots[(hash+uint64(n))&mask]
		meta := s.meta
		switch meta >> stateShift {
		case stateEmpty:
			return zero, false
		case stateLive:
			if (meta>>32)&keyLengthMask != keyLen ||
				(meta>>fingerprintShift)&fingerprintMask != fingerprint {
				continue
			}
			if len(key) == 0 || unsafe.String(s.keyData, len(key)) == key {
				return V(uint32(meta)), true
			}
		}
	}
	return zero, false
}

func (t *Table[V]) GetHashedBytes(key []byte, hash uint64) (V, bool) {
	var zero V
	if len(t.slots) == 0 {
		return zero, false
	}
	if len(t.slots) == initialCapacity && t.count == initialCapacity {
		bits := tinyFilterBits(hash)
		if t.tinyFilter&bits != bits {
			return zero, false
		}
	}

	// The transient string aliases only the caller's lookup bytes for the
	// duration of this method. It is never stored in the table.
	var lookup string
	if len(key) > 0 {
		lookup = unsafe.String(unsafe.SliceData(key), len(key))
	}

	mask := uint64(len(t.slots) - 1)
	keyLen := uint64(len(key))
	fingerprint := hashFingerprint(hash)
	for n := 0; n < len(t.slots); n++ {
		s := &t.slots[(hash+uint64(n))&mask]
		meta := s.meta
		switch meta >> stateShift {
		case stateEmpty:
			return zero, false
		case stateLive:
			if (meta>>32)&keyLengthMask != keyLen ||
				(meta>>fingerprintShift)&fingerprintMask != fingerprint {
				continue
			}
			if len(key) == 0 || unsafe.String(s.keyData, len(key)) == lookup {
				return V(uint32(meta)), true
			}
		}
	}
	return zero, false
}

func (t *Table[V]) Set(key string, value V) {
	hash := Hash(key)
	if _, ok := t.GetHashed(key, hash); !ok {
		t.growForInsert()
	}
	t.insertHashed(key, value, hash)
}

// SetKnownHashed updates or inserts using a hash and existence result already
// established by the caller while holding the owning shard lock.
func (t *Table[V]) SetKnownHashed(key string, value V, hash uint64, exists bool) {
	if exists {
		if _, ok := t.GetHashed(key, hash); !ok {
			panic("known index entry is missing")
		}
	} else {
		t.growForInsert()
	}
	t.insertHashed(key, value, hash)
}

func (t *Table[V]) growForInsert() {
	capacity := t.capacityFor(int(t.count) + 1)
	if capacity == len(t.slots) {
		return
	}
	old := t.slots
	t.slots = make([]slot[V], capacity)
	t.count = 0
	t.tinyFilter = 0
	for i := range old {
		s := &old[i]
		if s.state() == stateLive {
			t.insert(s.key(), s.value())
		}
	}
}

func (t *Table[V]) insert(key string, value V) {
	t.insertHashed(key, value, Hash(key))
}

func (t *Table[V]) insertHashed(key string, value V, hash uint64) {
	if len(t.slots) == initialCapacity {
		t.tinyFilter |= tinyFilterBits(hash)
	}
	mask := uint64(len(t.slots) - 1)
	deleted := -1
	for n := 0; n < len(t.slots); n++ {
		i := int((hash + uint64(n)) & mask)
		s := &t.slots[i]
		switch s.state() {
		case stateLive:
			if s.keyLen() == len(key) && s.key() == key {
				s.setLive(key, value, hash)
				return
			}
		case stateDeleted:
			if deleted < 0 {
				deleted = i
			}
		case stateEmpty:
			if deleted >= 0 {
				s = &t.slots[deleted]
			}
			s.setLive(key, value, hash)
			t.count++
			return
		}
	}
	if deleted >= 0 {
		t.slots[deleted].setLive(key, value, hash)
		t.count++
		return
	}
	panic("index capacity invariant")
}

func (t *Table[V]) Delete(key string) {
	if len(t.slots) == 0 {
		return
	}
	hash := Hash(key)
	if len(t.slots) == initialCapacity && t.count == initialCapacity {
		bits := tinyFilterBits(hash)
		if t.tinyFilter&bits != bits {
			return
		}
	}
	mask := uint64(len(t.slots) - 1)
	keyLen := uint64(len(key))
	fingerprint := hashFingerprint(hash)
	for n := 0; n < len(t.slots); n++ {
		s := &t.slots[(hash+uint64(n))&mask]
		meta := s.meta
		switch meta >> stateShift {
		case stateEmpty:
			return
		case stateLive:
			if (meta>>32)&keyLengthMask != keyLen ||
				(meta>>fingerprintShift)&fingerprintMask != fingerprint {
				continue
			}
			if len(key) == 0 || unsafe.String(s.keyData, len(key)) == key {
				s.setDeleted()
				t.count--
				if t.count == 0 {
					t.tinyFilter = 0
				}
				return
			}
		}
	}
}

// All iterates live slots without allocating; callers provide synchronization.
func (t *Table[V]) All() func(func(string, V) bool) {
	return func(yield func(string, V) bool) {
		for i := range t.slots {
			s := &t.slots[i]
			if s.state() == stateLive && !yield(s.key(), s.value()) {
				return
			}
		}
	}
}

func (t *Table[V]) Compact() {
	capacity := initialCapacity
	if t.count == 0 {
		t.slots = nil
		t.tinyFilter = 0
		return
	}
	for !capacityAccepts(int(t.count), capacity) {
		capacity *= 2
	}
	old := t.slots
	t.slots = make([]slot[V], capacity)
	t.count = 0
	t.tinyFilter = 0
	for i := range old {
		s := &old[i]
		if s.state() == stateLive {
			t.insert(s.key(), s.value())
		}
	}
}

// Sample advances a cursor with a bounded slot-scan budget.
func (t *Table[V]) Sample(cursor, budget, limit int) ([]string, int) {
	if len(t.slots) == 0 || limit <= 0 || budget <= 0 {
		return nil, 0
	}
	out := make([]string, 0, limit)
	cursor %= len(t.slots)
	for n := 0; n < budget && n < len(t.slots); n++ {
		s := &t.slots[cursor]
		cursor = (cursor + 1) % len(t.slots)
		if s.state() == stateLive {
			out = append(out, s.key())
			if len(out) == limit {
				break
			}
		}
	}
	return out, cursor
}

func (t *Table[V]) EntryBytes() uint64 {
	return slotBytes[V]()
}
