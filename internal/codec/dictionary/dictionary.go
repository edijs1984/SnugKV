// Package dictionary admits repeated byte strings into a bounded local table.
package dictionary

import (
	"bytes"
	"encoding/binary"
	"hash/fnv"
	"sync"
)

type item struct {
	id    uint64
	value []byte
	refs  int
	cost  int
}
type Store struct {
	mu        sync.RWMutex
	byValue   map[string]*item
	byID      map[uint64]*item
	sketch    [256]uint8
	observed  map[string]uint8
	next      uint64
	used, max int
}

func New(max int) *Store {
	return &Store{byValue: make(map[string]*item), byID: make(map[uint64]*item), observed: make(map[string]uint8), max: max}
}

// Candidate IDs are never reused; missing candidates are rejected at commit.
func (s *Store) Candidate(value []byte) (uint64, bool) {
	if len(value) < 4 || len(value) > 4096 {
		return 0, false
	}
	h := fnv.New64a()
	h.Write(value)
	slot := h.Sum64() % uint64(len(s.sketch))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sketch[slot] < 255 {
		s.sketch[slot]++
	}
	if found := s.byValue[string(value)]; found != nil {
		return found.id, true
	}
	key := string(value)
	if len(s.observed) >= 128 {
		clear(s.observed)
	}
	if s.observed[key] < 255 {
		s.observed[key]++
	}
	cost := 128 + 2*len(value)
	count := int(s.observed[key])
	if count < 8 || (len(value)-2)*count <= cost {
		return 0, false
	}
	for key, entry := range s.byValue {
		if s.used+cost <= s.max {
			break
		}
		if entry.refs == 0 {
			delete(s.byValue, key)
			delete(s.byID, entry.id)
			s.used -= entry.cost
		}
	}
	if cost > s.max-s.used || s.next == ^uint64(0) {
		return 0, false
	}
	s.next++
	entry := &item{id: s.next, value: bytes.Clone(value), cost: cost}
	s.byValue[string(value)] = entry
	s.byID[entry.id] = entry
	s.used += cost
	return entry.id, true
}
func (s *Store) Lookup(id uint64) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry := s.byID[id]
	if entry == nil {
		return nil, false
	}

	return bytes.Clone(entry.value), true
}

// LookupView returns an immutable view of a dictionary value.
//
// Callers must not modify the returned bytes. Dictionary entries are immutable
// after insertion, so this avoids an allocation on hot decode paths while
// allowing concurrent readers.
func (s *Store) LookupView(id uint64) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry := s.byID[id]
	if entry == nil {
		return nil, false
	}

	return entry.value, true
}
func (s *Store) Retain(ids []uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if s.byID[id] == nil {
			return false
		}
	}
	for _, id := range ids {
		s.byID[id].refs++
	}
	return true
}
func (s *Store) Release(ids []uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		entry := s.byID[id]
		if entry == nil || entry.refs <= 0 {
			panic("invalid dictionary reference")
		}
		entry.refs--
		if entry.refs == 0 {
			delete(s.byID, id)
			delete(s.byValue, string(entry.value))
			s.used -= entry.cost
		}
	}
}
func (s *Store) Stats() (int, int) { s.mu.Lock(); defer s.mu.Unlock(); return len(s.byID), s.used }
func IDBytes(id uint64) []byte {
	var b [10]byte
	n := binary.PutUvarint(b[:], id)
	return bytes.Clone(b[:n])
}
