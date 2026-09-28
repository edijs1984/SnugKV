package server

import (
	"path/filepath"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterMembershipRecoverAddsMissedJoinMember(t *testing.T) {
	aTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer aTCP.Close()
	bTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer bTCP.Close()
	cTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer cTCP.Close()

	a := aTCP.listener.Addr().String()
	b := bTCP.listener.Addr().String()
	c := cTCP.listener.Addr().String()
	ranges := map[string]string{"0-8191": a, "8192-16383": b}
	configureMembershipTestCluster(t, map[string]*Server{
		a: aTCP.server, b: bTCP.server, c: cTCP.server,
	}, ranges)

	digest := clusterOwnershipDigest(aTCP.server.clusterStateSnapshot())
	if err := bTCP.server.addClusterKnownNode(c, digest); err != nil {
		t.Fatal(err)
	}
	if err := cTCP.server.addClusterKnownNode(c, digest); err != nil {
		t.Fatal(err)
	}

	plan, err := aTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("MEMBERSHIP"), []byte("RECOVER"),
		[]byte(b), []byte("PLAN"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan), c) {
		t.Fatalf("PLAN does not show missed member %s: %q", c, plan)
	}

	state := aTCP.server.clusterStateSnapshot()
	view := clusterMembershipView{
		NodeAddr: b,
		Digest: digest,
		Nodes: clusterKnownNodesFromState(bTCP.server.clusterStateSnapshot()),
	}
	planID := clusterMembershipRecoveryPlanID(state, b, view)

	got, err := aTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("MEMBERSHIP"), []byte("RECOVER"),
		[]byte(b), []byte("APPLY"), []byte(planID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "recovered") {
		t.Fatalf("APPLY=%q", got)
	}

	nodes := clusterKnownNodesFromState(aTCP.server.clusterStateSnapshot())
	if len(nodes) != 3 {
		t.Fatalf("nodes=%v want 3", nodes)
	}
	found := false
	for _, node := range nodes {
		if node == c { found = true }
	}
	if !found {
		t.Fatalf("recovered membership missing %s: %v", c, nodes)
	}
}

func TestClusterMembershipRecoverRemovesStaleEvacuatedMember(t *testing.T) {
	aTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer aTCP.Close()
	bTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer bTCP.Close()

	a := aTCP.listener.Addr().String()
	b := bTCP.listener.Addr().String()
	c := "127.0.0.1:7009"
	ranges := map[string]string{"0-8191": a, "8192-16383": b}
	configureMembershipTestCluster(t, map[string]*Server{
		a: aTCP.server, b: bTCP.server,
	}, ranges)

	digest := clusterOwnershipDigest(aTCP.server.clusterStateSnapshot())
	if err := aTCP.server.addClusterKnownNode(c, digest); err != nil {
		t.Fatal(err)
	}

	state := aTCP.server.clusterStateSnapshot()
	view := clusterMembershipView{
		NodeAddr: b,
		Digest: digest,
		Nodes: clusterKnownNodesFromState(bTCP.server.clusterStateSnapshot()),
	}
	planID := clusterMembershipRecoveryPlanID(state, b, view)

	got, err := aTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("MEMBERSHIP"), []byte("RECOVER"),
		[]byte(b), []byte("APPLY"), []byte(planID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "recovered") {
		t.Fatalf("APPLY=%q", got)
	}
	for _, node := range clusterKnownNodesFromState(aTCP.server.clusterStateSnapshot()) {
		if node == c {
			t.Fatalf("stale member still present: %v", clusterKnownNodesFromState(aTCP.server.clusterStateSnapshot()))
		}
	}
}

func TestClusterMembershipRecoverRejectsDifferentOwnership(t *testing.T) {
	aTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer aTCP.Close()
	bTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer bTCP.Close()

	a := aTCP.listener.Addr().String()
	b := bTCP.listener.Addr().String()
	if err := aTCP.server.configureClusterSlots(true, a, map[string]string{
		"0-8191": a, "8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}
	if err := bTCP.server.configureClusterSlots(true, b, map[string]string{
		"0-9999": a, "10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = aTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("MEMBERSHIP"), []byte("RECOVER"),
		[]byte(b), []byte("PLAN"),
	})
	if err == nil || !strings.Contains(err.Error(), "topology does not match") {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterMembershipRecoverCannotDropSlotOwner(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8191": a, "8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}
	state := s.clusterStateSnapshot()
	view := clusterMembershipView{
		NodeAddr: a,
		Digest: clusterOwnershipDigest(state),
		Nodes: []string{a},
	}
	err := validateClusterMembershipView(state, a, view)
	if err == nil || !strings.Contains(err.Error(), "cannot remove slot owner") {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterMembershipRecoveryAfterRestart(t *testing.T) {
	dir := t.TempDir()
	aofPath := filepath.Join(dir, "appendonly.snug")

	peerTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer peerTCP.Close()

	a := "127.0.0.1:7000"
	b := peerTCP.listener.Addr().String()
	c := "127.0.0.1:7002"
	ranges := map[string]string{"0-8191": a, "8192-16383": b}

	stale := New(engine.New())
	if err := stale.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	clusterTopologyPersistencePaths.Store(stale, clusterTopologyPersistencePath(aofPath, ""))
	t.Cleanup(func() { clusterTopologyPersistencePaths.Delete(stale) })

	digest := clusterOwnershipDigest(stale.clusterStateSnapshot())
	if err := stale.addClusterKnownNode(c, digest); err != nil {
		t.Fatal(err)
	}

	if err := peerTCP.server.configureClusterSlots(true, b, ranges); err != nil {
		t.Fatal(err)
	}

	restartedTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer restartedTCP.Close()
	if err := restartedTCP.server.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	if err := restartedTCP.ConfigureClusterPersistence(aofPath, ""); err != nil {
		t.Fatal(err)
	}

	before := clusterKnownNodesFromState(restartedTCP.server.clusterStateSnapshot())
	foundStale := false
	for _, node := range before {
		if node == c { foundStale = true }
	}
	if !foundStale {
		t.Fatalf("restart did not restore stale member: %v", before)
	}

	state := restartedTCP.server.clusterStateSnapshot()
	view := clusterMembershipView{
		NodeAddr: b,
		Digest: clusterOwnershipDigest(state),
		Nodes: clusterKnownNodesFromState(peerTCP.server.clusterStateSnapshot()),
	}
	planID := clusterMembershipRecoveryPlanID(state, b, view)

	if _, err := restartedTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("MEMBERSHIP"), []byte("RECOVER"),
		[]byte(b), []byte("APPLY"), []byte(planID),
	}); err != nil {
		t.Fatal(err)
	}

	for _, node := range clusterKnownNodesFromState(restartedTCP.server.clusterStateSnapshot()) {
		if node == c {
			t.Fatalf("stale member survived recovery: %v", clusterKnownNodesFromState(restartedTCP.server.clusterStateSnapshot()))
		}
	}
}
