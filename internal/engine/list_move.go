package engine

// ListMove atomically pops one element from sourceLeft/sourceRight and pushes
// it to destinationLeft/destinationRight. Existing TTLs on both keys are
// preserved. A newly created destination is persistent.
func (s *Store) ListMove(source, destination string, sourceLeft, destinationLeft bool) ([]byte, bool, error) {
	unlock := s.lockAll()
	defer unlock()

	now := s.now()
	sourceShard := s.shardFor(source)
	sourceEntry, sourceExists := sourceShard.get(source)
	if !sourceExists || sourceShard.expired(source, sourceEntry, now) {
		if sourceExists {
			s.remove(sourceShard, source)
		}
		return nil, false, nil
	}
	if sourceEntry.valueType != TypeList {
		return nil, false, listWrongType()
	}

	sourceElements, err := s.listElementsFromEntry(sourceShard, sourceEntry)
	if err != nil {
		return nil, false, err
	}
	if len(sourceElements) == 0 {
		return nil, false, nil
	}

	popIndex := 0
	if !sourceLeft {
		popIndex = len(sourceElements) - 1
	}
	moved := append([]byte(nil), sourceElements[popIndex]...)

	// Same-key LMOVE is a rotation (or a logical no-op when both sides match).
	// It must preserve the key TTL exactly.
	if source == destination {
		if len(sourceElements) == 1 || sourceLeft == destinationLeft {
			return moved, true, nil
		}

		remaining := make([][]byte, 0, len(sourceElements)-1)
		remaining = append(remaining, sourceElements[:popIndex]...)
		remaining = append(remaining, sourceElements[popIndex+1:]...)

		rotated := make([][]byte, 0, len(sourceElements))
		if destinationLeft {
			rotated = append(rotated, moved)
			rotated = append(rotated, remaining...)
		} else {
			rotated = append(rotated, remaining...)
			rotated = append(rotated, moved)
		}

		packed, err := encodePackedList(rotated)
		if err != nil {
			return nil, false, err
		}
		updated := listPreparedEntry(packed)
		updated.expiresAt = sourceShard.expirationAt(source, sourceEntry)
		if err := s.publish(sourceShard, source, updated); err != nil {
			return nil, false, err
		}
		return moved, true, nil
	}

	destinationShard := s.shardFor(destination)
	destinationEntry, destinationExists := destinationShard.get(destination)
	if destinationExists && destinationShard.expired(destination, destinationEntry, now) {
		s.remove(destinationShard, destination)
		destinationExists = false
	}
	if destinationExists && destinationEntry.valueType != TypeList {
		return nil, false, listWrongType()
	}

	var destinationElements [][]byte
	if destinationExists {
		destinationElements, err = s.listElementsFromEntry(destinationShard, destinationEntry)
		if err != nil {
			return nil, false, err
		}
	}

	remaining := make([][]byte, 0, len(sourceElements)-1)
	remaining = append(remaining, sourceElements[:popIndex]...)
	remaining = append(remaining, sourceElements[popIndex+1:]...)

	nextDestination := make([][]byte, 0, len(destinationElements)+1)
	if destinationLeft {
		nextDestination = append(nextDestination, moved)
		nextDestination = append(nextDestination, destinationElements...)
	} else {
		nextDestination = append(nextDestination, destinationElements...)
		nextDestination = append(nextDestination, moved)
	}

	updates := make(map[string]preparedEntry, 2)
	deletions := make(map[string]bool, 1)

	if len(remaining) == 0 {
		deletions[source] = true
	} else {
		packed, err := encodePackedList(remaining)
		if err != nil {
			return nil, false, err
		}
		updated := listPreparedEntry(packed)
		updated.expiresAt = sourceShard.expirationAt(source, sourceEntry)
		updates[source] = updated
	}

	packedDestination, err := encodePackedList(nextDestination)
	if err != nil {
		return nil, false, err
	}
	updatedDestination := listPreparedEntry(packedDestination)
	if destinationExists {
		updatedDestination.expiresAt = destinationShard.expirationAt(destination, destinationEntry)
	}
	updates[destination] = updatedDestination

	if err := s.applyPreparedBatchLocked(updates, deletions, true); err != nil {
		return nil, false, err
	}
	return moved, true, nil
}
