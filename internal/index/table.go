// Package index provides a collision-safe open-addressed index with explicit capacity.
package index

import "unsafe"

const (
	stateEmpty   = uint64(0)
	stateLive    = uint64(1)
	stateDeleted = uint64(2)
	stateShift   = 62
	keyLengthMask = uint64(1<<30) - 1
)

// slot is intentionally 16 bytes on 64-bit targets:
//
//   - keyData keeps the immutable Go string bytes alive and gives exact-key
//     comparison without retaining a full 16-byte string header in every slot;
//   - meta packs 2 bits of state, 30 bits of key length, and the full uint32 value.
//
// SnugKV's RESP key limit is far below the 1 GiB key-length ceiling. Exact key
// bytes are still compared after hashing, so hash collisions remain fully safe.
type slot[V ~uint32] struct {
	keyData *byte
	meta    uint64
}

type Table[V ~uint32] struct {
	slots []slot[V]
	count int
	hash  func(string) uint64
}

func Hash(key string) uint64 {
	h := uint64(14695981039346656037)
	for i := range key {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return h
}

func New[V ~uint32]() *Table[V] { return &Table[V]{hash: Hash} }
func (t *Table[V]) Len() int     { return t.count }

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

func (s *slot[V]) setLive(key string, value V) {
	if uint64(len(key)) > keyLengthMask {
		panic("index key too large")
	}
	if len(key) == 0 {
		s.keyData = nil
	} else {
		s.keyData = unsafe.StringData(key)
	}
	s.meta = stateLive<<stateShift | uint64(len(key))<<32 | uint64(uint32(value))
}

func (s *slot[V]) setDeleted() {
	s.keyData = nil
	s.meta = stateDeleted << stateShift
}

func (t *Table[V]) CapacityBytes() uint64 {
	return uint64(cap(t.slots)) * slotBytes[V]()
}

func (t *Table[V]) capacityFor(n int) int {
	capacity := len(t.slots)
	if n == 0 {
		return capacity
	}
	if capacity == 0 {
		capacity = 8
	}
	for n > capacity*8/10 {
		capacity *= 2
	}
	return capacity
}

func (t *Table[V]) GrowthBytes(additional int) uint64 {
	return uint64(t.capacityFor(t.count+additional)-len(t.slots)) * slotBytes[V]()
}

func (t *Table[V]) Get(key string) (V, bool) {
	var zero V
	if len(t.slots) == 0 {
		return zero, false
	}
	hash := t.hash(key)
	mask := uint64(len(t.slots) - 1)
	for n := 0; n < len(t.slots); n++ {
		s := &t.slots[(hash+uint64(n))&mask]
		switch s.state() {
		case stateEmpty:
			return zero, false
		case stateLive:
			if s.keyLen() == len(key) && s.key() == key {
				return s.value(), true
			}
		}
	}
	return zero, false
}

func (t *Table[V]) Set(key string, value V) {
	if _, ok := t.Get(key); !ok {
		capacity := t.capacityFor(t.count + 1)
		if capacity != len(t.slots) {
			old := t.slots
			t.slots = make([]slot[V], capacity)
			t.count = 0
			for i := range old {
				s := &old[i]
				if s.state() == stateLive {
					t.insert(s.key(), s.value())
				}
			}
		}
	}
	t.insert(key, value)
}

func (t *Table[V]) insert(key string, value V) {
	hash := t.hash(key)
	mask := uint64(len(t.slots) - 1)
	deleted := -1
	for n := 0; n < len(t.slots); n++ {
		i := int((hash + uint64(n)) & mask)
		s := &t.slots[i]
		switch s.state() {
		case stateLive:
			if s.keyLen() == len(key) && s.key() == key {
				s.setLive(key, value)
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
			s.setLive(key, value)
			t.count++
			return
		}
	}
	if deleted >= 0 {
		t.slots[deleted].setLive(key, value)
		t.count++
		return
	}
	panic("index capacity invariant")
}

func (t *Table[V]) Delete(key string) {
	if len(t.slots) == 0 {
		return
	}
	hash := t.hash(key)
	mask := uint64(len(t.slots) - 1)
	for n := 0; n < len(t.slots); n++ {
		s := &t.slots[(hash+uint64(n))&mask]
		switch s.state() {
		case stateEmpty:
			return
		case stateLive:
			if s.keyLen() == len(key) && s.key() == key {
				s.setDeleted()
				t.count--
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
	capacity := 8
	if t.count == 0 {
		t.slots = nil
		return
	}
	for t.count > capacity*8/10 {
		capacity *= 2
	}
	old := t.slots
	t.slots = make([]slot[V], capacity)
	t.count = 0
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
