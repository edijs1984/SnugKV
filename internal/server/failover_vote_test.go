package server

import (
	"path/filepath"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func prepareVotingReplica(t *testing.T, s *Server, runID string, offset int64) {
	t.Helper()
	s.autoFailoverTimeout = 10 * time.Millisecond
	s.failoverPriority = 100
	s.replication.setReplica("127.0.0.1", 6390)
	s.replication.mu.Lock()
	s.replication.masterRunID = runID
	s.replication.offset = offset
	s.replication.masterLinkStatus = "down"
	s.replication.masterSyncInProgress = false
	s.replication.masterDownSince = time.Now().Add(-time.Second)
	s.replication.mu.Unlock()
}

func TestFailoverVoteRejectsSecondCandidateInSameTerm(t *testing.T) {
	s := New(engine.New())
	const lineage = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	prepareVotingReplica(t, s, lineage, 100)

	first, err := s.requestFailoverVote(time.Now(), lineage, 7, "candidate-a", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Granted || first.Term != 7 {
		t.Fatalf("first vote=%+v", first)
	}

	second, err := s.requestFailoverVote(time.Now(), lineage, 7, "candidate-b", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if second.Granted {
		t.Fatalf("second candidate granted in same term: %+v", second)
	}

	repeat, err := s.requestFailoverVote(time.Now(), lineage, 7, "candidate-a", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !repeat.Granted {
		t.Fatalf("repeat vote for same candidate should be idempotent: %+v", repeat)
	}
}

func TestFailoverVoteHigherTermCanReplacePriorVote(t *testing.T) {
	s := New(engine.New())
	const lineage = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	prepareVotingReplica(t, s, lineage, 100)

	if reply, err := s.requestFailoverVote(time.Now(), lineage, 3, "candidate-a", 100, 100); err != nil || !reply.Granted {
		t.Fatalf("term 3 vote reply=%+v err=%v", reply, err)
	}
	reply, err := s.requestFailoverVote(time.Now(), lineage, 4, "candidate-b", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Granted || reply.Term != 4 {
		t.Fatalf("higher-term vote=%+v", reply)
	}
}

func TestFailoverVoteRejectsStaleCandidate(t *testing.T) {
	s := New(engine.New())
	const lineage = "cccccccccccccccccccccccccccccccccccccccc"
	prepareVotingReplica(t, s, lineage, 200)

	reply, err := s.requestFailoverVote(time.Now(), lineage, 1, "candidate", 199, 100)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Granted {
		t.Fatalf("stale candidate unexpectedly granted: %+v", reply)
	}
}

func TestFailoverVotePersistsAcrossRestart(t *testing.T) {
	const lineage = "dddddddddddddddddddddddddddddddddddddddd"
	path := filepath.Join(t.TempDir(), "replication-state")

	first := New(engine.New())
	prepareVotingReplica(t, first, lineage, 100)
	replicationPersistencePaths.Store(first, path)
	t.Cleanup(func() { replicationPersistencePaths.Delete(first) })

	reply, err := first.requestFailoverVote(time.Now(), lineage, 11, "candidate-a", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Granted {
		t.Fatalf("initial vote not granted: %+v", reply)
	}

	second := New(engine.New())
	prepareVotingReplica(t, second, lineage, 100)
	if err := second.loadFailoverVoteState(path); err != nil {
		t.Fatal(err)
	}
	restored, err := second.requestFailoverVote(time.Now(), lineage, 11, "candidate-b", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Granted {
		t.Fatalf("restart allowed second vote in same term: %+v", restored)
	}
}

func TestFailoverVoteRPC(t *testing.T) {
	peer, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	const lineage = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	prepareVotingReplica(t, peer.server, lineage, 100)

	reply, err := queryFailoverVote(
		peer.listener.Addr().String(),
		time.Second,
		"",
		"",
		lineage,
		5,
		"candidate-a",
		100,
		100,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Granted || reply.Term != 5 {
		t.Fatalf("vote RPC reply=%+v", reply)
	}
}
