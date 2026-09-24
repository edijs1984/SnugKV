package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"
)

const maxPackedHashBytes = 32 << 20

var packedHashHeader = [...]byte{'S', 'H', 1}
var packedHashExpiryHeader = [...]byte{'S', 'H', 3}

// HashPair is one binary-safe HASH field/value pair.
// Packed hashes are stored sorted by Field so reads need no retained Go map.
type HashPair struct {
	Field       []byte
	Value       []byte
	ExpiresAtMS int64
}

// HashStats exposes storage measurements for datatype benchmarks.
type HashStats struct {
	Fields      int
	FieldBytes  int
	ValueBytes  int
	PackedBytes int
	StoredBytes int
	Encoding    string
}

func hashWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func appendHashUvarint(dst []byte, value uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	return append(dst, buf[:n]...)
}

func readHashUvarint(data []byte, offset *int) (uint64, error) {
	if *offset >= len(data) {
		return 0, errors.New("invalid packed hash")
	}

	value, n := binary.Uvarint(data[*offset:])
	if n <= 0 {
		return 0, errors.New("invalid packed hash")
	}

	*offset += n
	return value, nil
}

func packedHashHasFieldExpiry(data []byte) bool {
	return len(data) >= len(packedHashExpiryHeader) &&
		bytes.Equal(data[:len(packedHashExpiryHeader)], packedHashExpiryHeader[:])
}

func packedHashCount(data []byte) (int, error) {
	if len(data) < len(packedHashHeader) ||
		(!bytes.Equal(data[:len(packedHashHeader)], packedHashHeader[:]) && !packedHashHasFieldExpiry(data)) {
		return 0, errors.New("invalid packed hash")
	}

	offset := len(packedHashHeader)
	count, err := readHashUvarint(data, &offset)
	if err != nil || count > uint64(maxPackedHashBytes) {
		return 0, errors.New("invalid packed hash")
	}

	return int(count), nil
}

func encodePackedHash(input []HashPair) ([]byte, error) {
	pairs := make([]HashPair, len(input))
	hasFieldExpiry := false
	for i := range input {
		pairs[i] = HashPair{
			Field:       append([]byte(nil), input[i].Field...),
			Value:       append([]byte(nil), input[i].Value...),
			ExpiresAtMS: input[i].ExpiresAtMS,
		}
		if input[i].ExpiresAtMS != 0 {
			hasFieldExpiry = true
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		return bytes.Compare(pairs[i].Field, pairs[j].Field) < 0
	})

	capacity := len(packedHashHeader) + binary.MaxVarintLen64
	for i := range pairs {
		if i > 0 && bytes.Equal(pairs[i-1].Field, pairs[i].Field) {
			return nil, errors.New("duplicate hash field")
		}
		capacity += len(pairs[i].Field) + len(pairs[i].Value) + 2*binary.MaxVarintLen64
		if hasFieldExpiry {
			capacity += 8
		}
		if capacity > maxPackedHashBytes {
			return nil, errors.New("ERR hash exceeds 32 MiB limit")
		}
	}

	out := make([]byte, 0, capacity)
	if hasFieldExpiry {
		out = append(out, packedHashExpiryHeader[:]...)
	} else {
		out = append(out, packedHashHeader[:]...)
	}
	out = appendHashUvarint(out, uint64(len(pairs)))

	for _, pair := range pairs {
		out = appendHashUvarint(out, uint64(len(pair.Field)))
		out = appendHashUvarint(out, uint64(len(pair.Value)))
		if hasFieldExpiry {
			var expiry [8]byte
			binary.LittleEndian.PutUint64(expiry[:], uint64(pair.ExpiresAtMS))
			out = append(out, expiry[:]...)
		}
		out = append(out, pair.Field...)
		out = append(out, pair.Value...)
	}

	if len(out) > maxPackedHashBytes {
		return nil, errors.New("ERR hash exceeds 32 MiB limit")
	}

	return out, nil
}

