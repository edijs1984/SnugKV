// Package index provides a collision-safe open-addressed index with explicit capacity.
package index

import (
	"hash/maphash"
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

var tableHashSeed = maphash.MakeSeed()

// Hash uses Go's runtime-optimized seeded string hash. The same process-local
// seed is shared by string and byte lookups so Hash and HashBytes remain
// equivalent, while exact key comparison still makes the index collision-safe.
func Hash(key string) uint64 {
	return maphash.String(tableHashSeed, key)
}

func HashBytes(key []byte) uint64 {
	return maphash.Bytes(tableHashSeed, key)
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

// probeStart maps the low 32 hash bits onto [0, len(slots)) with a multiply
// and shift, so table capacities need not be powers of two. The shard selector
// uses hash bits 32-39 and the fingerprint bits 58-61, so the probe start stays
// independent of both.
func (t *Table[V]) probeStart(hash uint64) int {
	return int((uint64(uint32(hash)) * uint64(len(t.slots))) >> 32)
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

// tightCapacity returns the smallest capacity, in steps of 8 slots, that holds
// n live keys under the same occupancy rule as capacityAccepts. Growth still
// doubles; tight sizing is for tables that are idle or rebuilt in one pass,
// where power-of-two rounding would leave up to half the slots empty.
func tightCapacity(n int) int {
	if n <= initialCapacity {
		return initialCapacity
	}
	capacity := (n*5 + 3) / 4
	capacity = (capacity + 7) &^ 7
	for !capacityAccepts(n, capacity) {
		capacity += 8
	}
	return capacity
}

// Reserve rebuilds the table at the tightest capacity that holds max(n, Len())
// keys. It also drops tombstones. Callers provide synchronization.
func (t *Table[V]) Reserve(n int) {
	if n < int(t.count) {
		n = int(t.count)
	}
	if n == 0 {
		t.slots = nil
		t.tinyFilter = 0
		return
	}
	capacity := tightCapacity(n)
	if capacity == len(t.slots) {
		return
	}
	t.rebuild(capacity)
}

func (t *Table[V]) rebuild(capacity int) {
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

func (t *Table[V]) capacityFor(n int) int {
	capacity := len(t.slots)
	if n == 0 {
		return capacity
	}
	if capacity == 0 {
		capacity = initialCapacity
	}
	for !capacityAccepts(n, capacity) {
		if capacity < gradualGrowthMin {
			capacity *= 2
			continue
		}
		// Doubling leaves a table anywhere from 40% to 80% full, so a shard
		// that happens to stop growing just after a doubling carries almost
		// 2x its slots for the whole load. Larger tables instead grow to hold
		// a quarter more keys than they need now, which keeps the slack near
		// 25-55% at the cost of more (cheap, shard-local) rebuilds.
		capacity = tightCapacity(n + n/4)
	}
	return capacity
}

// gradualGrowthMin is the table size from which growth stops doubling. Below
// it the tables are tiny and doubling's few rebuilds are cheaper than the
// memory it wastes.
const gradualGrowthMin = 256

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
	size := len(t.slots)
	keyLen := uint64(len(key))
	fingerprint := hashFingerprint(hash)
	for n, i := 0, t.probeStart(hash); n < size; n, i = n+1, i+1 {
		if i == size {
			i = 0
		}
		s := &t.slots[i]
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

	size := len(t.slots)
	keyLen := uint64(len(key))
	fingerprint := hashFingerprint(hash)
	for n, i := 0, t.probeStart(hash); n < size; n, i = n+1, i+1 {
		if i == size {
			i = 0
		}
		s := &t.slots[i]
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
		t.insertHashed(key, value, hash)
		return
	}
	t.growForInsert()
	t.insertKnownAbsentHashed(key, value, hash)
}

// insertKnownAbsentHashed inserts a key after the caller has already proved,
// under the owning shard lock, that the exact key is absent. Probe traversal
// still follows the normal collision chain, but live slots do not need an
// exact-key comparison a second time.
func (t *Table[V]) insertKnownAbsentHashed(key string, value V, hash uint64) {
	if len(t.slots) == initialCapacity {
		t.tinyFilter |= tinyFilterBits(hash)
	}
	size := len(t.slots)
	deleted := -1
	for n, i := 0, t.probeStart(hash); n < size; n, i = n+1, i+1 {
		if i == size {
			i = 0
		}
		s := &t.slots[i]
		switch s.state() {
		case stateLive:
			continue
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

func (t *Table[V]) growForInsert() {
	capacity := t.capacityFor(int(t.count) + 1)
	if capacity == len(t.slots) {
		return
	}
	t.rebuild(capacity)
}

func (t *Table[V]) insert(key string, value V) {
	t.insertHashed(key, value, Hash(key))
}

func (t *Table[V]) insertHashed(key string, value V, hash uint64) {
	if len(t.slots) == initialCapacity {
		t.tinyFilter |= tinyFilterBits(hash)
	}
	size := len(t.slots)
	deleted := -1
	for n, i := 0, t.probeStart(hash); n < size; n, i = n+1, i+1 {
		if i == size {
			i = 0
		}
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
	size := len(t.slots)
	keyLen := uint64(len(key))
	fingerprint := hashFingerprint(hash)
	for n, i := 0, t.probeStart(hash); n < size; n, i = n+1, i+1 {
		if i == size {
			i = 0
		}
		s := &t.slots[i]
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
	t.Reserve(int(t.count))
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
