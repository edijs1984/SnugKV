package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
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
	members, err := decodePackedSet(data)
	if err != nil {
		return false, err
	}
	idx := sort.Search(len(members), func(i int) bool { return bytes.Compare(members[i], target) >= 0 })
	return idx < len(members) && bytes.Equal(members[idx], target), nil
}

func setPreparedEntry(packed []byte) preparedEntry {
	stored := append([]byte(nil), packed...)
	if members, err := decodePackedSet(packed); err == nil {
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
	}
	return preparedEntry{
		entry: entry{valueType: TypeSet, rawLength: uint32(len(packed))},
		data:  stored,
	}
}

func (s *Store) setMembersFromEntry(sh *shard, e entry) ([][]byte, error) {
	physical := sh.encoded(e)
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
func (s *Store) SetAdd(key string, members [][]byte) (int64, error) {
	if len(members) == 0 {
		return 0, errors.New("ERR invalid set member count")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	old, exists := sh.get(key)
	if exists && old.expired(now) {
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
		var err error
		current, err = s.setMembersFromEntry(sh, old)
		if err != nil {
			return 0, err
		}
		expiresAt = old.expiresAt
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
	packed, err := encodePackedSet(current)
	if err != nil {
		return 0, err
	}
	updated := setPreparedEntry(packed)
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return added, nil
}

func (s *Store) SetRemove(key string, members [][]byte) (int64, error) {
	if len(members) == 0 {
		return 0, nil
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
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
	updated.expiresAt = e.expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return removed, nil
}

func (s *Store) SetContains(key string, member []byte) (bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return false, nil
	}
	if e.valueType != TypeSet {
		return false, setWrongType()
	}
	members, err := s.setMembersFromEntry(sh, e)
	if err != nil {
		return false, err
	}
	idx := sort.Search(len(members), func(i int) bool { return bytes.Compare(members[i], member) >= 0 })
	return idx < len(members) && bytes.Equal(members[idx], member), nil
}

func (s *Store) SetLen(key string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return 0, nil
	}
	if e.valueType != TypeSet {
		return 0, setWrongType()
	}
	members, err := s.setMembersFromEntry(sh, e)
	return int64(len(members)), err
}

func (s *Store) SetMembers(key string) ([][]byte, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return nil, nil
	}
	if e.valueType != TypeSet {
		return nil, setWrongType()
	}
	return s.setMembersFromEntry(sh, e)
}

func (s *Store) SetStorageStats(key string) (SetStats, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
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
	if len(members) == 1 && len(physical) != int(e.rawLength) {
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
