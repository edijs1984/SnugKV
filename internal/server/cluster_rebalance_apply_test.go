package server

import (
	"fmt"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func findClusterTestKeyForSlot(slot int) string {
	for i := 0; i < 1000000; i++ {
		key := fmt.Sprintf("rebalance-key-%d", i)
		if clusterKeySlot([]byte(key)) == slot {
			return key
		}
	}
	return ""
}

func TestClusterRebalanceApplyOnceMovesSingleSlotEndToEnd(t *testing.T) {
	sourceTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer sourceTCP.Close()

	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	sourceAddr := sourceTCP.listener.Addr().String()
	targetAddr := targetTCP.listener.Addr().String()

	ranges := map[string]string{
		"0-9999":      sourceAddr,
		"10000-16383": targetAddr,
	}
	if err := sourceTCP.server.configureClusterSlots(true, sourceAddr, ranges); err != nil {
		t.Fatal(err)
	}
	if err := targetTCP.server.configureClusterSlots(true, targetAddr, ranges); err != nil {
		t.Fatal(err)
	}

	state := sourceTCP.server.clusterStateSnapshot()
	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) == 0 {
		t.Fatal("expected rebalance moves")
	}
	if moves[0].Source != sourceAddr || moves[0].Target != targetAddr {
		t.Fatalf("first move=%+v source=%s target=%s", moves[0], sourceAddr, targetAddr)
	}

	slot := moves[0].Start
	key := findClusterTestKeyForSlot(slot)
	if key == "" {
		t.Fatalf("failed to find key for slot %d", slot)
	}
	if _, err := sourceTCP.server.execute([][]byte{
		[]byte("SET"), []byte(key), []byte("value"),
	}); err != nil {
		t.Fatalf("seed source key: %v", err)
	}

	planID := clusterRebalancePlanID(state, moves)
	got, err := sourceTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("ONCE"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, token := range []string{
		"status", "moved", "slot", "source_addr", sourceAddr,
		"target_addr", targetAddr, "slots_moved", "keys_moved",
	} {
		if !strings.Contains(text, token) {
			t.Fatalf("response missing %q: %q", token, text)
		}
	}

	sourceState := sourceTCP.server.clusterStateSnapshot()
	targetState := targetTCP.server.clusterStateSnapshot()
	if sourceState.owners[slot] != targetAddr {
		t.Fatalf("source owner[%d]=%q want=%q", slot, sourceState.owners[slot], targetAddr)
	}
	if targetState.owners[slot] != targetAddr {
		t.Fatalf("target owner[%d]=%q want=%q", slot, targetState.owners[slot], targetAddr)
	}
	if sourceState.migrating[slot] != "" || sourceState.importing[slot] != "" {
		t.Fatalf("source transition not cleared: migrating=%q importing=%q",
			sourceState.migrating[slot], sourceState.importing[slot])
	}
	if targetState.migrating[slot] != "" || targetState.importing[slot] != "" {
		t.Fatalf("target transition not cleared: migrating=%q importing=%q",
			targetState.migrating[slot], targetState.importing[slot])
	}

	if sourceTCP.server.store.Exists([]string{key}) != 0 {
		t.Fatalf("source still contains migrated key %q", key)
	}
	gotValue, err := targetTCP.server.execute([][]byte{
		[]byte("GET"), []byte(key),
	})
	if err != nil {
		t.Fatalf("target GET after rebalance: %v", err)
	}
	if string(gotValue) != "$5\r\nvalue\r\n" {
		t.Fatalf("target value=%q", gotValue)
	}

	_, err = sourceTCP.server.execute([][]byte{
		[]byte("GET"), []byte(key),
	})
	wantMoved := fmt.Sprintf("MOVED %d %s", slot, targetAddr)
	if err == nil || err.Error() != wantMoved {
		t.Fatalf("source GET err=%v want=%q", err, wantMoved)
	}
}

func TestClusterRebalanceApplyOnceRequiresSourceCoordinator(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, b, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	state := s.clusterStateSnapshot()
	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) == 0 || moves[0].Source != a {
		t.Fatalf("unexpected plan: %+v", moves)
	}
	planID := clusterRebalancePlanID(state, moves)

	_, err = s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("ONCE"),
	})
	want := "ERR REBALANCE APPLY ONCE must run on source node " + a
	if err == nil || err.Error() != want {
		t.Fatalf("err=%v want=%q", err, want)
	}
}


func TestClusterRebalanceApplyOnceConvergesThirdNodeTopology(t *testing.T) {
	sourceTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer sourceTCP.Close()

	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	observerTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer observerTCP.Close()

	sourceAddr := sourceTCP.listener.Addr().String()
	targetAddr := targetTCP.listener.Addr().String()
	observerAddr := observerTCP.listener.Addr().String()

	ranges := map[string]string{
		"0-8999":      sourceAddr,
		"9000-13999":  targetAddr,
		"14000-16383": observerAddr,
	}
	nodes := []struct {
		server *Server
		addr   string
	}{
		{sourceTCP.server, sourceAddr},
		{targetTCP.server, targetAddr},
		{observerTCP.server, observerAddr},
	}
	for _, node := range nodes {
		if err := node.server.configureClusterSlots(true, node.addr, ranges); err != nil {
			t.Fatal(err)
		}
	}

	state := sourceTCP.server.clusterStateSnapshot()
	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) == 0 {
		t.Fatal("expected rebalance moves")
	}
	if moves[0].Source != sourceAddr {
		t.Fatalf("first move source=%q want=%q plan=%+v", moves[0].Source, sourceAddr, moves)
	}

	slot := moves[0].Start
	key := findClusterTestKeyForSlot(slot)
	if key == "" {
		t.Fatalf("failed to find key for slot %d", slot)
	}
	if _, err := sourceTCP.server.execute([][]byte{
		[]byte("SET"), []byte(key), []byte("value"),
	}); err != nil {
		t.Fatalf("seed source key: %v", err)
	}

	planID := clusterRebalancePlanID(state, moves)
	if _, err := sourceTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("ONCE"),
	}); err != nil {
		t.Fatal(err)
	}

	for name, srv := range map[string]*Server{
		"source":   sourceTCP.server,
		"target":   targetTCP.server,
		"observer": observerTCP.server,
	} {
		got := srv.clusterStateSnapshot()
		if got.owners[slot] != moves[0].Target {
			t.Fatalf("%s owner[%d]=%q want=%q", name, slot, got.owners[slot], moves[0].Target)
		}
		if got.migrating[slot] != "" || got.importing[slot] != "" {
			t.Fatalf("%s transition not cleared: migrating=%q importing=%q",
				name, got.migrating[slot], got.importing[slot])
		}
	}
}


