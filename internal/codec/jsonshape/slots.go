package jsonshape

import (
	"encoding/binary"
	"errors"
	"snugkv/internal/codec/dictionary"
	"strconv"
)

const (
	slotRaw        byte = 0
	slotDictionary byte = 1
	slotInt        byte = 2
	slotTrue       byte = 3
	slotFalse      byte = 4
	slotNull       byte = 5
)

func canonicalInt(slot []byte) (int64, bool) {
	if len(slot) == 0 {
		return 0, false
	}

	n, err := strconv.ParseInt(string(slot), 10, 64)
	if err != nil {
		return 0, false
	}

	// Preserve JSON bytes exactly. This intentionally rejects alternate
	// representations such as -0.
	if strconv.FormatInt(n, 10) != string(slot) {
		return 0, false
	}

	return n, true
}

// EncodeSlots stores primitive JSON values semantically when doing so is
// smaller, while preserving exact reconstruction of the original JSON bytes.
func (s *Store) EncodeSlots(slots [][]byte) []byte {
	var out []byte
	var b [10]byte

	for _, slot := range slots {
		switch string(slot) {
		case "true":
			out = append(out, slotTrue)
			continue

		case "false":
			out = append(out, slotFalse)
			continue

		case "null":
			out = append(out, slotNull)
			continue
		}

		if value, ok := canonicalInt(slot); ok {
			n := binary.PutVarint(b[:], value)

			// raw representation costs:
			// tag + length-varint + payload
			if 1+n < 1+binary.PutUvarint(b[:], uint64(len(slot)))+len(slot) {
				out = append(out, slotInt)
				n = binary.PutVarint(b[:], value)
				out = append(out, b[:n]...)
				continue
			}
		}

		if id, ok := s.dictionary.Candidate(slot); ok {
			encoded := dictionary.IDBytes(id)
			if len(encoded)+1 < len(slot)+2 {
				out = append(out, slotDictionary)
				out = append(out, encoded...)
				continue
			}
		}

		out = append(out, slotRaw)
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

		switch tag {
		case slotRaw:
			n, used := binary.Uvarint(data)
			if used <= 0 {
				return nil, errors.New("invalid raw slot")
			}
			data = data[used:]

			if n > uint64(len(data)) {
				return nil, errors.New("truncated slot")
			}

			data = data[int(n):]

		case slotDictionary:
			id, used := binary.Uvarint(data)
			if used <= 0 {
				return nil, errors.New("invalid dictionary slot")
			}
			data = data[used:]
			ids = append(ids, id)

		case slotInt:
			_, used := binary.Varint(data)
			if used <= 0 {
				return nil, errors.New("invalid integer slot")
			}
			data = data[used:]

		case slotTrue, slotFalse, slotNull:
			// Tag is the entire representation.

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

func (s *Store) DictionaryStats() (int, int) {
	return s.dictionary.Stats()
}

func (s *Store) TotalBytes() int {
	_, schemaBytes := s.Stats()
	_, dictionaryBytes := s.DictionaryStats()
	return schemaBytes + dictionaryBytes
}
