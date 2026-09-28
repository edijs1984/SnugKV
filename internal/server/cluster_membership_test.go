package server

import (
	"path/filepath"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func configureMembershipTestCluster(t *testing.T, nodes map[string]*Server, ranges map[string]string) {
	t.Helper()
	for addr, srv := range nodes {
		if err := srv.configureClusterSlots(true, addr, ranges); err != nil {
			t.Fatalf("configure %s: %v", addr, err)
		}
	}
}

func TestClusterJoinAddsZeroSlotMemberAndRebalancePlansForIt(t *testing.T) {
	aTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer aTCP.Close()
	bTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer bTCP.Close()
	cTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer cTCP.Close()

	a := aTCP.listener.Addr().String()
	b := bTCP.listener.Addr().String()
	c := cTCP.listener.Addr().String()
	ranges := map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}
	configureMembershipTestCluster(t, map[string]*Server{
		a: aTCP.server,
		b: bTCP.server,
		c: cTCP.server,
	}, ranges)

	before, err := planClusterRebalance(aTCP.server.clusterStateSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("pre-join plan=%+v want balanced two-owner topology", before)
	}

	got, err := aTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("JOIN"), []byte(c),
	})
	if err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("JOIN response=%q err=%v", got, err)
	}

	for name, srv := range map[string]*Server{
		"a": aTCP.server,
		"b": bTCP.server,
		"c": cTCP.server,
	} {
		state := srv.clusterStateSnapshot()
		nodes := clusterKnownNodesFromState(state)
		if len(nodes) != 3 {
			t.Fatalf("%s known nodes=%v want 3", name, nodes)
		}
		found := false
		for _, node := range nodes {
			if node == c {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s missing joined node %s: %v", name, c, nodes)
		}
	}

	nodesReply := string(aTCP.server.clusterNodesReply())
	if !strings.Contains(nodesReply, c+"@0") {
		t.Fatalf("CLUSTER NODES missing zero-slot member %s: %q", c, nodesReply)
	}

	after, err := planClusterRebalance(aTCP.server.clusterStateSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) == 0 {
		t.Fatal("expected rebalance plan after zero-slot member joins")
	}
	slotsToC := 0
	for _, move := range after {
		if move.Target == c {
			slotsToC += move.End - move.Start + 1
		}
	}
	if slotsToC == 0 {
		t.Fatalf("plan does not assign slots to joined node: %+v", after)
	}

	// JOIN is idempotent.
	got, err = aTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("JOIN"), []byte(c),
	})
	if err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("second JOIN response=%q err=%v", got, err)
	}
}

func TestClusterJoinRejectsCandidateWithDifferentSlotMap(t *testing.T) {
	aTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer aTCP.Close()
	bTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer bTCP.Close()
	cTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer cTCP.Close()

	a := aTCP.listener.Addr().String()
	b := bTCP.listener.Addr().String()
	c := cTCP.listener.Addr().String()

	if err := aTCP.server.configureClusterSlots(true, a, map[string]string{
		"0-8191": a, "8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}
	if err := bTCP.server.configureClusterSlots(true, b, map[string]string{
		"0-8191": a, "8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}
	if err := cTCP.server.configureClusterSlots(true, c, map[string]string{
		"0-9999": a, "10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = aTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("JOIN"), []byte(c),
	})
	if err == nil || !strings.Contains(err.Error(), "candidate topology does not match") {
		t.Fatalf("err=%v", err)
	}

	if len(clusterKnownNodesFromState(aTCP.server.clusterStateSnapshot())) != 2 {
		t.Fatal("failed JOIN mutated coordinator membership")
	}
}

func TestClusterKnownNodesPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	aofPath := filepath.Join(dir, "appendonly.snug")
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	c := "127.0.0.1:7002"
	ranges := map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}

	first := New(engine.New())
	if err := first.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	clusterTopologyPersistencePaths.Store(first, clusterTopologyPersistencePath(aofPath, ""))
	t.Cleanup(func() { clusterTopologyPersistencePaths.Delete(first) })

	digest := clusterOwnershipDigest(first.clusterStateSnapshot())
	if err := first.addClusterKnownNode(c, digest); err != nil {
		t.Fatal(err)
	}

	restartedTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer restartedTCP.Close()
	if err := restartedTCP.server.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	if err := restartedTCP.ConfigureClusterPersistence(aofPath, ""); err != nil {
		t.Fatal(err)
	}

	nodes := clusterKnownNodesFromState(restartedTCP.server.clusterStateSnapshot())
	if len(nodes) != 3 {
		t.Fatalf("restored known nodes=%v want 3", nodes)
	}
	found := false
	for _, node := range nodes {
		if node == c {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored membership missing %s: %v", c, nodes)
	}
}
