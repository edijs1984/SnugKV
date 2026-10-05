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
	if isIndexedHash(data) {
		count, _, _, _, err := indexedHashMeta(data)
		return count, err
	}
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
	if isIndexedHash(data) {
		return decodeIndexedHash(data)
	}
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

func packedHashLookupView(data, target []byte, nowMS int64) ([]byte, bool, error) {
	if isIndexedHash(data) {
		return indexedHashLookup(data, target)
	}
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
			return data[offset:valueEnd], true, nil
		}
		if cmp > 0 {
			return nil, false, nil
		}

		offset = valueEnd
	}

	return nil, false, nil
}


func packedHashLookup(data, target []byte, nowMS int64) ([]byte, bool, error) {
	value, found, err := packedHashLookupView(data, target, nowMS)
	if err != nil || !found {
		return nil, found, err
	}
	return append([]byte(nil), value...), true, nil
}

func (s *Store) hashEntryFromPairs(pairs []HashPair) (preparedEntry, error) {
	packedLen, ok := canonicalPackedHashLen(pairs)
	if !ok {
		return preparedEntry{}, errors.New("ERR hash exceeds 32 MiB limit")
	}
	for _, pair := range pairs {
		if pair.ExpiresAtMS != 0 {
			packed, err := encodePackedHash(pairs)
			if err != nil {
				return preparedEntry{}, err
			}
			return s.hashEntry(pairs, packed), nil
		}
	}
	if shapeID, ok := s.hashShapeID(pairs, packedLen); ok {
		shaped := encodeShapedHash(shapeID, pairs)
		if len(shaped)+hashShapeMinSavings <= packedLen {
			return preparedEntry{
				entry: entry{entryData: entryData{
					valueType: TypeHash,
					rawLength: uint32(packedLen),
				}},
				data: shaped,
			}, nil
		}
	}
	packed, err := encodePackedHash(pairs)
	if err != nil {
		return preparedEntry{}, err
	}
	return s.hashEntry(pairs, packed), nil
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
func (s *Store) hashSetLocked(sh *shard, key string, fields, values [][]byte) (int64, error) {
	now := s.now()
	old, exists := sh.get(key)
	if exists && sh.expired(key, old, now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}

	if exists {
		if old.valueType != TypeHash {
			return 0, hashWrongType()
		}
		if old.isHotHash() {
			h, _, ok := sh.hotHashForKey(key)
			if !ok || h == nil {
				return 0, errors.New("HOT hash sidecar invariant")
			}
			before := h.memoryBytes()
			var added int64
			for i, field := range fields {
				if h.set(field, values[i]) {
					added++
				}
			}
			h.lastMutation = s.now().UnixMilli()
			s.accountHotHashResize(before, h.memoryBytes())
			return added, nil
		}
	}

	var pairs []HashPair
	var expiresAt stamp
	if exists {
		expiresAt = sh.expirationAt(key, old)
		physical := sh.encoded(old)
		if isIndexedHash(physical) {
			added, rebuilt, err := indexedHashSet(physical, fields, values)
			if err != nil {
				return 0, err
			}
			if rebuilt == nil {
				return added, nil
			}
			updated := preparedEntry{
				entry: entry{entryData: entryData{
					valueType: TypeHash,
					rawLength: uint32(len(rebuilt)),
				}},
				data:      rebuilt,
				expiresAt: expiresAt,
			}
			if err := s.publish(sh, key, updated); err != nil {
				return 0, err
			}
			return added, nil
		}
		var err error
		if isShapedHash(physical) {
			pairs, err = s.shapedHashPairsView(physical)
		} else {
			pairs, err = decodePackedHash(s.decode(sh, old))
			if err == nil {
				pairs = liveHashPairs(pairs, now.UnixMilli())
			}
		}
		if err != nil {
			return 0, err
		}
	}

	var added int64
	for i, field := range fields {
		index := sort.Search(len(pairs), func(j int) bool {
			return bytes.Compare(pairs[j].Field, field) >= 0
		})

		value := append([]byte(nil), values[i]...)
		if index < len(pairs) && bytes.Equal(pairs[index].Field, field) {
			pairs[index].Value = value
			pairs[index].ExpiresAtMS = 0
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

	var updated preparedEntry
	indexThreshold := indexedHashPromoteFields
	if len(fields) == 1 {
		indexThreshold = 16
	}
	canIndex := len(pairs) >= indexThreshold
	if canIndex {
		for _, pair := range pairs {
			if pair.ExpiresAtMS != 0 {
				canIndex = false
				break
			}
		}
	}
	if canIndex {
		indexed, err := encodeIndexedHash(pairs)
		if err != nil {
			return 0, err
		}
		updated = preparedEntry{
			entry: entry{entryData: entryData{
				valueType: TypeHash,
				rawLength: uint32(len(indexed)),
			}},
			data: indexed,
		}
	} else {
		var err error
		updated, err = s.hashEntryFromPairs(pairs)
		if err != nil {
			return 0, err
		}
	}
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return added, nil
}
func (s *Store) HashSet(key string, fields, values [][]byte) (int64, error) {
	if len(fields) == 0 || len(fields) != len(values) {
		return 0, errors.New("ERR invalid hash field/value count")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return s.hashSetLocked(sh, key, fields, values)
}

// HashSetResults applies a sequence of single-field HSET commands for one key
// under a single shard lock and returns one Redis HSET result per command.
// The first insertion of a previously absent field returns 1; overwrites and
// repeated fields later in the same batch return 0.
func (s *Store) HashSetResults(key string, fields, values [][]byte) ([]int64, error) {
	if len(fields) == 0 || len(fields) != len(values) {
		return nil, errors.New("ERR invalid hash field/value count")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	results := make([]int64, len(fields))
	now := s.now()
	old, exists := sh.get(key)
	if exists && sh.expired(key, old, now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}
	if exists && old.valueType != TypeHash {
		return nil, hashWrongType()
	}

	if exists {
		if h, hot, err := s.thawHotHashLocked(sh, key, old); err != nil {
			return nil, err
		} else if hot {
			before := h.memoryBytes()
			for i, field := range fields {
				if _, found := h.get(field); !found {
					results[i] = 1
				}
				h.set(field, values[i])
			}
			h.lastMutation = s.now().UnixMilli()
			s.accountHotHashResize(before, h.memoryBytes())
			return results, nil
		}
	}

	existing := make(map[string]struct{})
	if exists {
		physical := sh.encoded(old)
		if isIndexedHash(physical) {
			for _, field := range fields {
				name := string(field)
				if _, seen := existing[name]; seen {
					continue
				}
				_, _, found, err := indexedHashFind(physical, field)
				if err != nil {
					return nil, err
				}
				if found {
					existing[name] = struct{}{}
				}
			}
		} else if isShapedHash(physical) {
			pairs, err := s.shapedHashPairsView(physical)
			if err != nil {
				return nil, err
			}
			for _, pair := range pairs {
				existing[string(pair.Field)] = struct{}{}
			}
		} else {
			pairs, err := decodePackedHash(physical)
			if err != nil {
				return nil, err
			}
			for _, pair := range liveHashPairs(pairs, now.UnixMilli()) {
				existing[string(pair.Field)] = struct{}{}
			}
		}
	}

	seen := make(map[string]struct{}, len(fields))
	for i, field := range fields {
		name := string(field)
		if _, ok := existing[name]; ok {
			seen[name] = struct{}{}
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		results[i] = 1
	}

	if _, err := s.hashSetLocked(sh, key, fields, values); err != nil {
		return nil, err
	}
	if current, ok := sh.get(key); ok && !current.isHotHash() {
		shouldPromote := false
		if isIndexedHash(sh.encoded(current)) {
			if count, _, _, _, err := indexedHashMeta(sh.encoded(current)); err == nil {
				shouldPromote = count >= hotHashPromoteFields
			}
		} else if isShapedHash(sh.encoded(current)) {
			pairs, err := s.shapedHashPairsView(sh.encoded(current))
			if err != nil {
				return nil, err
			}
			shouldPromote = len(pairs) >= hotHashPromoteFields
		} else {
			pairs, err := decodePackedHash(sh.encoded(current))
			if err != nil {
				return nil, err
			}
			shouldPromote = len(liveHashPairs(pairs, now.UnixMilli())) >= hotHashPromoteFields
		}
		if shouldPromote {
			if _, _, err := s.thawHotHashLocked(sh, key, current); err != nil {
				return nil, err
			}
		}
	}
	return results, nil
}
func (s *Store) hashLogicalValue(sh *shard, e entry) ([]byte, error) {
	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	return encodePackedHash(pairs)
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
	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return nil, false, errors.New("HOT hash sidecar invariant")
		}
		value, found := h.get(field)
		if !found {
			return nil, false, nil
		}
		return append([]byte(nil), value...), true, nil
	}

	physical := sh.encoded(e)
	if isIndexedHash(physical) {
		return indexedHashLookup(physical, field)
	}
	if isShapedHash(physical) {
		value, found, err := s.shapedHashLookupView(physical, field)
		if err != nil || !found {
			return nil, found, err
		}
		return append([]byte(nil), value...), true, nil
	}
	return packedHashLookup(physical, field, s.now().UnixMilli())
}
// HashGetResult describes one field value copied into a caller-owned batch buffer.
type HashGetResult struct {
	Offset uint32
	Length uint32
	Found  bool
}

func (s *Store) HashGetResultsInto(
	key string,
	fields [][]byte,
	dst []byte,
	results []HashGetResult,
) ([]byte, error) {
	if len(results) < len(fields) {
		return dst, errors.New("ERR insufficient hash result scratch")
	}
	if len(fields) == 0 {
		return dst, nil
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		for i := range fields {
			results[i] = HashGetResult{}
		}
		return dst, nil
	}
	if e.valueType != TypeHash {
		return dst, hashWrongType()
	}

	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return dst, errors.New("HOT hash sidecar invariant")
		}
		for i, field := range fields {
			value, found := h.get(field)
			if !found {
				results[i] = HashGetResult{}
				continue
			}
			offset := len(dst)
			dst = append(dst, value...)
			results[i] = HashGetResult{
				Offset: uint32(offset),
				Length: uint32(len(value)),
				Found:  true,
			}
		}
		return dst, nil
	}

	physical := sh.encoded(e)
	nowMS := s.now().UnixMilli()
	if isShapedHash(physical) {
		for i, field := range fields {
			value, found, lookupErr := s.shapedHashLookupView(physical, field)
			if lookupErr != nil {
				return dst, lookupErr
			}
			if !found {
				results[i] = HashGetResult{}
				continue
			}
			offset := len(dst)
			dst = append(dst, value...)
			results[i] = HashGetResult{
				Offset: uint32(offset),
				Length: uint32(len(value)),
				Found:  true,
			}
		}
		return dst, nil
	}

	if isIndexedHash(physical) {
		_, slots, used, start, err := indexedHashMeta(physical)
		if err != nil {
			return dst, err
		}
		for i, field := range fields {
			value, found, lookupErr := indexedHashLookupViewKnown(
				physical, field, slots, used, start,
			)
			if lookupErr != nil {
				return dst, lookupErr
			}
			if !found {
				results[i] = HashGetResult{}
				continue
			}
			offset := len(dst)
			dst = append(dst, value...)
			results[i] = HashGetResult{
				Offset: uint32(offset),
				Length: uint32(len(value)),
				Found:  true,
			}
		}
		return dst, nil
	}

	decoded := s.decode(sh, e)
	for i, field := range fields {
		value, found, lookupErr := packedHashLookupView(decoded, field, nowMS)
		if lookupErr != nil {
			return dst, lookupErr
		}
		if !found {
			results[i] = HashGetResult{}
			continue
		}
		offset := len(dst)
		dst = append(dst, value...)
		results[i] = HashGetResult{
			Offset: uint32(offset),
			Length: uint32(len(value)),
			Found:  true,
		}
	}
	return dst, nil
}
func (s *Store) HashGetResults(key string, fields [][]byte) (values [][]byte, found []bool, err error) {
	values = make([][]byte, len(fields))
	found = make([]bool, len(fields))
	if len(fields) == 0 {
		return values, found, nil
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return values, found, nil
	}
	if e.valueType != TypeHash {
		return nil, nil, hashWrongType()
	}
	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return nil, nil, errors.New("HOT hash sidecar invariant")
		}
		for i, field := range fields {
			if value, ok := h.get(field); ok {
				values[i] = append([]byte(nil), value...)
				found[i] = true
			}
		}
		return values, found, nil
	}

	physical := sh.encoded(e)
	if isShapedHash(physical) {
		for i, field := range fields {
			value, ok, lookupErr := s.shapedHashLookupView(physical, field)
			if lookupErr != nil {
				return nil, nil, lookupErr
			}
			if ok {
				values[i] = append([]byte(nil), value...)
				found[i] = true
			}
		}
		return values, found, nil
	}
	if isIndexedHash(physical) {
		_, slots, used, start, metaErr := indexedHashMeta(physical)
		if metaErr != nil {
			return nil, nil, metaErr
		}
		for i, field := range fields {
			value, ok, lookupErr := indexedHashLookupKnown(
				physical,
				field,
				slots,
				used,
				start,
			)
			if lookupErr != nil {
				return nil, nil, lookupErr
			}
			values[i] = value
			found[i] = ok
		}
		return values, found, nil
	}

	nowMS := s.now().UnixMilli()
	decoded := s.decode(sh, e)
	for i, field := range fields {
		value, ok, lookupErr := packedHashLookup(decoded, field, nowMS)
		if lookupErr != nil {
			return nil, nil, lookupErr
		}
		values[i] = value
		found[i] = ok
	}
	return values, found, nil
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
	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return 0, errors.New("HOT hash sidecar invariant")
		}
		return int64(len(h.records)), nil
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return 0, err
	}
	pairs = liveHashPairs(pairs, s.now().UnixMilli())
	return int64(len(pairs)), nil
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
	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return nil, errors.New("HOT hash sidecar invariant")
		}
		return h.pairs(), nil
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	return liveHashPairs(pairs, s.now().UnixMilli()), nil
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

	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return 0, errors.New("HOT hash sidecar invariant")
		}
		before := h.memoryBytes()
		deleted := h.delete(fields)
		s.accountHotHashResize(before, h.memoryBytes())
		if deleted != 0 && len(h.records) == 0 {
			s.remove(sh, key)
		}
		return deleted, nil
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return 0, err
	}
	pairs = liveHashPairs(pairs, s.now().UnixMilli())

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
	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return HashStats{}, false, errors.New("HOT hash sidecar invariant")
		}
		pairs := h.pairs()
		packed, err := encodePackedHash(pairs)
		if err != nil {
			return HashStats{}, false, err
		}
		stats := HashStats{
			Fields:      len(pairs),
			PackedBytes: len(packed),
			StoredBytes: int(h.memoryBytes()),
			Encoding:    "hot",
		}
		for _, pair := range pairs {
			stats.FieldBytes += len(pair.Field)
			stats.ValueBytes += len(pair.Value)
		}
		return stats, true, nil
	}

	physical := sh.encoded(e)
	packed := s.decode(sh, e)
	pairs, err := decodePackedHash(packed)
	if err != nil {
		return HashStats{}, false, err
	}
	pairs = liveHashPairs(pairs, s.now().UnixMilli())

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
