// Package jsonshape preserves exact JSON syntax while sharing literal templates.
package jsonshape

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/fnv"
	"snugkv/internal/codec/dictionary"
	"strconv"
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
	ID         uint32
	refs       int
}
type Store struct {
	dictionary *dictionary.Store
	mu         sync.Mutex
	schemas    map[string]*Schema
	byID       map[uint32]*Schema
	frequency  [256]uint8
	observed   map[string]uint8
	used, max  int
	nextID     uint32
	threshold  uint8
}

func New(maxBytes int, threshold uint8) *Store {
	if threshold == 0 {
		threshold = 8
	}
	return &Store{
		dictionary: dictionary.New(1024),
		schemas:    make(map[string]*Schema),
		byID:       make(map[uint32]*Schema),
		observed:   make(map[string]uint8),
		max:        maxBytes,
		nextID:     1,
		threshold:  threshold,
	}
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

type AdmissionStatus struct {
	Valid       bool
	Observed    uint8
	Threshold   uint8
	Admitted    bool
	SchemaCount int
	UsedBytes   int
}

func (s *Store) Admission(src []byte) AdmissionStatus {
	literals, slots, ok := Split(src)
	if !ok || len(slots) == 0 {
		return AdmissionStatus{}
	}

	key := templateKey(literals)

	s.mu.Lock()
	defer s.mu.Unlock()

	_, admitted := s.schemas[key]

	return AdmissionStatus{
		Valid:       true,
		Observed:    s.observed[key],
		Threshold:   s.threshold,
		Admitted:    admitted,
		SchemaCount: len(s.schemas),
		UsedBytes:   s.used,
	}
}

// Lookup returns an already-admitted schema without changing observation
// counters or admitting a new schema. Diagnostics and optimizer evaluation
// must use this path so merely inspecting a value cannot train the store.
func (s *Store) Lookup(src []byte) (*Schema, [][]byte, bool) {
	literals, slots, ok := Split(src)
	if !ok || len(slots) == 0 {
		return nil, nil, false
	}

	key := templateKey(literals)

	s.mu.Lock()
	schema := s.schemas[key]
	s.mu.Unlock()

	if schema == nil {
		return nil, nil, false
	}

	return schema, slots, true
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
	// Keep admission bounded without destroying all accumulated evidence.
	// This lets a global store learn many thousands of recurring shapes.
	if _, exists := s.observed[key]; !exists && len(s.observed) >= 65536 {
		for observedKey, count := range s.observed {
			if count <= 1 {
				delete(s.observed, observedKey)
				continue
			}

			s.observed[observedKey] = count / 2
		}

		// Pathological streams of one-off shapes can still leave the map
		// full. Bound it deterministically enough for memory safety.
		if len(s.observed) >= 65536 {
			target := 32768
			for observedKey := range s.observed {
				delete(s.observed, observedKey)
				if len(s.observed) <= target {
					break
				}
			}
		}
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
			delete(s.byID, schema.ID)
			s.used -= schema.Bytes
		}
	}
	if s.used+cost > s.max {
		return nil, nil, false
	}
	ownedLiterals := make([][]byte, len(literals))
	for i := range literals {
		ownedLiterals[i] = bytes.Clone(literals[i])
	}

	schema := &Schema{
		dictionary: s.dictionary,
		Literal:    ownedLiterals,
		Key:        key,
		Bytes:      cost,
		ID:         s.nextID,
	}
	s.nextID++
	if s.nextID == 0 {
		s.nextID = 1
	}
	s.schemas[key] = schema
	s.byID[schema.ID] = schema
	delete(s.observed, key)
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
		if schema.ID == 0 {
			schema.ID = s.nextID
			s.nextID++
			if s.nextID == 0 {
				s.nextID = 1
			}
		} else if existing := s.byID[schema.ID]; existing != nil && existing != schema {
			return false
		}
		s.schemas[schema.Key] = schema
		s.byID[schema.ID] = schema
		s.used += schema.Bytes
	}
	schema.refs++
	return true
}

func (s *Store) ByID(id uint32) *Schema {
	if id == 0 {
		return nil
	}
	s.mu.Lock()
	schema := s.byID[id]
	s.mu.Unlock()
	return schema
}
func (s *Store) Release(schema *Schema) {
	if schema == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	schema.refs--

	if schema.refs < 0 {
		panic("negative schema references")
	}

	// Keep zero-reference schemas admitted.
	//
	// They are learned metadata and may immediately be useful for another
	// record with the same JSON shape. Candidate admission already performs
	// bounded eviction of zero-reference schemas when the store needs space.
	//
	// Deleting here causes schema thrashing:
	// JSON-shape -> overwrite -> refs 0 -> schema forgotten -> relearn.
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

		var slot []byte

		switch tag {
		case slotRaw:
			n, used := binary.Uvarint(data)
			if used <= 0 {
				return nil, errors.New("invalid raw shape slot")
			}

			data = data[used:]

			if n > uint64(len(data)) {
				return nil, errors.New("truncated raw slot")
			}

			slot = data[:int(n)]
			data = data[int(n):]

		case slotDictionary:
			if schema.dictionary == nil {
				return nil, errors.New("missing dictionary")
			}

			id, used := binary.Uvarint(data)
			if used <= 0 {
				return nil, errors.New("invalid dictionary slot")
			}

			data = data[used:]

			var ok bool
			slot, ok = schema.dictionary.LookupView(id)
			if !ok {
				return nil, errors.New("missing dictionary entry")
			}

		case slotInt:
			value, used := binary.Varint(data)
			if used <= 0 {
				return nil, errors.New("invalid integer slot")
			}

			data = data[used:]
			slot = []byte(strconv.FormatInt(value, 10))

		case slotTrue:
			slot = []byte("true")

		case slotFalse:
			slot = []byte("false")

		case slotNull:
			slot = []byte("null")

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
