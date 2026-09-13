// Package index provides a collision-safe open-addressed index with explicit capacity.
package index

import "reflect"

type slot[V any] struct {
	hash  uint64
	key   string
	value V
	state uint8
}
type Table[V any] struct {
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
func New[V any]() *Table[V]  { return &Table[V]{hash: Hash} }
func (t *Table[V]) Len() int { return t.count }
func (t *Table[V]) CapacityBytes() uint64 {
	return uint64(cap(t.slots)) * uint64(reflect.TypeOf(slot[V]{}).Size())
}
func (t *Table[V]) capacityFor(n int) int {
	capacity := len(t.slots)
	if n == 0 {
		return capacity
	}
	if capacity == 0 {
		capacity = 8
	}
	for n > capacity*7/10 {
		capacity *= 2
	}
	return capacity
}
func (t *Table[V]) GrowthBytes(additional int) uint64 {
	return uint64(t.capacityFor(t.count+additional)-len(t.slots)) * uint64(reflect.TypeOf(slot[V]{}).Size())
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
		if s.state == 0 {
			return zero, false
		}
		if s.state == 1 && s.hash == hash && s.key == key {
			return s.value, true
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
			for _, s := range old {
				if s.state == 1 {
					t.insert(s.key, s.value)
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
		if s.state == 1 && s.hash == hash && s.key == key {
			s.value = value
			return
		}
		if s.state == 2 && deleted < 0 {
			deleted = i
		}
		if s.state == 0 {
			if deleted >= 0 {
				s = &t.slots[deleted]
			}
			*s = slot[V]{hash, key, value, 1}
			t.count++
			return
		}
	}
	if deleted >= 0 {
		t.slots[deleted] = slot[V]{hash, key, value, 1}
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
		if s.state == 0 {
			return
		}
		if s.state == 1 && s.hash == hash && s.key == key {
			var zero V
			s.key = ""
			s.value = zero
			s.state = 2
			t.count--
			return
		}
	}
}

// All iterates live slots without allocating; callers provide synchronization.
func (t *Table[V]) All() func(func(string, V) bool) {
	return func(yield func(string, V) bool) {
		for i := range t.slots {
			s := &t.slots[i]
			if s.state == 1 && !yield(s.key, s.value) {
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
	for t.count > capacity*7/10 {
		capacity *= 2
	}
	old := t.slots
	t.slots = make([]slot[V], capacity)
	t.count = 0
	for _, s := range old {
		if s.state == 1 {
			t.insert(s.key, s.value)
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
		if s.state == 1 {
			out = append(out, s.key)
			if len(out) == limit {
				break
			}
		}
	}
	return out, cursor
}
func (t *Table[V]) EntryBytes() uint64 {
	return uint64(reflect.TypeOf(slot[V]{}).Size())
}