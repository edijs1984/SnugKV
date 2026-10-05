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
var shapedHashFixedHeader = [...]byte{'S', 'H', 4}

type hashShape struct {
	signature []byte
	ordinals  map[string]uint16
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

func shapedHashFixedValueLen(pairs []HashPair) (int, bool) {
	if len(pairs) == 0 {
		return 0, false
	}
	valueLen := len(pairs[0].Value)
	for i := 1; i < len(pairs); i++ {
		if len(pairs[i].Value) != valueLen {
			return 0, false
		}
	}
	return valueLen, true
}

func shapedHashEncodedLen(pairs []HashPair) int {
	if valueLen, ok := shapedHashFixedValueLen(pairs); ok {
		return len(shapedHashFixedHeader) + 2 +
			hashUvarintLen(uint64(valueLen)) + len(pairs)*valueLen
	}
	length := len(shapedHashHeader) + 2
	for _, pair := range pairs {
		length += hashUvarintLen(uint64(len(pair.Value))) + len(pair.Value)
	}
	return length
}

func encodeShapedHash(shapeID uint16, pairs []HashPair) []byte {
	var id [2]byte
	binary.LittleEndian.PutUint16(id[:], shapeID)

	if valueLen, ok := shapedHashFixedValueLen(pairs); ok {
		out := make([]byte, 0, shapedHashEncodedLen(pairs))
		out = append(out, shapedHashFixedHeader[:]...)
		out = append(out, id[:]...)
		out = appendHashUvarint(out, uint64(valueLen))
		for _, pair := range pairs {
			out = append(out, pair.Value...)
		}
		return out
	}

	out := make([]byte, 0, shapedHashEncodedLen(pairs))
	out = append(out, shapedHashHeader[:]...)
	out = append(out, id[:]...)
	for _, pair := range pairs {
		out = appendHashUvarint(out, uint64(len(pair.Value)))
		out = append(out, pair.Value...)
	}
	return out
}

func isFixedShapedHash(data []byte) bool {
	return len(data) >= len(shapedHashFixedHeader)+2 &&
		bytes.Equal(data[:len(shapedHashFixedHeader)], shapedHashFixedHeader[:])
}

func isShapedHash(data []byte) bool {
	return len(data) >= len(shapedHashHeader)+2 &&
		(bytes.Equal(data[:len(shapedHashHeader)], shapedHashHeader[:]) ||
			bytes.Equal(data[:len(shapedHashFixedHeader)], shapedHashFixedHeader[:]))
}

func (s *Store) shapedHashShape(data []byte) (hashShape, error) {
	if !isShapedHash(data) {
		return hashShape{}, errors.New("invalid shaped hash")
	}
	shapeID := binary.LittleEndian.Uint16(data[len(shapedHashHeader) : len(shapedHashHeader)+2])
	shape, ok := s.hashShapeByID(shapeID)
	if !ok {
		return hashShape{}, errors.New("unknown HASH shape")
	}
	return shape, nil
}

func (s *Store) shapedHashSignature(data []byte) ([]byte, error) {
	shape, err := s.shapedHashShape(data)
	if err != nil {
		return nil, err
	}
	return shape.signature, nil
}

// shapedHashLookupViewKnown reads directly from a compact shared-shape value.
// SH4 hashes are fixed-width and therefore O(1) after the shared field ordinal
// lookup. Legacy/variable-width SH2 hashes reuse the same ordinal index and
// only scan value-length prefixes up to the requested value.
func shapedHashLookupViewKnown(data, target []byte, shape hashShape) ([]byte, bool, error) {
	if !isShapedHash(data) {
		return nil, false, errors.New("invalid shaped hash")
	}
	ordinal, found := shape.ordinals[string(target)]
	if !found {
		return nil, false, nil
	}

	valueOffset := len(shapedHashHeader) + 2
	if isFixedShapedHash(data) {
		valueLen, err := readHashUvarint(data, &valueOffset)
		if err != nil {
			return nil, false, errors.New("invalid fixed shaped hash")
		}
		count := len(shape.ordinals)
		if valueLen > uint64(maxPackedHashBytes) ||
			uint64(count) > uint64(maxPackedHashBytes)/(valueLen+1) {
			return nil, false, errors.New("invalid fixed shaped hash")
		}
		expected := valueOffset + count*int(valueLen)
		if expected != len(data) {
			return nil, false, errors.New("invalid fixed shaped hash framing")
		}
		start := valueOffset + int(ordinal)*int(valueLen)
		end := start + int(valueLen)
		return data[start:end], true, nil
	}

	for i := uint16(0); i <= ordinal; i++ {
		valueLen, err := readHashUvarint(data, &valueOffset)
		if err != nil || valueLen > uint64(len(data)-valueOffset) {
			return nil, false, errors.New("invalid shaped hash value")
		}
		valueEnd := valueOffset + int(valueLen)
		if i == ordinal {
			return data[valueOffset:valueEnd], true, nil
		}
		valueOffset = valueEnd
	}
	return nil, false, errors.New("invalid shaped hash ordinal")
}

func (s *Store) shapedHashLookupView(data, target []byte) ([]byte, bool, error) {
	shape, err := s.shapedHashShape(data)
	if err != nil {
		return nil, false, err
	}
	return shapedHashLookupViewKnown(data, target, shape)
}

func (s *Store) shapedHashLookup(data, target []byte) ([]byte, bool, error) {
	value, found, err := s.shapedHashLookupView(data, target)
	if err != nil || !found {
		return nil, found, err
	}
	return append([]byte(nil), value...), true, nil
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
	ordinals := make(map[string]uint16, len(pairs))
	ordinalBytes := 0
	for i, pair := range pairs {
		ordinals[string(pair.Field)] = uint16(i)
		ordinalBytes += len(pair.Field) + 24
	}
	// Covers the retained signature, ordinal lookup, and conservative
	// map/slice bookkeeping. The lookup is retained once per shared shape.
	charge := uint64(len(signature) + ordinalBytes + 64)
	if catalog.bytes+charge > hashShapeBudgetBytes {
		return 0, false
	}

	s.memory.mu.Lock()
	if max := s.memory.max.Load(); max > 0 && s.memory.used+charge > max {
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
	catalog.shapes = append(catalog.shapes, hashShape{
		signature: signature,
		ordinals:  ordinals,
	})
	catalog.byFingerprint[fingerprint] = id
	catalog.bytes += charge
	return id, true
}

func (s *Store) hashShapeByID(id uint16) (hashShape, bool) {
	catalog := &s.hashShapes
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if id == 0 || int(id) > len(catalog.shapes) {
		return hashShape{}, false
	}
	return catalog.shapes[id-1], true
}

func (s *Store) hashShapeSignatureByID(id uint16) ([]byte, bool) {
	shape, ok := s.hashShapeByID(id)
	if !ok {
		return nil, false
	}
	return shape.signature, true
}

func (s *Store) decodeShapedHash(data []byte, rawLength int) ([]byte, error) {
	if !isShapedHash(data) {
		return nil, errors.New("invalid shaped hash")
	}
	shape, err := s.shapedHashShape(data)
	if err != nil {
		return nil, err
	}
	signature := shape.signature

	sigOffset := 0
	count, err := readHashUvarint(signature, &sigOffset)
	if err != nil {
		return nil, err
	}
	valueOffset := len(shapedHashHeader) + 2
	fixedValueLen := uint64(0)
	if isFixedShapedHash(data) {
		fixedValueLen, err = readHashUvarint(data, &valueOffset)
		if err != nil {
			return nil, errors.New("invalid fixed shaped hash")
		}
		if int(count) != len(shape.ordinals) ||
			fixedValueLen > uint64(maxPackedHashBytes) ||
			uint64(count) > uint64(maxPackedHashBytes)/(fixedValueLen+1) ||
			valueOffset+int(count)*int(fixedValueLen) != len(data) {
			return nil, errors.New("invalid fixed shaped hash framing")
		}
	}

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

		valueLen := fixedValueLen
		if !isFixedShapedHash(data) {
			valueLen, err = readHashUvarint(data, &valueOffset)
			if err != nil || valueLen > uint64(len(data)-valueOffset) {
				return nil, errors.New("invalid shaped hash value")
			}
		}
		if valueLen > uint64(len(data)-valueOffset) {
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
