package server

type replicaDurabilityPoint struct {
	sequence uint64
	offset   int64
}

func (s *Server) noteReplicaAOFOffset(offset int64) {
	if s.durabilityFailed {
		return
	}
	journal, ok := s.journal.(durabilityJournal)
	if !ok {
		return
	}
	appended, synced, _ := journal.DurabilitySnapshot()
	if appended == 0 {
		return
	}

	s.replicaDurabilityMu.Lock()
	defer s.replicaDurabilityMu.Unlock()

	// With appendfsync=always, Append does not return until the accepted
	// sequence has crossed fsync. Publish that durability immediately instead
	// of waiting for a later ACK-time sweep. This also covers any other policy
	// state where the current append sequence is already known synced.
	//
	// The comparison is conservative: if another append advanced appended past
	// this replica frame but has not synced yet, we queue the point below rather
	// than claiming durability early.
	if synced >= appended {
		if !s.replicaDurabilityKnown || offset > s.replicaDurabilityFsynced {
			s.replicaDurabilityFsynced = offset
			s.replicaDurabilityKnown = true
		}
		return
	}

	n := len(s.replicaDurabilityPoints)
	if n > 0 && s.replicaDurabilityPoints[n-1].sequence == appended {
		if offset > s.replicaDurabilityPoints[n-1].offset {
			s.replicaDurabilityPoints[n-1].offset = offset
		}
		return
	}
	s.replicaDurabilityPoints = append(s.replicaDurabilityPoints, replicaDurabilityPoint{
		sequence: appended,
		offset:   offset,
	})
}

func (s *Server) replicaAOFFsyncedOffset() (int64, bool) {
	if s.durabilityFailed {
		return 0, false
	}
	journal, ok := s.journal.(durabilityJournal)
	if !ok {
		return 0, false
	}
	_, synced, _ := journal.DurabilitySnapshot()

	s.replicaDurabilityMu.Lock()
	defer s.replicaDurabilityMu.Unlock()

	consumed := 0
	for consumed < len(s.replicaDurabilityPoints) &&
		s.replicaDurabilityPoints[consumed].sequence <= synced {
		point := s.replicaDurabilityPoints[consumed]
		if !s.replicaDurabilityKnown || point.offset > s.replicaDurabilityFsynced {
			s.replicaDurabilityFsynced = point.offset
			s.replicaDurabilityKnown = true
		}
		consumed++
	}
	if consumed > 0 {
		copy(s.replicaDurabilityPoints, s.replicaDurabilityPoints[consumed:])
		s.replicaDurabilityPoints = s.replicaDurabilityPoints[:len(s.replicaDurabilityPoints)-consumed]
	}
	return s.replicaDurabilityFsynced, s.replicaDurabilityKnown
}

func (s *Server) resetReplicaAOFTracking() {
	s.replicaDurabilityMu.Lock()
	s.replicaDurabilityPoints = nil
	s.replicaDurabilityFsynced = 0
	s.replicaDurabilityKnown = false
	s.replicaDurabilityMu.Unlock()
}
