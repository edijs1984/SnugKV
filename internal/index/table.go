// Package index provides a collision-safe open-addressed index with explicit capacity.
package index

import (
	"encoding/binary"
	"hash/maphash"
	"unsafe"
)

// Slot layout (one uint64 per slot, no pointers):
//
//	bit  63      live
//	bits 57..62  hash fingerprint (6 bits)
//	bits 30..56  value, e.g. the shard entry ID (27 bits)
//	bits  0..29  byte offset of the key record in the table's key log (30 bits)
//
// The zero word is an empty slot and the word 1 is a tombstone; neither has the
// live bit set. A slot is half the size of the earlier pointer-and-metadata
// layout, and because it holds no pointer the garbage collector never scans it.
//
// Keys are not referenced from the slots by pointer. They are appended to a
// per-table key log as a uvarint length followed by the key bytes, so a key
// costs its own length plus one byte instead of a separate heap object.
//
// The log is append-only. A record is never overwritten or moved in place:
// growth and compaction copy into a new array. Strings handed out by All and
// Sample therefore keep pointing at bytes that never change, even after the
// table has moved on to a newer log.
const (
	liveBit       = uint64(1) << 63
	slotTombstone = uint64(1)

	fingerprintShift = 57
	fingerprintMask  = uint64(0x3f)

	valueShift = 30
	valueMask  = uint64(1)<<27 - 1
	offsetMask = uint64(1)<<30 - 1

	// MaxValue is the largest value a Table can store.
	MaxValue = uint32(valueMask)
	// maxLogBytes is the largest key log a single Table can address.
	maxLogBytes = int(offsetMask) + 1

	initialCapacity = 4
	slotSize        = 8

	// A key log below this size is not worth compacting for dead bytes alone.
	minCompactLogBytes = 4096
)

// Table maps string keys to ~uint32 values. It is not safe for concurrent use.
type Table[V ~uint32] struct {
	slots      []uint64
	log        []byte
	deadBytes  uint32
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
	return (hash >> 58) & fingerprintMask
}

func New[V ~uint32]() *Table[V] { return &Table[V]{} }
func (t *Table[V]) Len() int     { return int(t.count) }

func slotWord(fingerprint uint64, value uint32, offset uint32) uint64 {
	return liveBit | fingerprint<<fingerprintShift | uint64(value)<<valueShift | uint64(offset)
}

func slotFingerprint(w uint64) uint64 { return (w >> fingerprintShift) & fingerprintMask }
func slotValue(w uint64) uint32        { return uint32((w >> valueShift) & valueMask) }
func slotOffset(w uint64) uint32       { return uint32(w & offsetMask) }

// keyRecord decodes the key stored at offset and returns its bytes' position.
func (t *Table[V]) keyRecord(offset uint32) (start, n int) {
	b := t.log[offset:]
	if b[0] < 0x80 {
		return int(offset) + 1, int(b[0])
	}
	v, w := binary.Uvarint(b)
	return int(offset) + w, int(v)
}

func (t *Table[V]) keyAt(offset uint32) string {
	if t.log[offset] == packEscape {
		var parts packedParts
		parts.parse(t.log[offset:])
		var buf [packMaxKey]byte
		return string(parts.appendKey(buf[:0]))
	}
	start, n := t.keyRecord(offset)
	if n == 0 {
		return ""
	}
	return unsafe.String(&t.log[start], n)
}

func uvarintLen(n int) int {
	l := 1
	for n >= 0x80 {
		n >>= 7
		l++
	}
	return l
}

func recordBytes(keyLen int) int { return uvarintLen(keyLen) + keyLen }

// KeyRecordBytes is the key-log space one key of the given length occupies.
func KeyRecordBytes(keyLen int) int { return recordBytes(keyLen) }

// probeStart maps the low 32 hash bits onto [0, len(slots)) with a multiply
// and shift, so table capacities need not be powers of two. The shard selector
// uses hash bits 32-39 and the fingerprint bits 58-63, so the probe start stays
// independent of both.
func (t *Table[V]) probeStart(hash uint64) int {
	return int((uint64(uint32(hash)) * uint64(len(t.slots))) >> 32)
}

// CapacityBytes is the reserved size of the slot array. Key bytes are reported
// separately by KeyLogBytes and charged by callers per live key.
func (t *Table[V]) CapacityBytes() uint64 {
	return uint64(cap(t.slots)) * slotSize
}

