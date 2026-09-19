package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"
)

const (
	hashShapeAdmissionThreshold = 4
	hashShapeMaxShapes          = 4096
	hashShapeBudgetBytes        = 8 << 20
	hashShapeMinSavings         = 16
)

var shapedHashHeader = [...]byte{'S', 'H', 2}

type hashShape struct {
	signature []byte
}

type hashShapeCandidate struct {
	fingerprint uint64
	count       uint8
}

type hashShapeCatalog struct {
	mu            sync.Mutex
	byFingerprint map[uint64]uint16
	shapes        []hashShape
	candidates    [256]hashShapeCandidate
	bytes         uint64
}

func hashShapeFingerprint(pairs []HashPair) uint64 {
	h := uint64(14695981039346656037)
	mix := func(b byte) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(len(pairs)))
	for _, b := range buf {
		mix(b)
	}
	for _, pair := range pairs {
		binary.LittleEndian.PutUint64(buf[:], uint64(len(pair.Field)))
		for _, b := range buf {
			mix(b)
		}
		for _, b := range pair.Field {
			mix(b)
		}
	}
	return h
}

func hashShapeSignature(pairs []HashPair) []byte {
	capacity := binary.MaxVarintLen64
	for _, pair := range pairs {
		capacity += binary.MaxVarintLen64 + len(pair.Field)
	}
	out := make([]byte, 0, capacity)
	out = appendHashUvarint(out, uint64(len(pairs)))
	for _, pair := range pairs {
		out = appendHashUvarint(out, uint64(len(pair.Field)))
		out = append(out, pair.Field...)
	}
	return out
}

func hashShapeMatches(signature []byte, pairs []HashPair) bool {
	offset := 0
	count, err := readHashUvarint(signature, &offset)
	if err != nil || count != uint64(len(pairs)) {
		return false
	}
	for _, pair := range pairs {
		fieldLen, err := readHashUvarint(signature, &offset)
		if err != nil || fieldLen > uint64(len(signature)-offset) {
			return false
		}
		end := offset + int(fieldLen)
		if !bytes.Equal(signature[offset:end], pair.Field) {
			return false
		}
		offset = end
	}
	return offset == len(signature)
}

func hashUvarintLen(value uint64) int {
	n := 1
	for value >= 0x80 {
		value >>= 7
		n++
	}
	return n
}

func shapedHashEncodedLen(pairs []HashPair) int {
	length := len(shapedHashHeader) + 2
	for _, pair := range pairs {
		length += hashUvarintLen(uint64(len(pair.Value))) + len(pair.Value)
	}
	return length
}

func encodeShapedHash(shapeID uint16, pairs []HashPair) []byte {
	out := make([]byte, 0, shapedHashEncodedLen(pairs))
	out = append(out, shapedHashHeader[:]...)
	var id [2]byte
	binary.LittleEndian.PutUint16(id[:], shapeID)
	out = append(out, id[:]...)
	for _, pair := range pairs {
		out = appendHashUvarint(out, uint64(len(pair.Value)))
		out = append(out, pair.Value...)
	}
	return out
}

func isShapedHash(data []byte) bool {
	return len(data) >= len(shapedHashHeader)+2 &&
		bytes.Equal(data[:len(shapedHashHeader)], shapedHashHeader[:])
}

