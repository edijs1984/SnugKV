package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterRemovePlanEvacuatesOnlyTargetNode(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	c := "127.0.0.1:7002"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8190":      a,
		"8191-16381":  b,
		"16382-16383": c,
	}); err != nil {
		t.Fatal(err)
	}

	state := s.clusterStateSnapshot()
	moves, err := planClusterNodeRemoval(state, c)
	if err != nil {
		t.Fatal(err)
	}
	slots := 0
	for _, move := range moves {
		if move.Source != c {
			t.Fatalf("unexpected source in removal plan: %+v", move)
		}
		if move.Target == c {
			t.Fatalf("removal plan targets retiring node: %+v", move)
		}
		slots += move.End - move.Start + 1
	}
	if slots != 2 {
		t.Fatalf("slots=%d want 2, moves=%+v", slots, moves)
	}
}

func TestClusterRemoveApplyEvacuatesAndRemovesMembership(t *testing.T) {
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
		"0-8190":      a,
		"8191-16381":  b,
		"16382-16383": c,
	}
	configureMembershipTestCluster(t, map[string]*Server{
		a: aTCP.server,
		b: bTCP.server,
		c: cTCP.server,
	}, ranges)

	for _, slot := range []int{16382, 16383} {
		key := findClusterTestKeyForSlot(slot)
		if key == "" {
			t.Fatalf("no key for slot %d", slot)
		}
		if _, err := cTCP.server.execute([][]byte{
			[]byte("SET"), []byte(key), []byte("value"),
		}); err != nil {
			t.Fatalf("seed slot %d: %v", slot, err)
		}
	}

	state := aTCP.server.clusterStateSnapshot()
	moves, err := planClusterNodeRemoval(state, c)
	if err != nil {
		t.Fatal(err)
	}
	planID := clusterRemovalPlanID(state, c, moves)

	got, err := aTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("REMOVE"), []byte(c),
		[]byte("APPLY"), []byte(planID),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, token := range []string{"status", "removed", "slots_moved", ":2\r\n"} {
		if !strings.Contains(text, token) {
			t.Fatalf("response missing %q: %q", token, text)
		}
	}

	for name, srv := range map[string]*Server{
		"a": aTCP.server,
		"b": bTCP.server,
		"c": cTCP.server,
	} {
		final := srv.clusterStateSnapshot()
		for slot, owner := range final.owners {
			if owner == c {
				t.Fatalf("%s still assigns slot %d to removed node", name, slot)
			}
		}
		nodes := clusterKnownNodesFromState(final)
		for _, node := range nodes {
			if node == c {
				t.Fatalf("%s still knows removed node: %v", name, nodes)
			}
		}
		if clusterRebalanceHasActiveTransition(final) {
			t.Fatalf("%s has active transition after removal", name)
		}
	}

	if strings.Contains(string(aTCP.server.clusterNodesReply()), c+"@0") {
		t.Fatalf("coordinator CLUSTER NODES still contains removed node %s", c)
	}
}

func TestClusterRemoveZeroSlotMemberDoesNotMigrate(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	c := "127.0.0.1:7002"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}
	digest := clusterOwnershipDigest(s.clusterStateSnapshot())
	if err := s.addClusterKnownNode(c, digest); err != nil {
		t.Fatal(err)
	}

	moves, err := planClusterNodeRemoval(s.clusterStateSnapshot(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 0 {
		t.Fatalf("moves=%+v want none", moves)
	}
}

func TestClusterRemoveRejectsLastOrUnknownNode(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-16383": a,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := planClusterNodeRemoval(s.clusterStateSnapshot(), a); err == nil ||
		!strings.Contains(err.Error(), "cannot remove the last node") {
		t.Fatalf("last-node err=%v", err)
	}
	if _, err := planClusterNodeRemoval(s.clusterStateSnapshot(), "127.0.0.1:7009"); err == nil ||
		!strings.Contains(err.Error(), "not a known node") {
		t.Fatalf("unknown-node err=%v", err)
	}
}

func TestClusterRemoveRefusesActiveTransition(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}
	s.clusterMu.Lock()
	s.clusterSlotMigrating[10] = b
	s.clusterMu.Unlock()

	_, err := planClusterNodeRemoval(s.clusterStateSnapshot(), b)
	if err == nil || !strings.Contains(err.Error(), "slots are migrating or importing") {
		t.Fatalf("err=%v", err)
	}
}