// KeyLogBytes is the reserved size of the key log, including dead records that
// a later rebuild will reclaim.
func (t *Table[V]) KeyLogBytes() uint64 { return uint64(cap(t.log)) }

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
// keys. It also drops tombstones and dead key records, and trims spare key-log
// capacity. Callers provide synchronization.
func (t *Table[V]) Reserve(n int) {
	t.ReserveKeys(n, 0)
}

// ReserveKeys is Reserve for a caller that is about to insert n keys with a
// known total key-log size, so the log can be allocated once at its final size.
func (t *Table[V]) ReserveKeys(n int, keyLogBytes int) {
	if n < int(t.count) {
		n = int(t.count)
	}
	if n == 0 {
		t.slots = nil
		t.log = nil
		t.deadBytes = 0
		t.tinyFilter = 0
		return
	}
	capacity := tightCapacity(n)
	if capacity != len(t.slots) || t.deadBytes > 0 {
		t.rebuildPacked(capacity, keyLogBytes)
		return
	}
	t.trimLog(keyLogBytes)
}

// trimLog shrinks spare key-log capacity, keeping room for extra more bytes.
func (t *Table[V]) trimLog(extra int) {
	want := len(t.log) + extra
	if cap(t.log) <= want {
		return
	}
	next := make([]byte, len(t.log), want)
	copy(next, t.log)
	t.log = next
}

func (t *Table[V]) rebuild(capacity int) {
	// Reuse the key log unless enough of it is dead to be worth packing.
	if t.deadBytes > minCompactLogBytes && int(t.deadBytes)*4 > len(t.log) {
		t.rebuildPacked(capacity, 0)
		return
	}
	t.rebuildSlots(capacity)
}

// rebuildSlots re-places every live key in a new slot array. Key offsets stay
// valid because the log is untouched.
func (t *Table[V]) rebuildSlots(capacity int) {
	old := t.slots
	t.slots = make([]uint64, capacity)
	t.count = 0
	t.tinyFilter = 0
	for _, w := range old {
		if w&liveBit == 0 {
			continue
		}
		t.placeKnownAbsent(t.recordHash(slotOffset(w)), slotValue(w), slotOffset(w))
	}
}

// rebuildPacked rebuilds the slots and copies the live key records into a fresh
// exactly-sized log, dropping dead records. extra reserves additional log room.
func (t *Table[V]) rebuildPacked(capacity int, extra int) {
	old := t.slots
	live := len(t.log) - int(t.deadBytes)
	oldLog := t.log
	t.log = make([]byte, 0, live+extra)
	t.deadBytes = 0
	t.slots = make([]uint64, capacity)
	t.count = 0
	t.tinyFilter = 0
	for _, w := range old {
		if w&liveBit == 0 {
			continue
		}
		offset := uint32(len(t.log))
		size := recordLen(oldLog, slotOffset(w))
		t.log = append(t.log, oldLog[slotOffset(w):int(slotOffset(w))+size]...)
		t.placeKnownAbsent(t.recordHash(offset), slotValue(w), offset)
	}
}

// recordHash hashes the key stored at offset without allocating.
func (t *Table[V]) recordHash(offset uint32) uint64 {
	b := t.log[offset:]
	if b[0] == packEscape {
		var parts packedParts
		parts.parse(b)
		// Most packed keys are far shorter than the limit, so a small buffer
		// avoids clearing a large one on every rehash.
		var small [160]byte
		return HashBytes(parts.appendKey(small[:0]))
	}
	start, n := t.keyRecord(offset)
	if n == 0 {
		return Hash("")
	}
	return Hash(unsafe.String(&t.log[start], n))
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
		// half as many keys again as they need now. Every rebuild rehashes each key
		// (a cache miss per key), so a gentler step costs more write CPU than the
		// extra slots cost memory; Compact tightens the table once writes stop.
		capacity = tightCapacity(n + n/2)
	}
	return capacity
}

// gradualGrowthMin is the table size from which growth stops doubling. Below
// it the tables are tiny and doubling's few rebuilds are cheaper than the
// memory it wastes.
const gradualGrowthMin = 256

