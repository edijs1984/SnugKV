package engine

import "errors"

// RenameStream handles RENAME/RENAMENX when the source is a STREAM. It returns
// handled=false for non-STREAM or missing sources so the existing datatype-aware
// rename chain can continue unchanged.
func (s *Store) RenameStream(source, destination string, nx bool) (handled bool, renamed bool, err error) {
	if source == destination {
		sh := s.shardFor(source)
		sh.mu.RLock()
		e, ok := sh.get(source)
		now := s.now()
		isStream := ok && !sh.expired(source, e, now) && e.valueType == TypeStream
		sh.mu.RUnlock()
		if !isStream {
			return false, false, nil
		}
		return true, false, errors.New("ERR source and destination objects are the same")
	}

	unlock := s.lockAll()
	defer unlock()
	return s.renameStreamLocked(source, destination, nx)
}

// renameStreamLocked is RenameStream for callers that hold every shard lock.
func (s *Store) renameStreamLocked(source, destination string, nx bool) (handled bool, renamed bool, err error) {
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
	if sourceEntry.valueType != TypeStream {
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

	state, loadErr := s.streamStateFromEntry(sourceShard, source, sourceEntry)
	if loadErr != nil {
		return true, false, loadErr
	}
	stub, encodeErr := encodeStreamStub(state)
	if encodeErr != nil {
		return true, false, encodeErr
	}
	replacement := streamPreparedEntry(stub)
	replacement.keepStream = true
	replacement.expiresAt = sourceShard.expirationAt(source, sourceEntry)
	if publishErr := s.publish(destinationShard, destination, replacement); publishErr != nil {
		return true, false, publishErr
	}
	// The entry log moves with the key; its memory charge stays as it was.
	s.dropStreamBody(destinationShard, destination)
	s.streamLogs.del(source)
	s.streamLogs.set(destination, state.log)
	s.remove(sourceShard, source)
	return true, true, nil
}
