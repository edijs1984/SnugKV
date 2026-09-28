package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterRebalancePlanRejectsIncompleteSingleOwnerCoverage(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999": a,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := planClusterRebalance(s.clusterStateSnapshot())
	want := "ERR REBALANCE PLAN requires all hash slots to be assigned"
	if err == nil || err.Error() != want {
		t.Fatalf("err=%v want=%q", err, want)
	}
}

func TestClusterRebalancePlanAllowsFullyAssignedSingleOwner(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-16383": a,
	}); err != nil {
		t.Fatal(err)
	}

	moves, err := planClusterRebalance(s.clusterStateSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 0 {
		t.Fatalf("moves=%+v want empty", moves)
	}
}

func TestClusterRebalanceMutationsFailFastWhenOperationBusy(t *testing.T) {
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

	s.clusterRebalanceMu.Lock()
	defer s.clusterRebalanceMu.Unlock()

	want := "ERR another cluster rebalance operation is already in progress"
	cases := [][][]byte{
		{
			[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
			[]byte(planID), []byte("ONCE"),
		},
		{
			[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
			[]byte(planID), []byte("BATCH"), []byte("1"),
		},
		{
			[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
			[]byte(planID), []byte("ALL"),
		},
		{
			[]byte("CLUSTER"), []byte("REBALANCE"), []byte("EXECUTE"),
			[]byte("1"), []byte(clusterNodeID(b)),
		},
		{
			[]byte("CLUSTER"), []byte("REBALANCE"), []byte("RECOVER"),
			[]byte("RESUME"), []byte("1"),
		},
	}
	for _, args := range cases {
		_, err := s.execute(args)
		if err == nil || err.Error() != want {
			t.Fatalf("command=%q err=%v want=%q", args, err, want)
		}
	}
}

func TestClusterRebalanceReadOnlyCommandsRemainAvailableWhenOperationBusy(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	s.clusterRebalanceMu.Lock()
	defer s.clusterRebalanceMu.Unlock()

	for _, args := range [][][]byte{
		{
			[]byte("CLUSTER"), []byte("REBALANCE"), []byte("PLAN"),
		},
		{
			[]byte("CLUSTER"), []byte("REBALANCE"), []byte("STATUS"),
		},
		{
			[]byte("CLUSTER"), []byte("REBALANCE"), []byte("RECOVER"), []byte("PLAN"),
		},
	} {
		got, err := s.execute(args)
		if err != nil {
			t.Fatalf("command=%q err=%v", args, err)
		}
		if len(got) == 0 || !strings.HasPrefix(string(got), "*") {
			t.Fatalf("command=%q response=%q", args, got)
		}
	}
}
