package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"snugkv/internal/index"
	"sort"
)

const maxPackedSetBytes = 32 << 20

var packedSetHeader = [...]byte{'S', 'S', 1}
var tinySetHeader = [...]byte{'S', 'T', 1}

// SetStats exposes storage measurements for datatype benchmarks.
type SetStats struct {
	Members     int
	MemberBytes int
	PackedBytes int
	StoredBytes int
	Encoding    string
}

func setWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func appendSetUvarint(dst []byte, value uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	return append(dst, buf[:n]...)
}

func readSetUvarint(data []byte, offset *int) (uint64, error) {
	if *offset >= len(data) {
		return 0, errors.New("invalid packed set")
	}
	value, n := binary.Uvarint(data[*offset:])
	if n <= 0 {
		return 0, errors.New("invalid packed set")
	}
	*offset += n
	return value, nil
}

func packedSetCount(data []byte) (int, error) {
	if len(data) < len(packedSetHeader) || !bytes.Equal(data[:len(packedSetHeader)], packedSetHeader[:]) {
		return 0, errors.New("invalid packed set")
	}
	offset := len(packedSetHeader)
	count, err := readSetUvarint(data, &offset)
	if err != nil || count > uint64(maxPackedSetBytes) {
		return 0, errors.New("invalid packed set")
	}
	return int(count), nil
}

func encodePackedSetSorted(members [][]byte) ([]byte, error) {
	capacity := len(packedSetHeader) + binary.MaxVarintLen64
	for i := range members {
		if i > 0 && bytes.Compare(members[i-1], members[i]) >= 0 {
			return nil, errors.New("duplicate or unsorted set member")
		}
		capacity += len(members[i]) + binary.MaxVarintLen64
		if capacity > maxPackedSetBytes {
			return nil, errors.New("ERR set exceeds 32 MiB limit")
		}
	}

	out := make([]byte, 0, capacity)
	out = append(out, packedSetHeader[:]...)
	out = appendSetUvarint(out, uint64(len(members)))
	for _, member := range members {
		out = appendSetUvarint(out, uint64(len(member)))
		out = append(out, member...)
	}
	if len(out) > maxPackedSetBytes {
		return nil, errors.New("ERR set exceeds 32 MiB limit")
	}
	return out, nil
}

func encodePackedSet(input [][]byte) ([]byte, error) {
	members := make([][]byte, len(input))
	for i := range input {
		members[i] = append([]byte(nil), input[i]...)
	}
	sort.Slice(members, func(i, j int) bool { return bytes.Compare(members[i], members[j]) < 0 })

	capacity := len(packedSetHeader) + binary.MaxVarintLen64
	for i := range members {
		if i > 0 && bytes.Equal(members[i-1], members[i]) {
			return nil, errors.New("duplicate set member")
		}
		capacity += len(members[i]) + binary.MaxVarintLen64
		if capacity > maxPackedSetBytes {
			return nil, errors.New("ERR set exceeds 32 MiB limit")
		}
	}

	out := make([]byte, 0, capacity)
	out = append(out, packedSetHeader[:]...)
	out = appendSetUvarint(out, uint64(len(members)))
	for _, member := range members {
		out = appendSetUvarint(out, uint64(len(member)))
		out = append(out, member...)
	}
	if len(out) > maxPackedSetBytes {
		return nil, errors.New("ERR set exceeds 32 MiB limit")
	}
	return out, nil
}

func decodePackedSet(data []byte) ([][]byte, error) {
	count, err := packedSetCount(data)
	if err != nil {
		return nil, err
	}
	offset := len(packedSetHeader)
	if _, err := readSetUvarint(data, &offset); err != nil {
		return nil, err
	}
	members := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		memberLen, err := readSetUvarint(data, &offset)
		if err != nil || memberLen > uint64(len(data)-offset) {
			return nil, errors.New("invalid packed set")
		}
		end := offset + int(memberLen)
		member := append([]byte(nil), data[offset:end]...)
		if len(members) > 0 && bytes.Compare(members[len(members)-1], member) >= 0 {
			return nil, errors.New("invalid packed set order")
		}
		members = append(members, member)
		offset = end
	}
	if offset != len(data) {
		return nil, errors.New("invalid packed set trailing data")
	}
	return members, nil
}

