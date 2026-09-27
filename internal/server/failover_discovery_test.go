package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func configureDiscoveryNode(s *Server, group string, epoch uint64, advertise string, peers []string, quorum int) {
	s.failoverGroupID = group
	s.failoverConfigEpoch = epoch
	s.failoverAdvertiseAddr = advertise
	s.failoverPeers = append([]string(nil), peers...)
	s.failoverQuorum = quorum
	s.failoverDiscoveryInterval = time.Millisecond
}

func TestFailoverDiscoveryLearnsTransitiveTopologyWithoutChangingMembership(t *testing.T) {
	const group = "cluster-discovery"

	seed, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()

	transitive, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer transitive.Close()

	seedAddr := seed.listener.Addr().String()
	transitiveAddr := transitive.listener.Addr().String()
	configureDiscoveryNode(seed.server, group, 7, seedAddr, []string{transitiveAddr}, 2)
	configureDiscoveryNode(transitive.server, group, 7, transitiveAddr, []string{seedAddr}, 2)

	local := New(engine.New())
	configureDiscoveryNode(local, group, 7, "127.0.0.1:7999", nil, 0)
	local.failoverDiscoverySeeds = []string{seedAddr}

	before := local.failoverMembershipSnapshot()
	local.refreshFailoverDiscovery(time.Now())
	after := local.failoverMembershipSnapshot()

	if !stringSlicesEqual(before.Peers, after.Peers) || before.Quorum != after.Quorum {
		t.Fatalf("discovery mutated membership: before=%+v after=%+v", before, after)
	}

	discovered := local.discoveredFailoverPeers()
	if len(discovered) != 2 {
		t.Fatalf("discovered=%+v want 2 peers", discovered)
	}
	got := map[string]bool{}
	for _, peer := range discovered {
		got[peer.Address] = true
	}
	if !got[seedAddr] || !got[transitiveAddr] {
		t.Fatalf("missing discovered peers: %+v", discovered)
	}
}

func TestFailoverDiscoveryRejectsMismatchedMembership(t *testing.T) {
	seed, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()

	seedAddr := seed.listener.Addr().String()
	configureDiscoveryNode(seed.server, "other-cluster", 3, seedAddr, nil, 1)

	local := New(engine.New())
	configureDiscoveryNode(local, "cluster-a", 3, "127.0.0.1:7999", nil, 0)
	local.failoverDiscoverySeeds = []string{seedAddr}

	local.refreshFailoverDiscovery(time.Now())
	if got := local.discoveredFailoverPeers(); len(got) != 0 {
		t.Fatalf("mismatched peer discovered: %+v", got)
	}
}

func TestFailoverStateGossipsCommittedMembers(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-a"
	s.failoverConfigEpoch = 9
	s.failoverAdvertiseAddr = "127.0.0.1:7001"
	s.failoverPeers = []string{"127.0.0.1:7002", "127.0.0.1:7003"}
	s.failoverQuorum = 2

	state := s.localFailoverState(time.Now())
	if state.Quorum != 2 {
		t.Fatalf("quorum=%d want=2", state.Quorum)
	}
	if len(state.Members) != 3 {
		t.Fatalf("members=%v", state.Members)
	}
	if !containsString(state.Members, "127.0.0.1:7001") ||
		!containsString(state.Members, "127.0.0.1:7002") ||
		!containsString(state.Members, "127.0.0.1:7003") {
		t.Fatalf("incomplete gossiped membership: %v", state.Members)
	}
}
