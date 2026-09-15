package engine

import "errors"

// RenameHash handles RENAME/RENAMENX when the source is a HASH. It returns
// handled=false for non-HASH or missing sources so the generic rename path can
// preserve its existing behavior and error text.
func (s *Store) RenameHash(source, destination string, nx bool) (handled bool, renamed bool, err error) {
	if source == destination {
		sh := s.shardFor(source)
		sh.mu.RLock()
		e, ok := sh.get(source)
		now := s.now()
		isHash := ok && !e.expired(now) && e.valueType == TypeHash
		sh.mu.RUnlock()
		if !isHash {
			return false, false, nil
		}
		return true, false, errors.New("ERR source and destination objects are the same")
	}

	unlock := s.lockAll()
	defer unlock()

	now := s.now()
	sourceShard := s.shardFor(source)
	destinationShard := s.shardFor(destination)

	sourceEntry, sourceExists := sourceShard.get(source)
	if !sourceExists || sourceEntry.expired(now) {
		if sourceExists {
			s.remove(sourceShard, source)
		}
		return false, false, nil
	}
	if sourceEntry.valueType != TypeHash {
		return false, false, nil
	}

	destinationEntry, destinationExists := destinationShard.get(destination)
	if destinationExists && destinationEntry.expired(now) {
		s.remove(destinationShard, destination)
		destinationExists = false
	}
	if nx && destinationExists {
		return true, false, nil
	}

	packed := s.decode(sourceShard, sourceEntry)
	pairs, decodeErr := decodePackedHash(packed)
	if decodeErr != nil {
		return true, false, decodeErr
	}

	replacement := s.hashEntry(pairs, packed)
	replacement.expiresAt = sourceEntry.expiresAt
	if publishErr := s.publish(destinationShard, destination, replacement); publishErr != nil {
		return true, false, publishErr
	}

	s.remove(sourceShard, source)
	return true, true, nil
}