func commonSetPrefix(a, b []byte) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}

// encodeTinyFixedSet front-codes sorted fixed-width members. The width is stored
// once and each member after the first stores only its common-prefix length and
// suffix. This is especially effective for IDs and names with shared prefixes.
func encodeTinyFixedSet(members [][]byte) ([]byte, bool) {
	if len(members) < 2 {
		return nil, false
	}
	width := len(members[0])
	for i := 1; i < len(members); i++ {
		if len(members[i]) != width {
			return nil, false
		}
	}

	out := make([]byte, 0, len(packedSetHeader)+2*binary.MaxVarintLen64+len(members)*width)
	out = append(out, tinySetHeader[:]...)
	out = appendSetUvarint(out, uint64(len(members)))
	out = appendSetUvarint(out, uint64(width))
	out = append(out, members[0]...)

	previous := members[0]
	for i := 1; i < len(members); i++ {
		prefix := commonSetPrefix(previous, members[i])
		out = appendSetUvarint(out, uint64(prefix))
		out = append(out, members[i][prefix:]...)
		previous = members[i]
	}
	return out, true
}

func decodeTinyFixedSet(data []byte) ([][]byte, error) {
	if len(data) < len(tinySetHeader) || !bytes.Equal(data[:len(tinySetHeader)], tinySetHeader[:]) {
		return nil, errors.New("invalid tiny set")
	}
	offset := len(tinySetHeader)
	count64, err := readSetUvarint(data, &offset)
	if err != nil || count64 < 2 || count64 > uint64(maxPackedSetBytes) {
		return nil, errors.New("invalid tiny set")
	}
	width64, err := readSetUvarint(data, &offset)
	if err != nil || width64 > uint64(maxPackedSetBytes) {
		return nil, errors.New("invalid tiny set")
	}
	count, width := int(count64), int(width64)
	if width > len(data)-offset {
		return nil, errors.New("invalid tiny set")
	}

	members := make([][]byte, 0, count)
	first := append([]byte(nil), data[offset:offset+width]...)
	members = append(members, first)
	offset += width
	previous := first

	for i := 1; i < count; i++ {
		prefix64, err := readSetUvarint(data, &offset)
		if err != nil || prefix64 > uint64(width) {
			return nil, errors.New("invalid tiny set")
		}
		prefix := int(prefix64)
		suffixLen := width - prefix
		if suffixLen > len(data)-offset {
			return nil, errors.New("invalid tiny set")
		}
		member := make([]byte, width)
		copy(member, previous[:prefix])
		copy(member[prefix:], data[offset:offset+suffixLen])
		offset += suffixLen
		if bytes.Compare(previous, member) >= 0 {
			return nil, errors.New("invalid tiny set order")
		}
		members = append(members, member)
		previous = member
	}
	if offset != len(data) {
		return nil, errors.New("invalid tiny set trailing data")
	}
	return members, nil
}

func packedSetContains(data, target []byte) (bool, error) {
	count, err := packedSetCount(data)
	if err != nil {
		return false, err
	}
	offset := len(packedSetHeader)
	if _, err := readSetUvarint(data, &offset); err != nil {
		return false, err
	}
	for i := 0; i < count; i++ {
		memberLen, err := readSetUvarint(data, &offset)
		if err != nil || memberLen > uint64(len(data)-offset) {
			return false, errors.New("invalid packed set")
		}
		end := offset + int(memberLen)
		cmp := bytes.Compare(data[offset:end], target)
		if cmp == 0 {
			return true, nil
		}
		if cmp > 0 {
			return false, nil
		}
		offset = end
	}
	if offset != len(data) {
		return false, errors.New("invalid packed set trailing data")
	}
	return false, nil
}

