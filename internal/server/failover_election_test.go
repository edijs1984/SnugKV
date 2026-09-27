package server

import "testing"

func TestFailoverElectionRequiresQuorum(t *testing.T) {
	result := evaluateFailoverElection([]failoverObservation{
		{NodeID: "a", MasterDown: true, Eligible: true, Offset: 100, Priority: 100},
		{NodeID: "b", MasterDown: false, Eligible: true, Offset: 110, Priority: 100},
		{NodeID: "c", MasterDown: true, Eligible: true, Offset: 90, Priority: 100},
	}, 3)
	if result.QuorumReached {
		t.Fatalf("unexpected quorum: %+v", result)
	}
	if result.DownVotes != 2 {
		t.Fatalf("down votes=%d want=2", result.DownVotes)
	}
	if result.CandidateID != "" {
		t.Fatalf("candidate=%q without quorum", result.CandidateID)
	}
}

func TestFailoverElectionChoosesFreshestReplica(t *testing.T) {
	result := evaluateFailoverElection([]failoverObservation{
		{NodeID: "a", MasterDown: true, Eligible: true, Offset: 100, Priority: 100},
		{NodeID: "b", MasterDown: true, Eligible: true, Offset: 120, Priority: 200},
		{NodeID: "c", MasterDown: true, Eligible: true, Offset: 110, Priority: 50},
	}, 2)
	if !result.QuorumReached {
		t.Fatalf("quorum not reached: %+v", result)
	}
	if result.CandidateID != "b" {
		t.Fatalf("candidate=%q want=b", result.CandidateID)
	}
}

func TestFailoverElectionUsesPriorityAndStableTieBreak(t *testing.T) {
	result := evaluateFailoverElection([]failoverObservation{
		{NodeID: "z", MasterDown: true, Eligible: true, Offset: 120, Priority: 100},
		{NodeID: "b", MasterDown: true, Eligible: true, Offset: 120, Priority: 50},
		{NodeID: "a", MasterDown: true, Eligible: true, Offset: 120, Priority: 50},
	}, 2)
	if result.CandidateID != "a" {
		t.Fatalf("candidate=%q want=a", result.CandidateID)
	}
}

func TestFailoverElectionPriorityZeroIsIneligible(t *testing.T) {
	result := evaluateFailoverElection([]failoverObservation{
		{NodeID: "a", MasterDown: true, Eligible: true, Offset: 200, Priority: 0},
		{NodeID: "b", MasterDown: true, Eligible: true, Offset: 100, Priority: 100},
	}, 2)
	if result.CandidateID != "b" {
		t.Fatalf("candidate=%q want=b", result.CandidateID)
	}
}

func TestFailoverElectionDeduplicatesVotes(t *testing.T) {
	result := evaluateFailoverElection([]failoverObservation{
		{NodeID: "a", MasterDown: true, Eligible: true, Offset: 100, Priority: 100},
		{NodeID: "a", MasterDown: true, Eligible: true, Offset: 100, Priority: 100},
		{NodeID: "b", MasterDown: false, Eligible: true, Offset: 100, Priority: 100},
	}, 2)
	if result.QuorumReached {
		t.Fatalf("duplicate vote counted toward quorum: %+v", result)
	}
	if result.DownVotes != 1 {
		t.Fatalf("down votes=%d want=1", result.DownVotes)
	}
}
