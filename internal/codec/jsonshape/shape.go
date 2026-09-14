// Package jsonshape preserves exact JSON syntax while sharing literal templates.
package jsonshape

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/fnv"
	"snugkv/internal/codec/dictionary"
	"sync"
)

const MaxInput = 1 << 20
const MaxSlots = 4096

// Schema is immutable. References and table accounting are owned by Store.
type Schema struct {
	dictionary *dictionary.Store
	Literal    [][]byte
	Key        string
	Bytes      int
	refs       int
}
type Store struct {
	dictionary *dictionary.Store
	mu         sync.Mutex
	schemas    map[string]*Schema
	frequency  [256]uint8
	observed   map[string]uint8
	used, max  int
	threshold  uint8
}

func New(maxBytes int, threshold uint8) *Store {
	if threshold == 0 {
		threshold = 8
	}
	return &Store{dictionary: dictionary.New(1024), schemas: make(map[string]*Schema), observed: make(map[string]uint8), max: maxBytes, threshold: threshold}
}

// Split validates JSON and preserves every byte outside primitive value spans.
func Split(src []byte) ([][]byte, [][]byte, bool) {
	if len(src) > MaxInput || !json.Valid(src) {
		return nil, nil, false
	}
	var literals, slots [][]byte
	last, depth := 0, 0
	for i := 0; i < len(src); {
		start := i
		b := src[i]
		switch {
		case b == '{' || b == '[':
			depth++
			if depth > 64 {
				return nil, nil, false
			}
			i++
			continue
		case b == '}' || b == ']':
			depth--
			i++
			continue
		case b == '"':
			i++
			for i < len(src) {
				if src[i] == '\\' {
					i += 2
					continue
				}
				if src[i] == '"' {
					i++
					break
				}
				i++
			}
			next := i
			for next < len(src) && (src[next] == ' ' || src[next] == '\n' || src[next] == '\r' || src[next] == '\t') {
				next++
			}
			if next < len(src) && src[next] == ':' {
				continue
			}
		case b == '-' || b >= '0' && b <= '9' || b == 't' || b == 'f' || b == 'n':
			for i < len(src) && src[i] != ',' && src[i] != ']' && src[i] != '}' && src[i] != ' ' && src[i] != '\r' && src[i] != '\n' && src[i] != '\t' {
				i++
			}
		default:
			i++
			continue
		}
		if len(slots) >= MaxSlots {
			return nil, nil, false
		}
		// Candidate parsing only needs immutable views into src.
		// Avoid cloning every literal and slot on every optimizer pass.
		// A schema that is actually admitted takes ownership of its
		// literals separately below.
		literals = append(literals, src[last:start])
		slots = append(slots, src[start:i])
		last = i
	}
	literals = append(literals, src[last:])
	return literals, slots, true
}
func templateKey(literals [][]byte) string {
	var b bytes.Buffer
	var buf [10]byte
	for _, literal := range literals {
		n := binary.PutUvarint(buf[:], uint64(len(literal)))
		b.Write(buf[:n])
		b.Write(literal)
	}
	return b.String()
}

// Candidate observes a shape and returns a template after bounded admission.
// Merely considering a candidate does not add a live reference.
func (s *Store) Candidate(src []byte) (*Schema, [][]byte, bool) {
	literals, slots, ok := Split(src)
	if !ok || len(slots) == 0 {
		return nil, nil, false
	}
	key := templateKey(literals)
	h := fnv.New64a()
	h.Write([]byte(key))
	index := h.Sum64() % uint64(len(s.frequency))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frequency[index] < 255 {
		s.frequency[index]++
	}
	if len(s.observed) >= 128 {
		clear(s.observed)
	}
	if s.observed[key] < 255 {
		s.observed[key]++
	}
	if existing, ok := s.schemas[key]; ok {
		return existing, slots, true
	}
	if s.observed[key] < s.threshold {
		return nil, nil, false
	}
	cost := 128 + len(key)
	for _, v := range literals {
		cost += len(v) + 24
	}
	if cost > s.max {
		return nil, nil, false
	}
	for name, schema := range s.schemas {
		if s.used+cost <= s.max {
			break
		}
		if schema.refs == 0 {
			delete(s.schemas, name)
			s.used -= schema.Bytes
		}
	}
	if s.used+cost > s.max {
		return nil, nil, false
	}
	schema := &Schema{dictionary: s.dictionary, Literal: literals, Key: key, Bytes: cost}
	s.schemas[key] = schema
	s.used += cost
	return schema, slots, true
}
func (s *Store) Retain(schema *Schema) bool {
	if schema == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.schemas[schema.Key]; ok {
		if existing != schema {
			return false
		}
	} else {
		if s.used+schema.Bytes > s.max {
			return false
		}
		s.schemas[schema.Key] = schema
		s.used += schema.Bytes
	}
	schema.refs++
	return true
}
func (s *Store) Release(schema *Schema) {
	if schema == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	schema.refs--
	if schema.refs == 0 {
		if s.schemas[schema.Key] == schema {
			delete(s.schemas, schema.Key)
			s.used -= schema.Bytes
		}
	}
	if schema.refs < 0 {
		panic("negative schema references")
	}
}
func (s *Store) Stats() (int, int) { s.mu.Lock(); defer s.mu.Unlock(); return len(s.schemas), s.used }
func EncodeSlots(slots [][]byte) []byte {
	var out []byte
	var b [10]byte
	for _, slot := range slots {
		out = append(out, 0)
		n := binary.PutUvarint(b[:], uint64(len(slot)))
		out = append(out, b[:n]...)
		out = append(out, slot...)
	}
	return out
}
func Decode(schema *Schema, data []byte, max int) ([]byte, error) {
	if schema == nil || max < 0 {
		return nil, errors.New("missing schema or invalid bound")
	}
	out := make([]byte, 0, max)
	for i, literal := range schema.Literal {
		if len(literal) > max-len(out) {
			return nil, errors.New("shape output exceeds limit")
		}
		out = append(out, literal...)
		if i == len(schema.Literal)-1 {
			break
		}
		if len(data) == 0 {
			return nil, errors.New("missing shape slot")
		}
		tag := data[0]
		data = data[1:]
		n, used := binary.Uvarint(data)
		if used <= 0 {
			return nil, errors.New("invalid shape slot")
		}
		data = data[used:]
		var slot []byte
		switch tag {
		case 0:
			if n > uint64(len(data)) {
				return nil, errors.New("truncated raw slot")
			}
			slot = data[:int(n)]
			data = data[int(n):]
		case 1:
			if schema.dictionary == nil {
				return nil, errors.New("missing dictionary")
			}
			var ok bool
			slot, ok = schema.dictionary.LookupView(n)
			if !ok {
				return nil, errors.New("missing dictionary entry")
			}
		default:
			return nil, errors.New("invalid slot tag")
		}
		if len(slot) > max-len(out) {
			return nil, errors.New("shape output exceeds limit")
		}
		out = append(out, slot...)

	}
	if len(data) != 0 {
		return nil, errors.New("trailing shape data")
	}
	return out, nil
}