func tinyFixedSetAddOne(data, target []byte) (updated []byte, added bool, count int, width int, err error) {
	if len(data) < len(tinySetHeader) || !bytes.Equal(data[:len(tinySetHeader)], tinySetHeader[:]) {
		return nil, false, 0, 0, errors.New("invalid tiny set")
	}
	offset := len(tinySetHeader)
	count64, err := readSetUvarint(data, &offset)
	if err != nil || count64 < 2 || count64 > uint64(maxPackedSetBytes) {
		return nil, false, 0, 0, errors.New("invalid tiny set")
	}
	width64, err := readSetUvarint(data, &offset)
	if err != nil || width64 > uint64(maxPackedSetBytes) {
		return nil, false, 0, 0, errors.New("invalid tiny set")
	}
	count, width = int(count64), int(width64)
	if len(target) != width {
		return nil, false, count, width, nil
	}
	if width > len(data)-offset {
		return nil, false, 0, 0, errors.New("invalid tiny set")
	}

	// Re-adding an existing member is a no-op. Detect it with the allocation-free
	// lookup before building the rewritten set.
	if present, err := tinyFixedSetContains(data, target); err != nil {
		return nil, false, 0, 0, err
	} else if present {
		return nil, false, count, width, nil
	}

	// Decode one member at a time into a reusable buffer. We only retain the
	// insertion neighborhood, so hot SADD avoids constructing [][]byte for the
	// complete tiny set.
	current := make([]byte, width)
	copy(current, data[offset:offset+width])
	offset += width

	members := make([]byte, 0, (count+1)*width)
	inserted := false
	appendMember := func(member []byte) {
		if !inserted && bytes.Compare(target, member) < 0 {
			members = append(members, target...)
			inserted = true
		}
		members = append(members, member...)
	}

	cmp := bytes.Compare(current, target)
	if cmp == 0 {
		return nil, false, count, width, nil
	}
	appendMember(current)

	for i := 1; i < count; i++ {
		prefix64, e := readSetUvarint(data, &offset)
		if e != nil || prefix64 > uint64(width) {
			return nil, false, 0, 0, errors.New("invalid tiny set")
		}
		prefix := int(prefix64)
		suffixLen := width - prefix
		if suffixLen > len(data)-offset {
			return nil, false, 0, 0, errors.New("invalid tiny set")
		}
		copy(current[prefix:], data[offset:offset+suffixLen])
		offset += suffixLen

		cmp = bytes.Compare(current, target)
		if cmp == 0 {
			return nil, false, count, width, nil
		}
		appendMember(current)
	}
	if offset != len(data) {
		return nil, false, 0, 0, errors.New("invalid tiny set trailing data")
	}
	if !inserted {
		members = append(members, target...)
	}

	newCount := count + 1
	out := make([]byte, 0, len(data)+width+binary.MaxVarintLen64)
	out = append(out, tinySetHeader[:]...)
	out = appendSetUvarint(out, uint64(newCount))
	out = appendSetUvarint(out, uint64(width))

	first := members[:width]
	out = append(out, first...)
	previous := first
	for i := 1; i < newCount; i++ {
		member := members[i*width : (i+1)*width]
		prefix := commonSetPrefix(previous, member)
		out = appendSetUvarint(out, uint64(prefix))
		out = append(out, member[prefix:]...)
		previous = member
	}
	return out, true, newCount, width, nil
}

