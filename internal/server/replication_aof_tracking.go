package server

type replicaDurabilityPoint struct {
	sequence uint64
	offset   int64
}

func (s *Server) noteReplicaAOFOffset(offset int64) {
	journal, ok := s.journal.(durabilityJournal)
	if !ok {
		return
	}
	appended, _, _ := journal.DurabilitySnapshot()

	s.replicaDurabilityMu.Lock()
	defer s.replicaDurabilityMu.Unlock()

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
