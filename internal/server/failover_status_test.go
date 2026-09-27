package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestFailoverHealthReportsReachableQuorum(t *testing.T) {
	const group = "cluster-health"

	local, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	peer, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	localAddr := local.listener.Addr().String()
	peerAddr := peer.listener.Addr().String()
	configureDynamicMembershipNode(t, local, group, 3, []string{peerAddr}, 2)
	configureDynamicMembershipNode(t, peer, group, 3, []string{localAddr}, 2)

	health := local.server.failoverHealth(time.Now())
	if health.Status != "healthy" {
		t.Fatalf("health=%+v", health)
	}
	if !health.QuorumReachable || health.ReachableVoters != 2 {
		t.Fatalf("health=%+v", health)
	}
	if len(health.Peers) != 1 || !health.Peers[0].Reachable {
		t.Fatalf("peers=%+v", health.Peers)
	}
}

func TestFailoverHealthReportsDegradedWithoutQuorum(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-health"
	s.failoverConfigEpoch = 3
	s.failoverAdvertiseAddr = "127.0.0.1:7001"
	s.failoverPeers = []string{"127.0.0.1:1", "127.0.0.1:2"}
	s.failoverQuorum = 2

	health := s.failoverHealth(time.Now())
	if health.Status != "degraded" {
		t.Fatalf("health=%+v", health)
	}
	if health.QuorumReachable {
		t.Fatalf("unexpected quorum: %+v", health)
	}
	if health.ReachableVoters != 1 {
		t.Fatalf("reachable=%d want=1", health.ReachableVoters)
	}
}

func TestFailoverTopologyReportsPendingTransitionAndDiscovery(t *testing.T) {
	now := time.Now()
	s := New(engine.New())
	s.failoverGroupID = "cluster-topology"
	s.failoverConfigEpoch = 5
	s.failoverAdvertiseAddr = "127.0.0.1:7001"
	s.failoverPeers = []string{"127.0.0.1:7002"}
	s.failoverQuorum = 2
	s.failoverJointActive = true
	s.failoverPendingEpoch = 6
	s.failoverPendingPeers = []string{"127.0.0.1:7002", "127.0.0.1:7003"}
	s.failoverPendingQuorum = 2
	s.failoverDiscoveryInterval = time.Second
	s.failoverDiscoveryMu.Lock()
	s.failoverDiscovered["127.0.0.1:7004"] = failoverDiscoveredPeer{
		Address:      "127.0.0.1:7004",
		LastSeenUnix: now.Unix(),
		State: failoverPeerState{
			AdvertiseAddr: "127.0.0.1:7004",
			GroupID:       "cluster-topology",
			ConfigEpoch:   5,
		},
	}
	s.failoverDiscoveryMu.Unlock()

	topology := s.failoverTopology(now)
	if topology.ConfigEpoch != 5 || !topology.JointActive || topology.PendingEpoch != 6 {
		t.Fatalf("topology=%+v", topology)
	}
	if len(topology.PendingMembers) != 3 {
		t.Fatalf("pending members=%v", topology.PendingMembers)
	}
	if len(topology.Discovered) != 1 || !topology.Discovered[0].Fresh {
		t.Fatalf("discovered=%+v", topology.Discovered)
	}
}

func TestFailoverHealthReportsFencedLeader(t *testing.T) {
	now := time.Now()
	s := New(engine.New())
	s.failoverGroupID = "cluster-health"
	s.failoverConfigEpoch = 1
	s.failoverQuorum = 1
	s.activateFailoverLeader(4, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", s.localFailoverState(now).NodeID, now.Add(-time.Second))

	health := s.failoverHealth(now)
	if health.Status != "fenced" || !health.WriteFenced || !health.LeaderActive {
		t.Fatalf("health=%+v", health)
	}
}

func TestFailoverHealthReportsRetired(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-health"
	s.failoverConfigEpoch = 9
	s.failoverQuorum = 1
	s.failoverRetired = true
	s.failoverRetiredAtEpoch = 10

	health := s.failoverHealth(time.Now())
	if health.Status != "retired" || !health.Retired {
		t.Fatalf("health=%+v", health)
	}
}


func TestFailoverTransitionDiagnosisPreparedIsNotRetryable(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-transition"
	s.failoverConfigEpoch = 4
	s.failoverAdvertiseAddr = "127.0.0.1:7001"
	s.failoverPeers = []string{"127.0.0.1:7002"}
	s.failoverQuorum = 2
	s.failoverJointActive = true
	s.failoverPendingEpoch = 5
	s.failoverPendingPeers = []string{"127.0.0.1:7002"}
	s.failoverPendingQuorum = 2

	d := s.diagnoseFailoverTransition(time.Now())
	if !d.Active || d.Phase != "prepared" || d.CanRetry {
		t.Fatalf("diagnosis=%+v", d)
	}
	if _, err := s.retryFailoverTransitionNow(time.Now()); err == nil {
		t.Fatal("expected retry without durable commit intent to fail")
	}
}

func TestFailoverTransitionRetryCompletesCommitForwardRecovery(t *testing.T) {
	const group = "cluster-transition-retry"

	coordinator, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	peer, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	coordAddr := coordinator.listener.Addr().String()
	peerAddr := peer.listener.Addr().String()

	configureDynamicMembershipNode(t, coordinator, group, 1, []string{peerAddr}, 2)
	configureDynamicMembershipNode(t, peer, group, 1, []string{coordAddr}, 2)

	newMembers := []string{coordAddr, peerAddr}
	if _, err := coordinator.server.prepareFailoverMembershipMembers(group, 1, 2, newMembers, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.server.prepareFailoverMembershipMembers(group, 1, 2, newMembers, 2); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.server.armFailoverMembershipCommitRecovery(
		1, 2, newMembers, 2, newMembers, nil,
	); err != nil {
		t.Fatal(err)
	}

	before := coordinator.server.diagnoseFailoverTransition(time.Now())
	if !before.Active || before.Phase != "commit-forward" || !before.CanRetry {
		t.Fatalf("before=%+v", before)
	}

	after, err := coordinator.server.retryFailoverTransitionNow(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if after.Active {
		t.Fatalf("transition still active: %+v", after)
	}
	if got := coordinator.server.failoverMembershipSnapshot(); got.ConfigEpoch != 2 || got.CommitPending || got.JointActive {
		t.Fatalf("coordinator membership=%+v", got)
	}
	if got := peer.server.failoverMembershipSnapshot(); got.ConfigEpoch != 2 || got.JointActive {
		t.Fatalf("peer membership=%+v", got)
	}
}

func TestFailoverTransitionDiagnosisReportsUnreachableCommitTarget(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-transition"
	s.failoverConfigEpoch = 8
	s.failoverAdvertiseAddr = "127.0.0.1:7001"
	s.failoverPeers = []string{"127.0.0.1:1"}
	s.failoverQuorum = 2
	s.failoverCommitPending = true
	s.failoverCommitOldEpoch = 8
	s.failoverCommitEpoch = 9
	s.failoverCommitMembers = []string{"127.0.0.1:7001", "127.0.0.1:1"}
	s.failoverCommitQuorum = 2
	s.failoverCommitTargets = []string{"127.0.0.1:7001", "127.0.0.1:1"}

	d := s.diagnoseFailoverTransition(time.Now())
	if !d.Active || d.Phase != "commit-forward" || !d.CanRetry {
		t.Fatalf("diagnosis=%+v", d)
	}
	if d.Blocker != "one or more transition targets are unreachable" {
		t.Fatalf("blocker=%q", d.Blocker)
	}
}