func tinyFixedSetContains(data, target []byte) (bool, error) {
	if len(data) < len(tinySetHeader) || !bytes.Equal(data[:len(tinySetHeader)], tinySetHeader[:]) {
		return false, errors.New("invalid tiny set")
	}
	offset := len(tinySetHeader)
	count64, err := readSetUvarint(data, &offset)
	if err != nil || count64 < 2 || count64 > uint64(maxPackedSetBytes) {
		return false, errors.New("invalid tiny set")
	}
	width64, err := readSetUvarint(data, &offset)
	if err != nil || width64 > uint64(maxPackedSetBytes) {
		return false, errors.New("invalid tiny set")
	}
	count, width := int(count64), int(width64)
	if len(target) != width {
		return false, nil
	}
	if width > len(data)-offset {
		return false, errors.New("invalid tiny set")
	}

	// Members are sorted and front-coded against their predecessor. Track how
	// many leading bytes the current member shares with the target: a member
	// whose shared prefix with its predecessor is longer than that is still
	// smaller than the target (skip it without copying), a shorter one is larger
	// (the target is absent), and an equal one needs only its suffix compared.
	first := data[offset : offset+width]
	offset += width
	match := 0
	for match < width && first[match] == target[match] {
		match++
	}
	if match == width {
		return true, nil
	}
	if first[match] > target[match] {
		return false, nil
	}

	for i := 1; i < count; i++ {
		if offset >= len(data) {
			return false, errors.New("invalid tiny set")
		}
		prefix := int(data[offset])
		if prefix >= 0x80 {
			prefix64, err := readSetUvarint(data, &offset)
			if err != nil {
				return false, errors.New("invalid tiny set")
			}
			prefix = int(prefix64)
		} else {
			offset++
		}
		if prefix >= width {
			return false, errors.New("invalid tiny set")
		}
		suffixLen := width - prefix
		if suffixLen > len(data)-offset {
			return false, errors.New("invalid tiny set")
		}
		if prefix > match {
			offset += suffixLen
			continue
		}
		if prefix < match {
			return false, nil
		}
		suffix := data[offset : offset+suffixLen]
		rest := target[prefix:]
		k := 0
		for k < suffixLen && suffix[k] == rest[k] {
			k++
		}
		if k == suffixLen {
			return true, nil
		}
		if suffix[k] > rest[k] {
			return false, nil
		}
		match = prefix + k
		offset += suffixLen
	}
	if offset != len(data) {
		return false, errors.New("invalid tiny set trailing data")
	}
	return false, nil
}

func setPreparedEntryFromMembers(packed []byte, members [][]byte) preparedEntry {
	stored := append([]byte(nil), packed...)
	switch len(members) {
	case 1:
		// Raw singleton storage has no framing overhead. Avoid the tiny-set
		// magic prefix so decoding remains unambiguous for arbitrary bytes.
		if !bytes.HasPrefix(members[0], tinySetHeader[:]) {
			stored = append([]byte(nil), members[0]...)
		}
	default:
		if tiny, ok := encodeTinyFixedSet(members); ok && len(tiny) < len(stored) {
			stored = tiny
		}
	}
	return preparedEntry{
		entry: entry{entryData: entryData{valueType: TypeSet, rawLength: uint32(len(packed))}},
		data:  stored,
	}
}

func setPreparedEntry(packed []byte) preparedEntry {
	members, err := decodePackedSet(packed)
	if err != nil {
		return preparedEntry{
			entry: entry{entryData: entryData{valueType: TypeSet, rawLength: uint32(len(packed))}},
			data:  append([]byte(nil), packed...),
		}
	}
	return setPreparedEntryFromMembers(packed, members)
}

func (s *Store) setMembersFromEntry(sh *shard, e entry) ([][]byte, error) {
	physical := sh.encoded(e)
	if isIndexedSet(physical) {
		return decodeIndexedSet(physical)
	}
	if len(physical) == int(e.rawLength) {
		return decodePackedSet(physical)
	}
	if bytes.HasPrefix(physical, tinySetHeader[:]) {
		return decodeTinyFixedSet(physical)
	}
	// The only unframed SET representation is a singleton. Reconstructing its
	// canonical SS1 value also validates rawLength and avoids magic collisions.
	packed, err := encodePackedSet([][]byte{physical})
	if err != nil || len(packed) != int(e.rawLength) {
		return nil, errors.New("invalid singleton set")
	}
	return [][]byte{append([]byte(nil), physical...)}, nil
}

func (s *Store) setLogicalValue(sh *shard, e entry) ([]byte, error) {
	members, err := s.setMembersFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	return encodePackedSet(members)
}

