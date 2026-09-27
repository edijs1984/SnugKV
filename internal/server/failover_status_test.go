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