func decodePackedHash(data []byte) ([]HashPair, error) {
	count, err := packedHashCount(data)
	if err != nil {
		return nil, err
	}

	offset := len(packedHashHeader)
	if _, err := readHashUvarint(data, &offset); err != nil {
		return nil, err
	}

	hasFieldExpiry := packedHashHasFieldExpiry(data)
	pairs := make([]HashPair, 0, count)
	for i := 0; i < count; i++ {
		fieldLen, err := readHashUvarint(data, &offset)
		if err != nil {
			return nil, err
		}
		valueLen, err := readHashUvarint(data, &offset)
		if err != nil {
			return nil, err
		}

		var expiresAtMS int64
		if hasFieldExpiry {
			if len(data)-offset < 8 {
				return nil, errors.New("invalid packed hash")
			}
			expiresAtMS = int64(binary.LittleEndian.Uint64(data[offset : offset+8]))
			offset += 8
		}

		if fieldLen > uint64(len(data)-offset) {
			return nil, errors.New("invalid packed hash")
		}
		fieldEnd := offset + int(fieldLen)
		field := append([]byte(nil), data[offset:fieldEnd]...)
		offset = fieldEnd

		if valueLen > uint64(len(data)-offset) {
			return nil, errors.New("invalid packed hash")
		}
		valueEnd := offset + int(valueLen)
		value := append([]byte(nil), data[offset:valueEnd]...)
		offset = valueEnd

		if len(pairs) > 0 && bytes.Compare(pairs[len(pairs)-1].Field, field) >= 0 {
			return nil, errors.New("invalid packed hash order")
		}

		pairs = append(pairs, HashPair{Field: field, Value: value, ExpiresAtMS: expiresAtMS})
	}

	if offset != len(data) {
		return nil, errors.New("invalid packed hash trailing data")
	}

	return pairs, nil
}

func packedHashLookup(data, target []byte, nowMS int64) ([]byte, bool, error) {
	count, err := packedHashCount(data)
	if err != nil {
		return nil, false, err
	}

	offset := len(packedHashHeader)
	if _, err := readHashUvarint(data, &offset); err != nil {
		return nil, false, err
	}

	hasFieldExpiry := packedHashHasFieldExpiry(data)
	for i := 0; i < count; i++ {
		fieldLen, err := readHashUvarint(data, &offset)
		if err != nil {
			return nil, false, err
		}
		valueLen, err := readHashUvarint(data, &offset)
		if err != nil {
			return nil, false, err
		}

		var expiresAtMS int64
		if hasFieldExpiry {
			if len(data)-offset < 8 {
				return nil, false, errors.New("invalid packed hash")
			}
			expiresAtMS = int64(binary.LittleEndian.Uint64(data[offset : offset+8]))
			offset += 8
		}

		if fieldLen > uint64(len(data)-offset) {
			return nil, false, errors.New("invalid packed hash")
		}
		fieldEnd := offset + int(fieldLen)
		field := data[offset:fieldEnd]
		offset = fieldEnd

		if valueLen > uint64(len(data)-offset) {
			return nil, false, errors.New("invalid packed hash")
		}
		valueEnd := offset + int(valueLen)

		cmp := bytes.Compare(field, target)
		if cmp == 0 {
			if expiresAtMS != 0 && expiresAtMS <= nowMS {
				return nil, false, nil
			}
			return append([]byte(nil), data[offset:valueEnd]...), true, nil
		}
		if cmp > 0 {
			return nil, false, nil
		}

		offset = valueEnd
	}

	return nil, false, nil
}

func (s *Store) hashEntry(pairs []HashPair, packed []byte) preparedEntry {
	stored := packed
	hasFieldExpiry := false
	for _, pair := range pairs {
		if pair.ExpiresAtMS != 0 {
			hasFieldExpiry = true
			break
		}
	}
	if !hasFieldExpiry {
		if shapeID, ok := s.hashShapeID(pairs, len(packed)); ok {
		shaped := encodeShapedHash(shapeID, pairs)
		if len(shaped)+hashShapeMinSavings <= len(packed) {
			stored = shaped
		}
	}
	}
	return preparedEntry{
		entry: entry{entryData: entryData{
			valueType: TypeHash,
			rawLength: uint32(len(packed)),
		}},
		data: append([]byte(nil), stored...),
	}
}