// SetAdd inserts binary-safe members and returns the number of newly added members.
func (s *Store) setAddLocked(sh *shard, key string, hash uint64, members [][]byte) (int64, error) {
	now := s.now()
	old, exists := sh.getHashed(key, hash)
	if exists && sh.expired(key, old, now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}

	var current [][]byte
	var expiresAt stamp
	if exists {
		if old.valueType != TypeSet {
			return 0, setWrongType()
		}
		expiresAt = sh.expirationAt(key, old)
		physical := sh.encoded(old)
		if len(members) == 1 && bytes.HasPrefix(physical, tinySetHeader[:]) {
			tiny, added, newCount, width, err := tinyFixedSetAddOne(physical, members[0])
			if err != nil {
				return 0, err
			}
			if !added {
				return 0, nil
			}
			if newCount < tinySetMaxMembers {
				oldCount := newCount - 1
				oldCountLen := len(appendSetUvarint(nil, uint64(oldCount)))
				newCountLen := len(appendSetUvarint(nil, uint64(newCount)))
				rawLength := int(old.rawLength) + width + 1 + (newCountLen - oldCountLen)
				updated := preparedEntry{
					entry: entry{entryData: entryData{
						valueType: TypeSet,
						rawLength: uint32(rawLength),
					}},
					data: tiny,
					expiresAt: expiresAt,
				}
				if err := s.publish(sh, key, updated); err != nil {
					return 0, err
				}
				return 1, nil
			}
			// Promotion at the indexed threshold falls through to the generic
			// path so the existing canonicalization checks remain authoritative.
		}
		if isIndexedSet(physical) {
			added, rebuilt, err := indexedSetAdd(physical, members)
			if err != nil {
				return 0, err
			}
			if rebuilt == nil {
				return added, nil
			}
			updated := preparedEntry{
				entry: entry{entryData: entryData{
					valueType: TypeSet,
					rawLength: uint32(len(rebuilt)),
				}},
				data: rebuilt,
				expiresAt: expiresAt,
			}
			if err := s.publish(sh, key, updated); err != nil {
				return 0, err
			}
			return added, nil
		}
		var err error
		current, err = s.setMembersFromEntry(sh, old)
		if err != nil {
			return 0, err
		}
	}

	var added int64
	for _, member := range members {
		idx := sort.Search(len(current), func(i int) bool { return bytes.Compare(current[i], member) >= 0 })
		if idx < len(current) && bytes.Equal(current[idx], member) {
			continue
		}
		copyMember := append([]byte(nil), member...)
		current = append(current, nil)
		copy(current[idx+1:], current[idx:])
		current[idx] = copyMember
		added++
	}
	if added == 0 {
		return 0, nil
	}
	var updated preparedEntry
	if len(current) >= setPromoteThreshold(current) {
		indexed, err := encodeIndexedSet(current)
		if err != nil {
			return 0, err
		}
		updated = preparedEntry{
			entry: entry{entryData: entryData{
				valueType: TypeSet,
				rawLength: uint32(len(indexed)),
			}},
			data: indexed,
		}
	} else {
		packed, err := encodePackedSetSorted(current)
		if err != nil {
			return 0, err
		}
		updated = setPreparedEntryFromMembers(packed, current)
	}
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return added, nil
}

func (s *Store) SetAdd(key string, members [][]byte) (int64, error) {
	if len(members) == 0 {
		return 0, errors.New("ERR invalid set member count")
	}
	hash := index.Hash(key)
	sh := s.shardForHash(hash)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return s.setAddLocked(sh, key, hash, members)
}

