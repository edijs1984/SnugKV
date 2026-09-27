package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
)

func configureDynamicMembershipNode(t *testing.T, srv *TCPServer, group string, epoch uint64, peers []string, quorum int) {
	t.Helper()
	srv.server.failoverGroupID = group
	srv.server.failoverConfigEpoch = epoch
	srv.server.failoverAdvertiseAddr = srv.listener.Addr().String()
	srv.server.failoverPeers = append([]string(nil), peers...)
	srv.server.failoverQuorum = quorum
	path := filepath.Join(t.TempDir(), "replication-state")
	replicationPersistencePaths.Store(srv.server, path)
	t.Cleanup(func() { replicationPersistencePaths.Delete(srv.server) })
}

func TestCoordinateFailoverMembershipChangeRequiresOldAndNewMajorities(t *testing.T) {
	const group = "cluster-a"

	coordinator, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()

	oldA, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer oldA.Close()

	oldB, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer oldB.Close()

	newC, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer newC.Close()

	coordAddr := coordinator.listener.Addr().String()
	oldAAddr := oldA.listener.Addr().String()
	oldBAddr := oldB.listener.Addr().String()
	newCAddr := newC.listener.Addr().String()

	configureDynamicMembershipNode(t, coordinator, group, 1, []string{oldAAddr, oldBAddr}, 2)
	configureDynamicMembershipNode(t, oldA, group, 1, []string{coordAddr, oldBAddr}, 2)
	configureDynamicMembershipNode(t, oldB, group, 1, []string{coordAddr, oldAAddr}, 2)
	configureDynamicMembershipNode(t, newC, group, 1, []string{coordAddr, oldAAddr}, 2)

	newMembers := []string{coordAddr, oldAAddr, newCAddr}
	result, err := coordinator.server.coordinateFailoverMembershipChange(2, newMembers, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Prepared || !result.Committed {
		t.Fatalf("change result=%+v", result)
	}
	if result.OldAcks < 2 || result.NewAcks < 2 {
		t.Fatalf("insufficient dual quorum: %+v", result)
	}

	for name, srv := range map[string]*TCPServer{
		"coordinator": coordinator,
		"oldA": oldA,
		"newC": newC,
	} {
		state := srv.server.failoverMembershipSnapshot()
		if state.ConfigEpoch != 2 || state.JointActive {
			t.Fatalf("%s membership=%+v", name, state)
		}
	}
}

func TestCoordinateFailoverMembershipChangeAbortsWithoutOldMajority(t *testing.T) {
	const group = "cluster-b"

	coordinator, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()

	newA, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer newA.Close()

	newB, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer newB.Close()

	coordAddr := coordinator.listener.Addr().String()
	newAAddr := newA.listener.Addr().String()
	newBAddr := newB.listener.Addr().String()

	// The current membership expects two old peers, both unavailable.
	configureDynamicMembershipNode(
		t,
		coordinator,
		group,
		4,
		[]string{"127.0.0.1:1", "127.0.0.1:2"},
		2,
	)
	configureDynamicMembershipNode(t, newA, group, 4, []string{coordAddr, newBAddr}, 2)
	configureDynamicMembershipNode(t, newB, group, 4, []string{coordAddr, newAAddr}, 2)

	newMembers := []string{coordAddr, newAAddr, newBAddr}
	result, err := coordinator.server.coordinateFailoverMembershipChange(5, newMembers, 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Committed {
		t.Fatalf("membership committed without old majority: %+v", result)
	}
	if result.OldAcks >= result.OldQuorum {
		t.Fatalf("unexpected old quorum: %+v", result)
	}
	if result.NewAcks < result.NewQuorum {
		t.Fatalf("expected new majority to prove old-majority gate: %+v", result)
	}

	state := coordinator.server.failoverMembershipSnapshot()
	if state.ConfigEpoch != 4 || state.JointActive {
		t.Fatalf("coordinator membership changed after abort: %+v", state)
	}
}

func TestDynamicMembershipDerivesPeersFromCanonicalMembers(t *testing.T) {
	s := New(engine.New())
	s.failoverAdvertiseAddr = "127.0.0.1:7001"
	peers, err := deriveFailoverPeersForMember(
		[]string{"127.0.0.1:7001", "127.0.0.1:7002", "127.0.0.1:7003"},
		s.failoverAdvertiseAddr,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 || peers[0] != "127.0.0.1:7002" || peers[1] != "127.0.0.1:7003" {
		t.Fatalf("peers=%v", peers)
	}
}
