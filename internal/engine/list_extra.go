package engine

import (
	"bytes"
	"errors"
)

func (s *Store) ListPushLeftX(key string, values [][]byte) (int64, error) {
	return s.listPushX(key, values, true)
}

func (s *Store) ListPushRightX(key string, values [][]byte) (int64, error) {
	return s.listPushX(key, values, false)
}

func (s *Store) listPushX(key string, values [][]byte, left bool) (int64, error) {
	if len(values) == 0 {
		return 0, errors.New("ERR invalid list element count")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	old, exists := sh.get(key)
	if !exists || sh.expired(key, old, now) {
		if exists {
			s.remove(sh, key)
		}
		return 0, nil
	}
	if old.valueType != TypeList {
		return 0, listWrongType()
	}

	current, err := s.listElementsFromEntry(sh, old)
	if err != nil {
		return 0, err
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
	updated.expiresAt = sh.expirationAt(key, old)
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return int64(len(result)), nil
}

func (s *Store) ListSet(key string, index int64, value []byte) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		if ok {
			s.remove(sh, key)
		}
		return errors.New("ERR no such key")
	}
	if e.valueType != TypeList {
		return listWrongType()
	}

	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return err
	}
	n := int64(len(elements))
	if index < 0 {
		index += n
	}
	if index < 0 || index >= n {
		return errors.New("ERR index out of range")
	}
	elements[index] = append([]byte(nil), value...)

	packed, err := encodePackedList(elements)
	if err != nil {
		return err
	}
	updated := listPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	return s.publish(sh, key, updated)
}

func normalizeListRange(n, start, stop int64) (int64, int64, bool) {
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
		return 0, 0, false
	}
	if stop >= n {
		stop = n - 1
	}
	return start, stop, true
}

func (s *Store) ListTrim(key string, start, stop int64) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		if ok {
			s.remove(sh, key)
		}
		return nil
	}
	if e.valueType != TypeList {
		return listWrongType()
	}

	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return err
	}
	from, to, keep := normalizeListRange(int64(len(elements)), start, stop)
	if !keep {
		s.remove(sh, key)
		return nil
	}

	packed, err := encodePackedList(elements[from : to+1])
	if err != nil {
		return err
	}
	updated := listPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	return s.publish(sh, key, updated)
}

func (s *Store) ListRemove(key string, count int64, value []byte) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		if ok {
			s.remove(sh, key)
		}
		return 0, nil
	}
	if e.valueType != TypeList {
		return 0, listWrongType()
	}

	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	remove := make([]bool, len(elements))
	removed := 0

	if count >= 0 {
		limit := len(elements)
		if count > 0 && count < int64(limit) {
			limit = int(count)
		}
		for i := 0; i < len(elements); i++ {
			if bytes.Equal(elements[i], value) && (count == 0 || removed < limit) {
				remove[i] = true
				removed++
				if count > 0 && removed == limit {
					break
				}
			}
		}
	} else {
		limit := len(elements)
		if count != -1<<63 {
			want := -count
			if want < int64(limit) {
				limit = int(want)
			}
		}
		for i := len(elements) - 1; i >= 0 && removed < limit; i-- {
			if bytes.Equal(elements[i], value) {
				remove[i] = true
				removed++
			}
		}
	}

	if removed == 0 {
		return 0, nil
	}
	if removed == len(elements) {
		s.remove(sh, key)
		return int64(removed), nil
	}

	kept := make([][]byte, 0, len(elements)-removed)
	for i := range elements {
		if !remove[i] {
			kept = append(kept, elements[i])
		}
	}
	packed, err := encodePackedList(kept)
	if err != nil {
		return 0, err
	}
	updated := listPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return int64(removed), nil
}

func (s *Store) ListInsert(key string, before bool, pivot, value []byte) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, now) {
		if ok {
			s.remove(sh, key)
		}
		return 0, nil
	}
	if e.valueType != TypeList {
		return 0, listWrongType()
	}

	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return 0, err
	}
	pivotIndex := -1
	for i := range elements {
		if bytes.Equal(elements[i], pivot) {
			pivotIndex = i
			break
		}
	}
	if pivotIndex < 0 {
		return -1, nil
	}
	insertAt := pivotIndex + 1
	if before {
		insertAt = pivotIndex
	}
	result := make([][]byte, 0, len(elements)+1)
	result = append(result, elements[:insertAt]...)
	result = append(result, append([]byte(nil), value...))
	result = append(result, elements[insertAt:]...)

	packed, err := encodePackedList(result)
	if err != nil {
		return 0, err
	}
	updated := listPreparedEntry(packed)
	updated.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}
	return int64(len(result)), nil
}

func (s *Store) ListPos(key string, value []byte, rank, count, maxLen int64, withCount bool) ([]int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, nil
	}
	if e.valueType != TypeList {
		return nil, listWrongType()
	}
	elements, err := s.listElementsFromEntry(sh, e)
	if err != nil {
		return nil, err
	}

	results := make([]int64, 0)
	if rank > 0 {
		seen := int64(0)
		scanned := int64(0)
		for i := 0; i < len(elements); i++ {
			if maxLen > 0 && scanned >= maxLen {
				break
			}
			scanned++
			if !bytes.Equal(elements[i], value) {
				continue
			}
			seen++
			if seen < rank {
				continue
			}
			results = append(results, int64(i))
			if !withCount || count > 0 && int64(len(results)) >= count {
				break
			}
		}
		return results, nil
	}

	// Compute |rank| without overflowing for MinInt64.
	wanted := uint64(-(rank + 1)) + 1
	seen := uint64(0)
	scanned := int64(0)
	for i := len(elements) - 1; i >= 0; i-- {
		if maxLen > 0 && scanned >= maxLen {
			break
		}
		scanned++
		if !bytes.Equal(elements[i], value) {
			continue
		}
		seen++
		if seen < wanted {
			continue
		}
		results = append(results, int64(i))
		if !withCount || count > 0 && int64(len(results)) >= count {
			break
		}
	}
	return results, nil
}