// SetAddResults applies a sequence of SADD key member operations for one key
// under a single shard lock and returns one Redis-compatible result per member.
// Duplicate members within the batch preserve sequential command semantics.
func (s *Store) SetAddResults(key string, members [][]byte) ([]int64, error) {
	if len(members) == 0 {
		return nil, errors.New("ERR invalid set member count")
	}

	hash := index.Hash(key)
	sh := s.shardForHash(hash)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	results := make([]int64, len(members))
	now := s.now()
	old, exists := sh.getHashed(key, hash)
	if exists && sh.expired(key, old, now) {
		s.remove(sh, key)
		exists = false
		old = entry{}
	}
	if exists && old.valueType != TypeSet {
		return nil, setWrongType()
	}

	seen := make(map[string]struct{}, len(members))
	var physical []byte
	if exists {
		physical = sh.encoded(old)

		if isIndexedSet(physical) {
			count, _, _, _, metaErr := indexedSetMeta(physical)
			if metaErr != nil {
				return nil, metaErr
			}
			// Medium sets benefit more from the existing pre-probe/batched
			// rebuild path, while large sets benefit from combining membership
			// detection and insertion into one hash probe. Keep the crossover
			// above the set-medium workload so the two representations can use
			// their individually faster mutation strategy.
			if count >= 128 {
				results, rebuilt, err := indexedSetAddResults(physical, members)
				if err != nil {
					return nil, err
				}
				if rebuilt == nil {
					return results, nil
				}
				updated := preparedEntry{
					entry: entry{entryData: entryData{
						valueType: TypeSet,
						rawLength: uint32(len(rebuilt)),
					}},
					data: rebuilt,
					expiresAt: sh.expirationAt(key, old),
				}
				if err := s.publish(sh, key, updated); err != nil {
					return nil, err
				}
				return results, nil
			}
		}
	}

	pending := make([][]byte, 0, len(members))
	for i, member := range members {
		memberKey := string(member)
		if _, duplicate := seen[memberKey]; duplicate {
			continue
		}
		seen[memberKey] = struct{}{}

		if exists {
			found, err := setContainsPhysical(physical, old, member)
			if err != nil {
				return nil, err
			}
			if found {
				continue
			}
		}

		results[i] = 1
		pending = append(pending, member)
	}

	if len(pending) == 0 {
		return results, nil
	}
	if _, err := s.setAddLocked(sh, key, hash, pending); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) SetRemove(key string, members [][]byte) (int64, error) {
	if len(members) == 0 {
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
	if e.valueType != TypeSet {
		return 0, setWrongType()
	}
	current, err := s.setMembersFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	remove := make(map[string]struct{}, len(members))
	for _, member := range members {
		remove[string(member)] = struct{}{}
	}
	kept := current[:0]
	var removed int64
	for _, member := range current {
		if _, found := remove[string(member)]; found {
			removed++
			continue
		}
		kept = append(kept, member)
	}
	if removed == 0 {
		return 0, nil
	}
	if len(kept) == 0 {
		s.remove(sh, key)
		return removed, nil
	}
	packed, err := encodePackedSet(kept)
	if err != nil {
		return 0, err
	}
	updated := setPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return removed, nil
}

func setContainsPhysical(physical []byte, e entry, member []byte) (bool, error) {
	if isIndexedSet(physical) {
		return indexedSetContains(physical, member)
	}
	if len(physical) == int(e.rawLength) {
		return packedSetContains(physical, member)
	}
	if bytes.HasPrefix(physical, tinySetHeader[:]) {
		return tinyFixedSetContains(physical, member)
	}
	// Unframed SET storage is only valid for a singleton.
	return bytes.Equal(physical, member), nil
}

func (s *Store) SetContains(key string, member []byte) (bool, error) {
	hash := index.Hash(key)
	sh := s.shardForHash(hash)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.getHashed(key, hash)
	if !ok || sh.expired(key, e, s.now()) {
		return false, nil
	}
	if e.valueType != TypeSet {
		return false, setWrongType()
	}
	return setContainsPhysical(sh.encoded(e), e, member)
}

// SetContainsBytes avoids allocating a string for the common non-expiring TCP
// SISMEMBER path. Expiring keys convert only when consulting the expiration map.
func (s *Store) SetContainsBytes(key, member []byte) (bool, error) {
	hash := index.HashBytes(key)
	sh := s.shardForHash(hash)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.getHashedBytes(key, hash)
	if !ok {
		return false, nil
	}
	if e.hasExpiry && sh.expired(string(key), e, s.now()) {
		return false, nil
	}
	if e.valueType != TypeSet {
		return false, setWrongType()
	}
	return setContainsPhysical(sh.encoded(e), e, member)
}

// SetContainsResultsBytes answers multiple SISMEMBER probes for one key
// under a single shard read lock and a single key lookup.
func (s *Store) SetContainsResultsBytes(key []byte, members [][]byte) ([]int64, error) {
	results := make([]int64, len(members))
	if len(members) == 0 {
		return results, nil
	}

	hash := index.HashBytes(key)
	sh := s.shardForHash(hash)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.getHashedBytes(key, hash)
	if !ok {
		return results, nil
	}
	if e.hasExpiry && sh.expired(string(key), e, s.now()) {
		return results, nil
	}
	if e.valueType != TypeSet {
		return nil, setWrongType()
	}

	physical := sh.encoded(e)
	for i, member := range members {
		found, err := setContainsPhysical(physical, e, member)
		if err != nil {
			return nil, err
		}
		if found {
			results[i] = 1
		}
	}
	return results, nil
}

func (s *Store) SetLen(key string) (int64, error) {
	hash := index.Hash(key)
	sh := s.shardForHash(hash)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.getHashed(key, hash)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, nil
	}
	if e.valueType != TypeSet {
		return 0, setWrongType()
	}
	physical := sh.encoded(e)
	if isIndexedSet(physical) {
		count, _, _, _, err := indexedSetMeta(physical)
		return int64(count), err
	}
	if len(physical) == int(e.rawLength) {
		count, err := packedSetCount(physical)
		return int64(count), err
	}
	if bytes.HasPrefix(physical, tinySetHeader[:]) {
		offset := len(tinySetHeader)
		count, err := readSetUvarint(physical, &offset)
		if err != nil {
			return 0, errors.New("invalid tiny set")
		}
		return int64(count), nil
	}
	return 1, nil
}