// HashSet updates one or more field/value pairs and returns the number of newly
// inserted fields. The logical representation remains canonical packed HASH;
// repeated field layouts may use a smaller shared-shape physical representation.
func (s *Store) HashSet(key string, fields, values [][]byte) (int64, error) {
	if len(fields) == 0 || len(fields) != len(values) {
		return 0, errors.New("ERR invalid hash field/value count")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	old, exists := sh.get(key)
	if exists && sh.expired(key, old, now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}

	var pairs []HashPair
	var expiresAt stamp
	if exists {
		if old.valueType != TypeHash {
			return 0, hashWrongType()
		}
		var err error
		pairs, err = decodePackedHash(s.decode(sh, old))
		if err != nil {
			return 0, err
		}
		expiresAt = sh.expirationAt(key, old)
	}

	var added int64
	for i, field := range fields {
		index := sort.Search(len(pairs), func(j int) bool {
			return bytes.Compare(pairs[j].Field, field) >= 0
		})

		value := append([]byte(nil), values[i]...)
		if index < len(pairs) && bytes.Equal(pairs[index].Field, field) {
			pairs[index].Value = value
			continue
		}

		pair := HashPair{
			Field: append([]byte(nil), field...),
			Value: value,
		}
		pairs = append(pairs, HashPair{})
		copy(pairs[index+1:], pairs[index:])
		pairs[index] = pair
		added++
	}

	packed, err := encodePackedHash(pairs)
	if err != nil {
		return 0, err
	}

	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}

	return added, nil
}

func (s *Store) HashGet(key string, field []byte) ([]byte, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, false, nil
	}
	if e.valueType != TypeHash {
		return nil, false, hashWrongType()
	}

	return packedHashLookup(s.decode(sh, e), field, s.now().UnixMilli())
}

func (s *Store) HashLen(key string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, nil
	}
	if e.valueType != TypeHash {
		return 0, hashWrongType()
	}

	count, err := packedHashCount(s.decode(sh, e))
	return int64(count), err
}

func (s *Store) HashGetAll(key string) ([]HashPair, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, nil
	}
	if e.valueType != TypeHash {
		return nil, hashWrongType()
	}

	return decodePackedHash(s.decode(sh, e))
}

func (s *Store) HashDel(key string, fields [][]byte) (int64, error) {
	if len(fields) == 0 {
		return 0, nil
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return 0, nil
	}
	if e.valueType != TypeHash {
		return 0, hashWrongType()
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return 0, err
	}

	remove := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		remove[string(field)] = struct{}{}
	}

	kept := pairs[:0]
	var deleted int64
	for _, pair := range pairs {
		if _, found := remove[string(pair.Field)]; found {
			deleted++
			continue
		}
		kept = append(kept, pair)
	}

	if deleted == 0 {
		return 0, nil
	}
	if len(kept) == 0 {
		s.remove(sh, key)
		return deleted, nil
	}

	packed, err := encodePackedHash(kept)
	if err != nil {
		return 0, err
	}
	updated := s.hashEntry(kept, packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}

	return deleted, nil
}

func (s *Store) HashStorageStats(key string) (HashStats, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return HashStats{}, false, nil
	}
	if e.valueType != TypeHash {
		return HashStats{}, false, hashWrongType()
	}

	physical := sh.encoded(e)
	packed := s.decode(sh, e)
	pairs, err := decodePackedHash(packed)
	if err != nil {
		return HashStats{}, false, err
	}

	encoding := "packed"
	if isShapedHash(physical) {
		encoding = "shape"
	}
	stats := HashStats{
		Fields:      len(pairs),
		PackedBytes: len(packed),
		StoredBytes: len(physical),
		Encoding:    encoding,
	}
	for _, pair := range pairs {
		stats.FieldBytes += len(pair.Field)
		stats.ValueBytes += len(pair.Value)
	}

	return stats, true, nil
}