// hashShapeID returns an admitted shape for pairs, admitting the shape after
// repeated observations when the physical representation saves enough bytes.
// Fingerprint collisions only disable shape encoding for the colliding shape;
// they can never change logical data.
func (s *Store) hashShapeID(pairs []HashPair, packedLen int) (uint16, bool) {
	if len(pairs) < 2 || packedLen-shapedHashEncodedLen(pairs) < hashShapeMinSavings {
		return 0, false
	}

	fingerprint := hashShapeFingerprint(pairs)
	catalog := &s.hashShapes
	catalog.mu.Lock()
	defer catalog.mu.Unlock()

	if id, ok := catalog.byFingerprint[fingerprint]; ok {
		if id == 0 || int(id) > len(catalog.shapes) {
			panic("invalid HASH shape id")
		}
		if hashShapeMatches(catalog.shapes[id-1].signature, pairs) {
			return id, true
		}
		return 0, false
	}

	candidate := &catalog.candidates[fingerprint%uint64(len(catalog.candidates))]
	if candidate.fingerprint != fingerprint {
		candidate.fingerprint = fingerprint
		candidate.count = 1
		return 0, false
	}
	if candidate.count < ^uint8(0) {
		candidate.count++
	}
	if candidate.count < hashShapeAdmissionThreshold {
		return 0, false
	}

	if len(catalog.shapes) >= hashShapeMaxShapes {
		return 0, false
	}

	signature := hashShapeSignature(pairs)
	// Covers the retained signature plus conservative map/slice bookkeeping.
	charge := uint64(len(signature) + 64)
	if catalog.bytes+charge > hashShapeBudgetBytes {
		return 0, false
	}

	s.memory.mu.Lock()
	if s.memory.max.Load() > 0 && s.memory.used+charge > s.memory.max.Load() {
		s.memory.mu.Unlock()
		return 0, false
	}
	s.memory.used += charge
	s.memory.schemas += charge
	s.memory.mu.Unlock()

	if catalog.byFingerprint == nil {
		catalog.byFingerprint = make(map[uint64]uint16)
	}
	id := uint16(len(catalog.shapes) + 1)
	catalog.shapes = append(catalog.shapes, hashShape{signature: signature})
	catalog.byFingerprint[fingerprint] = id
	catalog.bytes += charge
	return id, true
}

func (s *Store) hashShapeSignatureByID(id uint16) ([]byte, bool) {
	catalog := &s.hashShapes
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if id == 0 || int(id) > len(catalog.shapes) {
		return nil, false
	}
	return catalog.shapes[id-1].signature, true
}

func (s *Store) decodeShapedHash(data []byte, rawLength int) ([]byte, error) {
	if !isShapedHash(data) {
		return nil, errors.New("invalid shaped hash")
	}
	shapeID := binary.LittleEndian.Uint16(data[len(shapedHashHeader) : len(shapedHashHeader)+2])
	signature, ok := s.hashShapeSignatureByID(shapeID)
	if !ok {
		return nil, errors.New("unknown HASH shape")
	}

	sigOffset := 0
	count, err := readHashUvarint(signature, &sigOffset)
	if err != nil {
		return nil, err
	}
	valueOffset := len(shapedHashHeader) + 2
	out := make([]byte, 0, rawLength)
	out = append(out, packedHashHeader[:]...)
	out = appendHashUvarint(out, count)

	for i := uint64(0); i < count; i++ {
		fieldLen, err := readHashUvarint(signature, &sigOffset)
		if err != nil || fieldLen > uint64(len(signature)-sigOffset) {
			return nil, errors.New("invalid HASH shape")
		}
		fieldEnd := sigOffset + int(fieldLen)
		field := signature[sigOffset:fieldEnd]
		sigOffset = fieldEnd

		valueLen, err := readHashUvarint(data, &valueOffset)
		if err != nil || valueLen > uint64(len(data)-valueOffset) {
			return nil, errors.New("invalid shaped hash value")
		}
		valueEnd := valueOffset + int(valueLen)

		out = appendHashUvarint(out, fieldLen)
		out = appendHashUvarint(out, valueLen)
		out = append(out, field...)
		out = append(out, data[valueOffset:valueEnd]...)
		valueOffset = valueEnd
	}

	if sigOffset != len(signature) || valueOffset != len(data) || len(out) != rawLength {
		return nil, errors.New("invalid shaped hash framing")
	}
	return out, nil
}

func (s *Store) dropGlobalHashShapeStoreLocked() {
	catalog := &s.hashShapes
	catalog.mu.Lock()
	charge := catalog.bytes
	catalog.byFingerprint = nil
	catalog.shapes = nil
	catalog.candidates = [256]hashShapeCandidate{}
	catalog.bytes = 0
	catalog.mu.Unlock()

	if charge == 0 {
		return
	}
	s.memory.mu.Lock()
	if s.memory.used >= charge {
		s.memory.used -= charge
	} else {
		s.memory.used = 0
	}
	if s.memory.schemas >= charge {
		s.memory.schemas -= charge
	} else {
		s.memory.schemas = 0
	}
	s.memory.mu.Unlock()
}
