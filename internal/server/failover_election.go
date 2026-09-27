package server

import "sort"

type failoverObservation struct {
	NodeID     string
	MasterDown bool
	Eligible   bool
	Offset     int64
	Priority   int
}

type failoverElectionResult struct {
	QuorumReached bool
	DownVotes     int
	CandidateID   string
}

func evaluateFailoverElection(observations []failoverObservation, quorum int) failoverElectionResult {
	if quorum <= 0 {
		return failoverElectionResult{}
	}

	unique := make(map[string]failoverObservation, len(observations))
	for _, observation := range observations {
		if observation.NodeID == "" {
			continue
		}
		if current, exists := unique[observation.NodeID]; exists {
			if observation.Offset < current.Offset {
				observation.Offset = current.Offset
			}
			observation.MasterDown = observation.MasterDown || current.MasterDown
			observation.Eligible = observation.Eligible || current.Eligible
			if observation.Priority == 0 || current.Priority != 0 && current.Priority < observation.Priority {
				observation.Priority = current.Priority
			}
		}
		unique[observation.NodeID] = observation
	}

	result := failoverElectionResult{}
	candidates := make([]failoverObservation, 0, len(unique))
	for _, observation := range unique {
		if observation.MasterDown {
			result.DownVotes++
		}
		if observation.Eligible && observation.Priority > 0 {
			candidates = append(candidates, observation)
		}
	}
	if result.DownVotes < quorum {
		return result
	}
	result.QuorumReached = true
	if len(candidates) == 0 {
		return result
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Offset != candidates[j].Offset {
			return candidates[i].Offset > candidates[j].Offset
		}
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].NodeID < candidates[j].NodeID
	})
	result.CandidateID = candidates[0].NodeID
	return result
}
