package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterFailoverOwnershipConvergesWithoutOldPrimary(t *testing.T) {
	leaderTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leaderTCP.Close()

	observerTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer observerTCP.Close()

	newOwner := leaderTCP.listener.Addr().String()
	observerAddr := observerTCP.listener.Addr().String()
	oldOwner := "127.0.0.1:65530"

	ranges := map[string]string{
		"0-8191":     oldOwner,
		"8192-16383": observerAddr,
	}
	if err := leaderTCP.server.configureClusterSlots(true, newOwner, ranges); err != nil {
		t.Fatal(err)
	}
	if err := observerTCP.server.configureClusterSlots(true, observerAddr, ranges); err != nil {
		t.Fatal(err)
	}

	leaderTCP.server.failoverAdvertiseAddr = newOwner
	leaderTCP.server.failoverPeers = []string{oldOwner}

	if err := leaderTCP.server.convergeClusterFailoverOwnership(); err != nil {
		t.Fatal(err)
	}

	for name, srv := range map[string]*Server{
		"leader":   leaderTCP.server,
		"observer": observerTCP.server,
	} {
		state := srv.clusterStateSnapshot()
		for slot := 0; slot <= 8191; slot++ {
			if state.owners[slot] != newOwner {
				t.Fatalf("%s owner[%d]=%q want=%q", name, slot, state.owners[slot], newOwner)
			}
		}
		for slot := 8192; slot < clusterSlotCount; slot++ {
			if state.owners[slot] != observerAddr {
				t.Fatalf("%s observer range owner[%d]=%q want=%q", name, slot, state.owners[slot], observerAddr)
			}
		}
	}
}

func TestClusterFailoverMemberRepairUpdatesStaleReplicaView(t *testing.T) {
	s := New(engine.New())
	replicaAddr := "127.0.0.1:7002"
	oldOwner := "127.0.0.1:7000"
	newOwner := "127.0.0.1:7001"
	otherOwner := "127.0.0.1:7003"

	if err := s.configureClusterSlots(true, replicaAddr, map[string]string{
		"0-8191":     oldOwner,
		"8192-16383": otherOwner,
	}); err != nil {
		t.Fatal(err)
	}
	s.failoverAdvertiseAddr = replicaAddr
	s.failoverPeers = []string{oldOwner, newOwner}

	if err := s.repairClusterFailoverMemberOwnership(newOwner); err != nil {
		t.Fatal(err)
	}

	state := s.clusterStateSnapshot()
	if state.owners[0] != newOwner || state.owners[8191] != newOwner {
		t.Fatalf("stale shard owner not repaired: first=%q last=%q", state.owners[0], state.owners[8191])
	}
	if state.owners[8192] != otherOwner {
		t.Fatalf("unrelated shard changed: %q", state.owners[8192])
	}
	foundNewOwner := false
	for _, addr := range state.knownNodes {
		if addr == newOwner {
			foundNewOwner = true
			break
		}
	}
	if !foundNewOwner {
		t.Fatalf("promoted owner %q missing from known nodes: %v", newOwner, state.knownNodes)
	}
	if reply := string(s.clusterNodesReply()); !strings.Contains(reply, newOwner+"@0") {
		t.Fatalf("CLUSTER NODES omitted promoted owner %q: %q", newOwner, reply)
	}
}

func TestClusterFailoverOwnershipRejectsIdentityMismatch(t *testing.T) {
	s := New(engine.New())
	clusterAddr := "127.0.0.1:7000"
	advertiseAddr := "127.0.0.1:7001"
	oldOwner := "127.0.0.1:7002"

	if err := s.configureClusterSlots(true, clusterAddr, map[string]string{
		"0-16383": oldOwner,
	}); err != nil {
		t.Fatal(err)
	}
	s.failoverAdvertiseAddr = advertiseAddr
	s.failoverPeers = []string{oldOwner}

	err := s.convergeClusterFailoverOwnership()
	if err == nil || !strings.Contains(err.Error(), "to match failover_advertise_addr") {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterFailoverOwnershipRefusesActiveTransitions(t *testing.T) {
	s := New(engine.New())
	newOwner := "127.0.0.1:7001"
	oldOwner := "127.0.0.1:7000"
	otherOwner := "127.0.0.1:7002"

	if err := s.configureClusterSlots(true, newOwner, map[string]string{
		"0-8191":     oldOwner,
		"8192-16383": otherOwner,
	}); err != nil {
		t.Fatal(err)
	}
	s.failoverAdvertiseAddr = newOwner
	s.failoverPeers = []string{oldOwner}

	s.clusterMu.Lock()
	s.clusterSlotMigrating[10] = newOwner
	s.clusterMu.Unlock()

	err := s.convergeClusterFailoverOwnership()
	if err == nil || !strings.Contains(err.Error(), "slots are migrating or importing") {
		t.Fatalf("err=%v", err)
	}
}