// GrowthBytes is the slot-array growth that inserting additional more keys
// would cause. Key-log bytes are charged by the caller per live key.
func (t *Table[V]) GrowthBytes(additional int) uint64 {
	return uint64(t.capacityFor(int(t.count)+additional)-len(t.slots)) * slotSize
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
	fingerprint := hashFingerprint(hash)
	for n, i := 0, t.probeStart(hash); n < size; n, i = n+1, i+1 {
		if i == size {
			i = 0
		}
		w := t.slots[i]
		if w == 0 {
			return zero, false
		}
		if w&liveBit == 0 || slotFingerprint(w) != fingerprint {
			continue
		}
		if t.keyEquals(slotOffset(w), key) {
			return V(slotValue(w)), true
		}
	}
	return zero, false
}

func (t *Table[V]) keyEquals(offset uint32, key string) bool {
	b := t.log[offset:]
	if b[0] == packEscape {
		var parts packedParts
		parts.parse(b)
		return parts.equals(key)
	}
	n, w := int(b[0]), 1
	if n >= 0x80 {
		v, width := binary.Uvarint(b)
		n, w = int(v), width
	}
	if n != len(key) {
		return false
	}
	return string(b[w:w+n]) == key
}

func (t *Table[V]) GetHashedBytes(key []byte, hash uint64) (V, bool) {
	var lookup string
	if len(key) > 0 {
		// The transient string aliases only the caller's lookup bytes for the
		// duration of this method. It is never stored in the table.
		lookup = unsafe.String(unsafe.SliceData(key), len(key))
	}
	return t.GetHashed(lookup, hash)
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

func checkValue(value uint32) {
	if value > uint32(valueMask) {
		panic("index value exceeds the 27-bit slot capacity")
	}
}

// appendKey adds a key record to the log and returns its offset.
func (t *Table[V]) appendKey(key string) uint32 {
	packed, isPacked := packKey(key)
	need := recordBytes(len(key))
	switch {
	case isPacked:
		need = packed.size
	case len(key) == 0:
		need = 2
	}
	if len(t.log)+need > maxLogBytes {
		panic("index key log exceeds its 1 GiB capacity")
	}
	if cap(t.log)-len(t.log) < need {
		grow := len(t.log) / 4
		if grow < 256 {
			grow = 256
		}
		next := make([]byte, len(t.log), len(t.log)+need+grow)
		copy(next, t.log)
		t.log = next
	}
	offset := uint32(len(t.log))
	switch {
	case isPacked:
		t.log = packed.appendTo(t.log)
	case len(key) == 0:
		t.log = append(t.log, packEscape, packEmpty)
	default:
		t.log = binary.AppendUvarint(t.log, uint64(len(key)))
		t.log = append(t.log, key...)
	}
	return offset
}

// placeKnownAbsent stores an already-logged key at its probe position. The
// caller has established that the key is not in the table.
func (t *Table[V]) placeKnownAbsent(hash uint64, value uint32, offset uint32) {
	if len(t.slots) == initialCapacity {
		t.tinyFilter |= tinyFilterBits(hash)
	}
	word := slotWord(hashFingerprint(hash), value, offset)
	size := len(t.slots)
	deleted := -1
	for n, i := 0, t.probeStart(hash); n < size; n, i = n+1, i+1 {
		if i == size {
			i = 0
		}
		switch w := t.slots[i]; {
		case w == 0:
			if deleted >= 0 {
				i = deleted
			}
			t.slots[i] = word
			t.count++
			return
		case w&liveBit != 0:
			continue
		default:
			if deleted < 0 {
				deleted = i
			}
		}
	}
	if deleted >= 0 {
		t.slots[deleted] = word
		t.count++
		return
	}
	panic("index capacity invariant")
}

// insertKnownAbsentHashed inserts a key after the caller has already proved,
// under the owning shard lock, that the exact key is absent.
func (t *Table[V]) insertKnownAbsentHashed(key string, value V, hash uint64) {
	checkValue(uint32(value))
	t.placeKnownAbsent(hash, uint32(value), t.appendKey(key))
}

func (t *Table[V]) growForInsert() {
	capacity := t.capacityFor(int(t.count) + 1)
	if capacity == len(t.slots) {
		return
	}
	t.rebuild(capacity)
}

func (t *Table[V]) insertHashed(key string, value V, hash uint64) {
	checkValue(uint32(value))
	if len(t.slots) == initialCapacity {
		t.tinyFilter |= tinyFilterBits(hash)
	}
	fingerprint := hashFingerprint(hash)
	size := len(t.slots)
	deleted := -1
	for n, i := 0, t.probeStart(hash); n < size; n, i = n+1, i+1 {
		if i == size {
			i = 0
		}
		w := t.slots[i]
		switch {
		case w == 0:
			if deleted >= 0 {
				i = deleted
			}
			t.slots[i] = slotWord(fingerprint, uint32(value), t.appendKey(key))
			t.count++
			return
		case w&liveBit != 0:
			if slotFingerprint(w) == fingerprint && t.keyEquals(slotOffset(w), key) {
				t.slots[i] = slotWord(fingerprint, uint32(value), slotOffset(w))
				return
			}
		default:
			if deleted < 0 {
				deleted = i
			}
		}
	}
	if deleted >= 0 {
		t.slots[deleted] = slotWord(fingerprint, uint32(value), t.appendKey(key))
		t.count++
		return
	}
	panic("index capacity invariant")
}

func (t *Table[V]) Delete(key string) {
	t.deleteHashed(key, Hash(key))
}

func (t *Table[V]) deleteHashed(key string, hash uint64) {
	if len(t.slots) == 0 {
		return
	}
	if len(t.slots) == initialCapacity && t.count == initialCapacity {
		bits := tinyFilterBits(hash)
		if t.tinyFilter&bits != bits {
			return
		}
	}
	size := len(t.slots)
	fingerprint := hashFingerprint(hash)
	for n, i := 0, t.probeStart(hash); n < size; n, i = n+1, i+1 {
		if i == size {
			i = 0
		}
		w := t.slots[i]
		if w == 0 {
			return
		}
		if w&liveBit == 0 || slotFingerprint(w) != fingerprint || !t.keyEquals(slotOffset(w), key) {
			continue
		}
		t.deadBytes += uint32(recordLen(t.log, slotOffset(w)))
		t.slots[i] = slotTombstone
		t.count--
		if t.count == 0 {
			t.tinyFilter = 0
			t.log = nil
			t.deadBytes = 0
			return
		}
		// Deleted keys leave dead records in the append-only log. Pack the
		// table once they outweigh a third of it, so a churning workload
		// cannot grow the log without bound.
		if t.deadBytes > minCompactLogBytes && int(t.deadBytes)*3 > len(t.log) {
			t.rebuildPacked(len(t.slots), 0)
		}
		return
	}
}

// All iterates live slots without allocating; callers provide synchronization.
// The yielded keys stay valid after the table changes.
func (t *Table[V]) All() func(func(string, V) bool) {
	return func(yield func(string, V) bool) {
		var chunk keyChunk
		for _, w := range t.slots {
			if w&liveBit != 0 && !yield(t.keyChunked(&chunk, slotOffset(w)), V(slotValue(w))) {
				return
			}
		}
	}
}

// keyChunked is keyAt for iteration: packed keys are decoded into chunk.
func (t *Table[V]) keyChunked(chunk *keyChunk, offset uint32) string {
	if t.log[offset] == packEscape {
		return chunk.decode(t.log[offset:])
	}
	return t.keyAt(offset)
}

// KeyRef locates a key in the table's log without decoding it.
type KeyRef struct{ offset uint32 }

// Key decodes the key a KeyRef from AllRefs points at. A ref is valid only
// until the table next changes.
func (t *Table[V]) Key(r KeyRef) string { return t.keyAt(r.offset) }

// KeyLen is the decoded length of the key a KeyRef points at.
func (t *Table[V]) KeyLen(r KeyRef) int {
	b := t.log[r.offset:]
	if b[0] == packEscape {
		var parts packedParts
		parts.parse(b)
		return parts.keyLen()
	}
	_, n := t.keyRecord(r.offset)
	return n
}

// AllRefs iterates live slots like All but defers decoding each key. Callers
// that need only a key's length, or the key of a few entries, avoid decoding
// the rest.
func (t *Table[V]) AllRefs() func(func(KeyRef, V) bool) {
	return func(yield func(KeyRef, V) bool) {
		for _, w := range t.slots {
			if w&liveBit != 0 && !yield(KeyRef{slotOffset(w)}, V(slotValue(w))) {
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
	var chunk keyChunk
	cursor %= len(t.slots)
	for n := 0; n < budget && n < len(t.slots); n++ {
		w := t.slots[cursor]
		cursor = (cursor + 1) % len(t.slots)
		if w&liveBit != 0 {
			out = append(out, t.keyChunked(&chunk, slotOffset(w)))
			if len(out) == limit {
				break
			}
		}
	}
	return out, cursor
}

func (t *Table[V]) EntryBytes() uint64 {
	return slotSize
}
