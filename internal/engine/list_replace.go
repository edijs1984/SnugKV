package engine

// ListReplace atomically replaces key with the supplied list elements.
// STORE-style commands use replacement semantics: an empty result deletes the
// destination and a non-empty result clears any previous TTL.
func (s *Store) ListReplace(key string, elements [][]byte) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if len(elements) == 0 {
		s.remove(sh, key)
		return nil
	}

	copyElements := make([][]byte, len(elements))
	for i, element := range elements {
		copyElements[i] = append([]byte(nil), element...)
	}
	packed, err := encodePackedList(copyElements)
	if err != nil {
		return err
	}

	// Deliberately do not copy the old expiration: SORT STORE replaces the
	// destination and clears any prior TTL.
	return s.publish(sh, key, listPreparedEntry(packed))
}
