package engine

import "errors"

// RenameZSet handles RENAME/RENAMENX when the source is a ZSET. It returns
// handled=false for non-ZSET or missing sources so the generic rename path keeps
// its existing behavior and error text.
func (s *Store) RenameZSet(source, destination string, nx bool) (handled bool, renamed bool, err error) {
	if source == destination {
		sh := s.shardFor(source)
		sh.mu.RLock()
		e, ok := sh.get(source)
		now := s.now()
		isZSet := ok && !sh.expired(source, e, now) && e.valueType == TypeZSet
		sh.mu.RUnlock()
		if !isZSet {
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
	if !sourceExists || sourceShard.expired(source, sourceEntry, now) {
		if sourceExists {
			s.remove(sourceShard, source)
		}
		return false, false, nil
	}
	if sourceEntry.valueType != TypeZSet {
		return false, false, nil
	}

	destinationEntry, destinationExists := destinationShard.get(destination)
	if destinationExists && destinationShard.expired(destination, destinationEntry, now) {
		s.remove(destinationShard, destination)
		destinationExists = false
	}
	if nx && destinationExists {
		return true, false, nil
	}

	packed, decodeErr := s.zsetLogicalValue(sourceShard, sourceEntry)
	if decodeErr != nil {
		return true, false, decodeErr
	}
	replacement := zsetPreparedEntry(packed)
	replacement.expiresAt = sourceShard.expirationAt(source, sourceEntry)
	if publishErr := s.publish(destinationShard, destination, replacement); publishErr != nil {
		return true, false, publishErr
	}

	s.remove(sourceShard, source)
	return true, true, nil
}
