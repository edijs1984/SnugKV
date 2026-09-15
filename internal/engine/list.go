package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
)

const maxPackedListBytes = 32 << 20

var packedListHeader = [...]byte{'S', 'L', 1}

type ListStats struct {
	Elements     int
	ElementBytes int
	PackedBytes  int
	StoredBytes  int
	Encoding     string
}

func listWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func appendListUvarint(dst []byte, value uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	return append(dst, buf[:n]...)
}

func readListUvarint(data []byte, offset *int) (uint64, error) {
	if *offset >= len(data) {
		return 0, errors.New("invalid packed list")
	}
	value, n := binary.Uvarint(data[*offset:])
	if n <= 0 {
		return 0, errors.New("invalid packed list")
	}
	*offset += n
	return value, nil
}

func encodePackedList(elements [][]byte) ([]byte, error) {
	capacity := len(packedListHeader) + binary.MaxVarintLen64
	for _, element := range elements {
		capacity += len(element) + binary.MaxVarintLen64
		if capacity > maxPackedListBytes {
			return nil, errors.New("ERR list exceeds 32 MiB limit")
		}
	}
	out := make([]byte, 0, capacity)
	out = append(out, packedListHeader[:]...)
	out = appendListUvarint(out, uint64(len(elements)))
	for _, element := range elements {
		out = appendListUvarint(out, uint64(len(element)))
		out = append(out, element...)
	}
	if len(out) > maxPackedListBytes {
		return nil, errors.New("ERR list exceeds 32 MiB limit")
	}
	return out, nil
}

func decodePackedList(data []byte) ([][]byte, error) {
	if len(data) < len(packedListHeader) || !bytes.Equal(data[:len(packedListHeader)], packedListHeader[:]) {
		return nil, errors.New("invalid packed list")
	}
	offset := len(packedListHeader)
	count64, err := readListUvarint(data, &offset)
	if err != nil || count64 > uint64(maxPackedListBytes) {
		return nil, errors.New("invalid packed list")
	}
	elements := make([][]byte, 0, int(count64))
	for i := 0; i < int(count64); i++ {
		length64, err := readListUvarint(data, &offset)
		if err != nil || length64 > uint64(len(data)-offset) {
			return nil, errors.New("invalid packed list")
		}
		end := offset + int(length64)
		elements = append(elements, append([]byte(nil), data[offset:end]...))
		offset = end
	}
	if offset != len(data) {
		return nil, errors.New("invalid packed list trailing data")
	}
	return elements, nil
}

func listPreparedEntry(packed []byte) preparedEntry {
	return preparedEntry{entry: entry{valueType: TypeList, rawLength: uint32(len(packed))}, data: append([]byte(nil), packed...)}
}

func (s *Store) listElementsFromEntry(sh *shard, e entry) ([][]byte, error) {
	return decodePackedList(sh.encoded(e))
}

func (s *Store) listLogicalValue(sh *shard, e entry) ([]byte, error) {
	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	return encodePackedList(elements)
}

func (s *Store) ListPushLeft(key string, values [][]byte) (int64, error) { return s.listPush(key, values, true) }
func (s *Store) ListPushRight(key string, values [][]byte) (int64, error) { return s.listPush(key, values, false) }

func (s *Store) listPush(key string, values [][]byte, left bool) (int64, error) {
	if len(values) == 0 {
		return 0, errors.New("ERR invalid list element count")
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
		if old.valueType != TypeList {
			return 0, listWrongType()
		}
		var err error
		current, err = s.listElementsFromEntry(sh, old)
		if err != nil {
			return 0, err
		}
		expiresAt = old.expiresAt
	}
	result := make([][]byte, 0, len(current)+len(values))
	if left {
		for i := len(values) - 1; i >= 0; i-- {
			result = append(result, append([]byte(nil), values[i]...))
		}
		result = append(result, current...)
	} else {
		result = append(result, current...)
		for _, value := range values {
			result = append(result, append([]byte(nil), value...))
		}
	}
	packed, err := encodePackedList(result)
	if err != nil {
		return 0, err
	}
	updated := listPreparedEntry(packed)
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return int64(len(result)), nil
}

func (s *Store) ListPopLeft(key string, count int) ([][]byte, error) { return s.listPop(key, count, true) }
func (s *Store) ListPopRight(key string, count int) ([][]byte, error) { return s.listPop(key, count, false) }

func (s *Store) listPop(key string, count int, left bool) ([][]byte, error) {
	if count < 0 {
		return nil, errors.New("ERR count must be non-negative")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		if ok {
			s.remove(sh, key)
		}
		return nil, nil
	}
	if e.valueType != TypeList {
		return nil, listWrongType()
	}
	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return [][]byte{}, nil
	}
	if count > len(elements) {
		count = len(elements)
	}
	out := make([][]byte, count)
	var kept [][]byte
	if left {
		for i := 0; i < count; i++ {
			out[i] = append([]byte(nil), elements[i]...)
		}
		kept = elements[count:]
	} else {
		for i := 0; i < count; i++ {
			out[i] = append([]byte(nil), elements[len(elements)-1-i]...)
		}
		kept = elements[:len(elements)-count]
	}
	if len(kept) == 0 {
		s.remove(sh, key)
		return out, nil
	}
	packed, err := encodePackedList(kept)
	if err != nil {
		return nil, err
	}
	updated := listPreparedEntry(packed)
	updated.expiresAt = e.expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListLen(key string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return 0, nil
	}
	if e.valueType != TypeList {
		return 0, listWrongType()
	}
	elements, err := s.listElementsFromEntry(sh, e)
	return int64(len(elements)), err
}

func (s *Store) ListIndex(key string, index int64) ([]byte, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return nil, false, nil
	}
	if e.valueType != TypeList {
		return nil, false, listWrongType()
	}
	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return nil, false, err
	}
	n := int64(len(elements))
	if index < 0 {
		index += n
	}
	if index < 0 || index >= n {
		return nil, false, nil
	}
	return append([]byte(nil), elements[index]...), true, nil
}

func (s *Store) ListRange(key string, start, stop int64) ([][]byte, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return nil, nil
	}
	if e.valueType != TypeList {
		return nil, listWrongType()
	}
	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return nil, err
	}
	n := int64(len(elements))
	if start < 0 {
		start += n
	}
	if stop < 0 {
		stop += n
	}
	if start < 0 {
		start = 0
	}
	if stop < 0 || start >= n || start > stop {
		return nil, nil
	}
	if stop >= n {
		stop = n - 1
	}
	out := make([][]byte, 0, stop-start+1)
	for i := start; i <= stop; i++ {
		out = append(out, append([]byte(nil), elements[i]...))
	}
	return out, nil
}

func (s *Store) ListStorageStats(key string) (ListStats, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return ListStats{}, false, nil
	}
	if e.valueType != TypeList {
		return ListStats{}, false, listWrongType()
	}
	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return ListStats{}, false, err
	}
	logical, err := encodePackedList(elements)
	if err != nil {
		return ListStats{}, false, err
	}
	stats := ListStats{Elements: len(elements), PackedBytes: len(logical), StoredBytes: len(sh.encoded(e)), Encoding: "packed"}
	for _, element := range elements {
		stats.ElementBytes += len(element)
	}
	return stats, true, nil
}