func (s *Store) SetMembers(key string) ([][]byte, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, nil
	}
	if e.valueType != TypeSet {
		return nil, setWrongType()
	}
	return s.setMembersFromEntry(sh, e)
}

// CompactIndexedSet freezes a mutable indexed SET after foreground writes
// have gone quiet. Small sets are demoted back to the compact packed/prefix
// representation; larger sets retain their hash index but shed append reserve.
func (s *Store) CompactIndexedSet(key string) bool {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) || e.valueType != TypeSet {
		return false
	}
	physical := sh.encoded(e)
	if !isIndexedSet(physical) {
		return false
	}

	count, _, used, dataStart, err := indexedSetMeta(physical)
	if err != nil {
		return false
	}

	if count < 32 {
		members, err := decodeIndexedSet(physical)
		if err != nil {
			return false
		}
		packed, err := encodePackedSetSorted(members)
		if err != nil {
			return false
		}
		updated := setPreparedEntryFromMembers(packed, members)
		updated.expiresAt = sh.expirationAt(key, e)
		return s.publish(sh, key, updated) == nil
	}

	targetLen := dataStart + used
	if targetLen >= len(physical) {
		return false
	}

	compact := append([]byte(nil), physical[:targetLen]...)
	updated := preparedEntry{
		entry: entry{entryData: entryData{
			valueType: TypeSet,
			rawLength: e.rawLength,
		}},
		data: compact,
		expiresAt: sh.expirationAt(key, e),
	}
	return s.publish(sh, key, updated) == nil
}

func (s *Store) SetStorageStats(key string) (SetStats, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return SetStats{}, false, nil
	}
	if e.valueType != TypeSet {
		return SetStats{}, false, setWrongType()
	}
	members, err := s.setMembersFromEntry(sh, e)
	if err != nil {
		return SetStats{}, false, err
	}
	physical := sh.encoded(e)
	encoding := "packed"
	if isIndexedSet(physical) {
		encoding = "indexed"
	} else if len(members) == 1 && len(physical) != int(e.rawLength) {
		encoding = "singleton"
	} else if bytes.HasPrefix(physical, tinySetHeader[:]) {
		encoding = "prefix"
	}
	stats := SetStats{Members: len(members), PackedBytes: int(e.rawLength), StoredBytes: len(physical), Encoding: encoding}
	for _, member := range members {
		stats.MemberBytes += len(member)
	}
	return stats, true, nil
}
