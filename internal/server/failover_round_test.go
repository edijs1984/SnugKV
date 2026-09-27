package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func newFailoverPeerForRound(t *testing.T, lineage string, offset int64, priority int, term uint64) *TCPServer {
	t.Helper()
	peer, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	now := time.Now()
	setFailoverReplicaState(peer.server, lineage, offset, priority, now.Add(-time.Second))
	peer.server.failoverVoteMu.Lock()
	peer.server.failoverTerm = term
	peer.server.failoverVoteMu.Unlock()
	return peer
}

func TestFailoverElectionRoundWinsWithMajority(t *testing.T) {
	const lineage = "1111111111111111111111111111111111111111"
	now := time.Now()

	peerA := newFailoverPeerForRound(t, lineage, 100, 100, 3)
	peerB := newFailoverPeerForRound(t, lineage, 90, 100, 4)

	local := New(engine.New())
	setFailoverReplicaState(local, lineage, 120, 100, now.Add(-time.Second))
	local.failoverPeers = []string{
		peerA.listener.Addr().String(),
		peerB.listener.Addr().String(),
	}
	local.failoverQuorum = 2

	result, err := local.runFailoverElectionRound(now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.QuorumReached || !result.Won {
		t.Fatalf("round=%+v", result)
	}
	if result.CandidateID != local.replication.runID {
		t.Fatalf("candidate=%q want local=%q", result.CandidateID, local.replication.runID)
	}
	if result.Term != 5 {
		t.Fatalf("term=%d want=5", result.Term)
	}
	if result.Votes < 2 {
		t.Fatalf("votes=%d want>=2", result.Votes)
	}
}

func TestFailoverElectionRoundNonCandidateDoesNotRequestVotes(t *testing.T) {
	const lineage = "2222222222222222222222222222222222222222"
	now := time.Now()

	peerA := newFailoverPeerForRound(t, lineage, 200, 100, 7)
	peerB := newFailoverPeerForRound(t, lineage, 90, 100, 7)

	local := New(engine.New())
	setFailoverReplicaState(local, lineage, 100, 100, now.Add(-time.Second))
	local.failoverPeers = []string{
		peerA.listener.Addr().String(),
		peerB.listener.Addr().String(),
	}
	local.failoverQuorum = 2

	result, err := local.runFailoverElectionRound(now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.QuorumReached {
		t.Fatalf("expected quorum: %+v", result)
	}
	if result.CandidateID != peerA.server.replication.runID {
		t.Fatalf("candidate=%q want peerA=%q", result.CandidateID, peerA.server.replication.runID)
	}
	if result.Term != 0 || result.Votes != 0 || result.Won {
		t.Fatalf("non-candidate started election: %+v", result)
	}
}

func TestFailoverElectionRoundStartsAboveHighestObservedTerm(t *testing.T) {
	const lineage = "3333333333333333333333333333333333333333"
	now := time.Now()

	peerA := newFailoverPeerForRound(t, lineage, 90, 100, 50)
	peerB := newFailoverPeerForRound(t, lineage, 80, 100, 12)

	local := New(engine.New())
	setFailoverReplicaState(local, lineage, 100, 100, now.Add(-time.Second))
	local.failoverPeers = []string{
		peerA.listener.Addr().String(),
		peerB.listener.Addr().String(),
	}
	local.failoverQuorum = 2

	result, err := local.runFailoverElectionRound(now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Won {
		t.Fatalf("round did not win: %+v", result)
	}
	if result.Term != 51 {
		t.Fatalf("term=%d want=51", result.Term)
	}
}