func TestClusterRebalanceApplyBatchMovesMultipleSlots(t *testing.T) {
	sourceTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer sourceTCP.Close()

	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	sourceAddr := sourceTCP.listener.Addr().String()
	targetAddr := targetTCP.listener.Addr().String()

	ranges := map[string]string{
		"0-9999":      sourceAddr,
		"10000-16383": targetAddr,
	}
	if err := sourceTCP.server.configureClusterSlots(true, sourceAddr, ranges); err != nil {
		t.Fatal(err)
	}
	if err := targetTCP.server.configureClusterSlots(true, targetAddr, ranges); err != nil {
		t.Fatal(err)
	}

	state := sourceTCP.server.clusterStateSnapshot()
	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) == 0 || moves[0].End-moves[0].Start < 1 {
		t.Fatalf("expected a move range with at least two slots: %+v", moves)
	}

	slot1 := moves[0].Start
	slot2 := moves[0].Start + 1
	key1 := findClusterTestKeyForSlot(slot1)
	key2 := findClusterTestKeyForSlot(slot2)
	if key1 == "" || key2 == "" {
		t.Fatalf("failed to find test keys for slots %d and %d", slot1, slot2)
	}

	for _, tc := range []struct {
		key   string
		value string
	}{
		{key1, "one"},
		{key2, "two"},
	} {
		if _, err := sourceTCP.server.execute([][]byte{
			[]byte("SET"), []byte(tc.key), []byte(tc.value),
		}); err != nil {
			t.Fatalf("seed %q: %v", tc.key, err)
		}
	}

	planID := clusterRebalancePlanID(state, moves)
	got, err := sourceTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("BATCH"), []byte("2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, token := range []string{"status", "moved", "slots_moved", ":2\r\n", "keys_moved"} {
		if !strings.Contains(text, token) {
			t.Fatalf("batch response missing %q: %q", token, text)
		}
	}

	for _, slot := range []int{slot1, slot2} {
		sourceState := sourceTCP.server.clusterStateSnapshot()
		targetState := targetTCP.server.clusterStateSnapshot()
		if sourceState.owners[slot] != targetAddr {
			t.Fatalf("source owner[%d]=%q want=%q", slot, sourceState.owners[slot], targetAddr)
		}
		if targetState.owners[slot] != targetAddr {
			t.Fatalf("target owner[%d]=%q want=%q", slot, targetState.owners[slot], targetAddr)
		}
		if sourceState.migrating[slot] != "" || sourceState.importing[slot] != "" {
			t.Fatalf("source slot %d transition not cleared", slot)
		}
		if targetState.migrating[slot] != "" || targetState.importing[slot] != "" {
			t.Fatalf("target slot %d transition not cleared", slot)
		}
	}

	for _, tc := range []struct {
		key   string
		value string
	}{
		{key1, "one"},
		{key2, "two"},
	} {
		if sourceTCP.server.store.Exists([]string{tc.key}) != 0 {
			t.Fatalf("source still contains %q", tc.key)
		}
		gotValue, err := targetTCP.server.execute([][]byte{
			[]byte("GET"), []byte(tc.key),
		})
		if err != nil {
			t.Fatalf("target GET %q: %v", tc.key, err)
		}
		want := fmt.Sprintf("$%d\r\n%s\r\n", len(tc.value), tc.value)
		if string(gotValue) != want {
			t.Fatalf("target GET %q=%q want=%q", tc.key, gotValue, want)
		}
	}
}

func TestClusterRebalanceApplyBatchValidatesLimit(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	state := s.clusterStateSnapshot()
	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	planID := clusterRebalancePlanID(state, moves)

	for _, limit := range []string{"0", "-1", "nope"} {
		_, err := s.execute([][]byte{
			[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
			[]byte(planID), []byte("BATCH"), []byte(limit),
		})
		if err == nil || err.Error() != "ERR REBALANCE APPLY BATCH limit must be a positive integer" {
			t.Fatalf("limit=%q err=%v", limit, err)
		}
	}
}

func TestClusterRebalanceApplyBatchRequiresLocalSource(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, b, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	state := s.clusterStateSnapshot()
	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	planID := clusterRebalancePlanID(state, moves)

	_, err = s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("BATCH"), []byte("2"),
	})
	want := "ERR REBALANCE APPLY BATCH has no planned slots sourced by this node " + b
	if err == nil || err.Error() != want {
		t.Fatalf("err=%v want=%q", err, want)
	}
}
