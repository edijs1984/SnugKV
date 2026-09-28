package server

import (
	"sort"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterRebalanceApplyAllCoordinatesMultipleDonors(t *testing.T) {
	nodes := make([]*TCPServer, 0, 3)
	for i := 0; i < 3; i++ {
		tcp, err := Listen("127.0.0.1:0", engine.New())
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, tcp)
		defer tcp.Close()
	}

	byAddr := make(map[string]*TCPServer, 3)
	addrs := make([]string, 0, 3)
	for _, node := range nodes {
		addr := node.listener.Addr().String()
		addrs = append(addrs, addr)
		byAddr[addr] = node
	}
	sort.Strings(addrs)

	// For three owners the deterministic targets are 5462, 5461, 5461.
	// Give the first two owners one surplus slot each and the third a deficit
	// of two, producing exactly two planned slot moves from two donors.
	ranges := map[string]string{
		"0-5462":      addrs[0], // 5463 (+1)
		"5463-10924":  addrs[1], // 5462 (+1)
		"10925-16383": addrs[2], // 5459 (-2)
	}
	for addr, node := range byAddr {
		if err := node.server.configureClusterSlots(true, addr, ranges); err != nil {
			t.Fatal(err)
		}
	}

	coordinator := byAddr[addrs[2]].server
	state := coordinator.clusterStateSnapshot()
	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}

	totalSlots := 0
	donors := map[string]bool{}
	for _, move := range moves {
		totalSlots += move.End - move.Start + 1
		donors[move.Source] = true
	}
	if totalSlots != 2 || len(donors) != 2 {
		t.Fatalf("plan=%+v totalSlots=%d donors=%d, want two slots from two donors", moves, totalSlots, len(donors))
	}

	for _, move := range moves {
		for slot := move.Start; slot <= move.End; slot++ {
			key := findClusterTestKeyForSlot(slot)
			if key == "" {
				t.Fatalf("failed to find key for slot %d", slot)
			}
			donor := byAddr[move.Source].server
			if _, err := donor.execute([][]byte{
				[]byte("SET"), []byte(key), []byte("value"),
			}); err != nil {
				t.Fatalf("seed donor %s slot %d: %v", move.Source, slot, err)
			}
		}
	}

	planID := clusterRebalancePlanID(state, moves)
	got, err := coordinator.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("ALL"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, token := range []string{"status", "complete", "slots_moved", ":2\r\n"} {
		if !strings.Contains(text, token) {
			t.Fatalf("response missing %q: %q", token, text)
		}
	}

	for _, node := range nodes {
		got := node.server.clusterStateSnapshot()
		counts := map[string]int{}
		for _, owner := range got.owners {
			counts[owner]++
		}
		if counts[addrs[0]] != 5462 || counts[addrs[1]] != 5461 || counts[addrs[2]] != 5461 {
			t.Fatalf("node %s counts=%v", node.listener.Addr(), counts)
		}
		if clusterRebalanceHasActiveTransition(got) {
			t.Fatalf("node %s still has active transition", node.listener.Addr())
		}
	}

	finalState := coordinator.clusterStateSnapshot()
	finalMoves, err := planClusterRebalance(finalState)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalMoves) != 0 {
		t.Fatalf("cluster still unbalanced: %+v", finalMoves)
	}
}

func TestClusterRebalanceExecuteRejectsWrongOwnerAndTarget(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("EXECUTE"),
		[]byte("9000"), []byte(clusterNodeID(a)),
	})
	if err == nil || err.Error() != "ERR rebalance execute slot is not owned by this node" {
		t.Fatalf("wrong-owner err=%v", err)
	}

	_, err = s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("EXECUTE"),
		[]byte("100"), []byte(clusterNodeID(a)),
	})
	if err == nil || err.Error() != "ERR rebalance execute target is invalid" {
		t.Fatalf("invalid-target err=%v", err)
	}
}
