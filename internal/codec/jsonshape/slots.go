package jsonshape

import (
	"encoding/binary"
	"errors"
	"snugkv/internal/codec/dictionary"
)

// Slot tag zero is raw bytes; one is a dictionary ID. IDs never repeat within
// the lifetime of a dictionary, preventing stale-reference aliasing.
func (s *Store) EncodeSlots(slots [][]byte) []byte {
	var out []byte
	var b [10]byte
	for _, slot := range slots {
		if id, ok := s.dictionary.Candidate(slot); ok {
			encoded := dictionary.IDBytes(id)
			if len(encoded)+1 < len(slot)+2 {
				out = append(out, 1)
				out = append(out, encoded...)
				continue
			}
		}
		out = append(out, 0)
		n := binary.PutUvarint(b[:], uint64(len(slot)))
		out = append(out, b[:n]...)
		out = append(out, slot...)
	}
	return out
}
func slotIDs(data []byte, count int) ([]uint64, error) {
	var ids []uint64
	for i := 0; i < count; i++ {
		if len(data) == 0 {
			return nil, errors.New("missing slot")
		}
		tag := data[0]
		data = data[1:]
		n, used := binary.Uvarint(data)
		if used <= 0 {
			return nil, errors.New("invalid slot")
		}
		data = data[used:]
		switch tag {
		case 0:
			if n > uint64(len(data)) {
				return nil, errors.New("truncated slot")
			}
			data = data[int(n):]
		case 1:
			ids = append(ids, n)
		default:
			return nil, errors.New("invalid slot tag")
		}
	}
	if len(data) != 0 {
		return nil, errors.New("trailing slot data")
	}
	return ids, nil
}
func (s *Store) RetainRecord(schema *Schema, data []byte) bool {
	ids, err := slotIDs(data, len(schema.Literal)-1)
	if err != nil || !s.dictionary.Retain(ids) {
		return false
	}
	if !s.Retain(schema) {
		s.dictionary.Release(ids)
		return false
	}
	return true
}
func (s *Store) ReleaseRecord(schema *Schema, data []byte) {
	ids, err := slotIDs(data, len(schema.Literal)-1)
	if err != nil {
		panic(err)
	}
	s.dictionary.Release(ids)
	s.Release(schema)
}
func (s *Store) DictionaryStats() (int, int) { return s.dictionary.Stats() }

func (s *Store) TotalBytes() int {
	_, schemaBytes := s.Stats()
	_, dictionaryBytes := s.DictionaryStats()
	return schemaBytes + dictionaryBytes
}
