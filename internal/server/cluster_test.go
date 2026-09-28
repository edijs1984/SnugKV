package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterCRC16ReferenceVector(t *testing.T) {
	if got := clusterCRC16([]byte("123456789")); got != 0x31C3 {
		t.Fatalf("crc16=%04X want=31C3", got)
	}
}

func TestClusterKeySlotRedisFixtures(t *testing.T) {
	tests := []struct {
		key  string
		slot int
	}{
		{"somekey", 11058},
		{"foo", 12182},
		{"hello", 866},
		{"foo{hash_tag}", 2515},
		{"bar{hash_tag}", 2515},
	}
	for _, tt := range tests {
		if got := clusterKeySlot([]byte(tt.key)); got != tt.slot {
			t.Fatalf("clusterKeySlot(%q)=%d want=%d", tt.key, got, tt.slot)
		}
	}
}

func TestClusterHashTagRules(t *testing.T) {
	pairs := [][2]string{
		{"{user1000}.following", "{user1000}.followers"},
		{"foo{bar}{zap}", "zzz{bar}yyy"},
	}
	for _, pair := range pairs {
		a := clusterKeySlot([]byte(pair[0]))
		b := clusterKeySlot([]byte(pair[1]))
		if a != b {
			t.Fatalf("%q slot=%d, %q slot=%d", pair[0], a, pair[1], b)
		}
	}

	if got, whole := clusterHashKey([]byte("foo{}{bar}")), []byte("foo{}{bar}"); string(got) != string(whole) {
		t.Fatalf("empty hash tag should hash whole key: got=%q", got)
	}
	if got := string(clusterHashKey([]byte("foo{{bar}}zap"))); got != "{bar" {
		t.Fatalf("nested hash tag=%q want={bar", got)
	}
	if got := string(clusterHashKey([]byte("foo{bar}{zap}"))); got != "bar" {
		t.Fatalf("first hash tag=%q want=bar", got)
	}
}

func TestClusterKeySlotCommand(t *testing.T) {
	s := New(engine.New())
	reply, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("KEYSLOT"), []byte("somekey")})
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != ":11058\r\n" {
		t.Fatalf("reply=%q", reply)
	}
}

func TestConfigureClusterSlotsRejectsOverlap(t *testing.T) {
	s := New(engine.New())
	err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-100":  "127.0.0.1:7000",
		"100-200": "127.0.0.1:7001",
	})
	if err == nil {
		t.Fatal("expected overlapping slot ownership to fail")
	}
}

func TestConfigureClusterSlotsBuildsOwnershipMap(t *testing.T) {
	s := New(engine.New())
	err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-8191":     "127.0.0.1:7000",
		"8192-16383": "127.0.0.1:7001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.clusterSlotOwners[0] != "127.0.0.1:7000" ||
		s.clusterSlotOwners[8191] != "127.0.0.1:7000" ||
		s.clusterSlotOwners[8192] != "127.0.0.1:7001" ||
		s.clusterSlotOwners[16383] != "127.0.0.1:7001" {
		t.Fatalf("unexpected ownership map")
	}
}


func TestClusterRoutingAllowsLocalSlot(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	key := []byte("foo")
	slot := clusterKeySlot(key)
	if err := s.configureClusterSlots(true, local, map[string]string{
		fmt.Sprintf("%d", slot): local,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute([][]byte{[]byte("SET"), key, []byte("bar")}); err != nil {
		t.Fatal(err)
	}
}

func TestClusterRoutingReturnsMoved(t *testing.T) {
	s := New(engine.New())
	key := []byte("foo")
	slot := clusterKeySlot(key)
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		fmt.Sprintf("%d", slot): "127.0.0.1:7001",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.execute([][]byte{[]byte("GET"), key})
	if err == nil || err.Error() != fmt.Sprintf("MOVED %d 127.0.0.1:7001", slot) {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterRoutingReturnsCrossSlot(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-16383": "127.0.0.1:7000",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.execute([][]byte{[]byte("MGET"), []byte("foo"), []byte("bar")})
	if err == nil || err.Error() != "CROSSSLOT Keys in request don't hash to the same slot" {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterRoutingAllowsHashTaggedMultiKey(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-16383": "127.0.0.1:7000",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.execute([][]byte{
		[]byte("MGET"),
		[]byte("user:{42}:a"),
		[]byte("user:{42}:b"),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClusterRoutingReturnsClusterDownForUnservedSlot(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0": "127.0.0.1:7000",
	}); err != nil {
		t.Fatal(err)
	}
	key := []byte("foo")
	if clusterKeySlot(key) == 0 {
		t.Fatal("fixture unexpectedly hashes to slot 0")
	}
	_, err := s.execute([][]byte{[]byte("GET"), key})
	if err == nil || err.Error() != "CLUSTERDOWN Hash slot not served" {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterRoutingChecksEvalKeys(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-16383": "127.0.0.1:7000",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.execute([][]byte{
		[]byte("EVAL"),
		[]byte("return 1"),
		[]byte("2"),
		[]byte("foo"),
		[]byte("bar"),
	})
	if err == nil || err.Error() != "CROSSSLOT Keys in request don't hash to the same slot" {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterRoutingDisabledPreservesStandaloneBehavior(t *testing.T) {
	s := New(engine.New())
	if _, err := s.execute([][]byte{
		[]byte("MGET"),
		[]byte("foo"),
		[]byte("bar"),
	}); err != nil {
		t.Fatal(err)
	}
}


func TestClusterSlotRangesCollapseAdjacentOwners(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-100":   "127.0.0.1:7000",
		"101-200": "127.0.0.1:7000",
		"201-300": "127.0.0.1:7001",
	}); err != nil {
		t.Fatal(err)
	}
	ranges := s.clusterSlotRanges()
	if len(ranges) != 2 {
		t.Fatalf("ranges=%+v", ranges)
	}
	if ranges[0].Start != 0 || ranges[0].End != 200 || ranges[0].Owner != "127.0.0.1:7000" {
		t.Fatalf("range0=%+v", ranges[0])
	}
	if ranges[1].Start != 201 || ranges[1].End != 300 || ranges[1].Owner != "127.0.0.1:7001" {
		t.Fatalf("range1=%+v", ranges[1])
	}
}

func TestClusterSlotsReply(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-8191":     "127.0.0.1:7000",
		"8192-16383": "127.0.0.1:7001",
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("SLOTS")})
	if err != nil {
		t.Fatal(err)
	}
	text := string(reply)
	for _, want := range []string{
		":0\r\n",
		":8191\r\n",
		":8192\r\n",
		":16383\r\n",
		"127.0.0.1",
		":7000\r\n",
		":7001\r\n",
		clusterNodeID("127.0.0.1:7000"),
		clusterNodeID("127.0.0.1:7001"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CLUSTER SLOTS reply missing %q: %q", want, text)
		}
	}
}

func TestClusterShardsReply(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-16383": "127.0.0.1:7000",
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("SHARDS")})
	if err != nil {
		t.Fatal(err)
	}
	text := string(reply)
	for _, want := range []string{
		"slots",
		"nodes",
		"endpoint",
		"127.0.0.1",
		"role",
		"master",
		"health",
		"online",
		clusterNodeID("127.0.0.1:7000"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CLUSTER SHARDS reply missing %q: %q", want, text)
		}
	}
}

func TestClusterTopologyCommandsArity(t *testing.T) {
	s := New(engine.New())
	if _, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("SLOTS"), []byte("extra")}); err == nil {
		t.Fatal("expected CLUSTER SLOTS arity error")
	}
	if _, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("SHARDS"), []byte("extra")}); err == nil {
		t.Fatal("expected CLUSTER SHARDS arity error")
	}
}


func TestClusterNodeIDStable(t *testing.T) {
	a := clusterNodeID("127.0.0.1:7000")
	b := clusterNodeID("127.0.0.1:7000")
	if a != b || len(a) != 40 {
		t.Fatalf("a=%q b=%q", a, b)
	}
	if a == clusterNodeID("127.0.0.1:7001") {
		t.Fatal("different endpoints must have different node IDs")
	}
}

func TestClusterNodesReply(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-8191":     "127.0.0.1:7000",
		"8192-16383": "127.0.0.1:7001",
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("NODES")})
	if err != nil {
		t.Fatal(err)
	}
	text := string(reply)
	for _, want := range []string{
		clusterNodeID("127.0.0.1:7000"),
		clusterNodeID("127.0.0.1:7001"),
		"127.0.0.1:7000@0 myself,master",
		"127.0.0.1:7001@0 master",
		"0-8191",
		"8192-16383",
		"connected",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CLUSTER NODES reply missing %q: %q", want, text)
		}
	}
}

func TestClusterInfoReportsOKForFullCoverage(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-8191":     "127.0.0.1:7000",
		"8192-16383": "127.0.0.1:7001",
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("INFO")})
	if err != nil {
		t.Fatal(err)
	}
	text := string(reply)
	for _, want := range []string{
		"cluster_state:ok",
		"cluster_slots_assigned:16384",
		"cluster_slots_ok:16384",
		"cluster_known_nodes:2",
		"cluster_size:2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CLUSTER INFO reply missing %q: %q", want, text)
		}
	}
}

func TestClusterInfoReportsFailForPartialCoverage(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-100": "127.0.0.1:7000",
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("INFO")})
	if err != nil {
		t.Fatal(err)
	}
	text := string(reply)
	if !strings.Contains(text, "cluster_state:fail") ||
		!strings.Contains(text, "cluster_slots_assigned:101") {
		t.Fatalf("reply=%q", text)
	}
}

func TestClusterNodesAndInfoArity(t *testing.T) {
	s := New(engine.New())
	if _, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("NODES"), []byte("extra")}); err == nil {
		t.Fatal("expected CLUSTER NODES arity error")
	}
	if _, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("INFO"), []byte("extra")}); err == nil {
		t.Fatal("expected CLUSTER INFO arity error")
	}
}


func TestClusterErrorsKeepNativeRedisClasses(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.New("MOVED 123 127.0.0.1:7001"), "-MOVED 123 127.0.0.1:7001\r\n"},
		{errors.New("ASK 123 127.0.0.1:7001"), "-ASK 123 127.0.0.1:7001\r\n"},
		{errors.New("CROSSSLOT Keys in request don't hash to the same slot"), "-CROSSSLOT Keys in request don't hash to the same slot\r\n"},
		{errors.New("CLUSTERDOWN Hash slot not served"), "-CLUSTERDOWN Hash slot not served\r\n"},
	}
	for _, tt := range tests {
		if got := string(errorResponse(tt.err)); got != tt.want {
			t.Fatalf("errorResponse(%q)=%q want=%q", tt.err, got, tt.want)
		}
	}
}


func TestClusterModeDisablesScalarFastPaths(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-16383": "127.0.0.1:7000",
	}); err != nil {
		t.Fatal(err)
	}

	if _, handled, err := s.executeAuthorizedConcurrentGet([][]byte{
		[]byte("GET"), []byte("key"),
	}); err != nil || handled {
		t.Fatalf("GET fast path handled=%t err=%v", handled, err)
	}

	if _, handled, err := s.executeAuthorizedConcurrentSet([][]byte{
		[]byte("SET"), []byte("key"), []byte("value"),
	}); err != nil || handled {
		t.Fatalf("SET fast path handled=%t err=%v", handled, err)
	}

	handled, err := s.executeAuthorizedConcurrentRawGet(
		[][]byte{[]byte("GET"), []byte("key")},
		func([]byte) error { return nil },
	)
	if err != nil || handled {
		t.Fatalf("raw GET fast path handled=%t err=%v", handled, err)
	}
}


func TestClusterRoutingAppliesToPublicExecute(t *testing.T) {
	s := New(engine.New())
	key := []byte("foo")
	slot := clusterKeySlot(key)
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		fmt.Sprintf("%d", slot): "127.0.0.1:7001",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := s.Execute([][]byte{[]byte("GET"), key})
	if err == nil || err.Error() != fmt.Sprintf("MOVED %d 127.0.0.1:7001", slot) {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterRoutingPublicExecuteLocalReadWrite(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-16383": "127.0.0.1:7000",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Execute([][]byte{[]byte("SET"), []byte("foo"), []byte("bar")}); err != nil {
		t.Fatal(err)
	}
	reply, err := s.Execute([][]byte{[]byte("GET"), []byte("foo")})
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "$3\r\nbar\r\n" {
		t.Fatalf("reply=%q", reply)
	}
}


func TestClusterRoutingAppliesToCapturedStatePath(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-5460": "127.0.0.1:7000",
		"5461-16383": "127.0.0.1:7001",
	}); err != nil {
		t.Fatal(err)
	}

	k1 := []byte("n1:0")
	k2 := []byte("n2:1")
	if clusterKeySlot(k1) == clusterKeySlot(k2) {
		t.Fatal("test keys unexpectedly share a slot")
	}

	_, err := s.executeForSessionCaptureState(
		[][]byte{[]byte("MGET"), k1, k2},
		nil,
		nil,
		nil,
	)
	if err == nil || err.Error() != "CROSSSLOT Keys in request don't hash to the same slot" {
		t.Fatalf("err=%v", err)
	}
}


func TestClusterShardsGroupsDisjointRangesByOwner(t *testing.T) {
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", map[string]string{
		"0-100":       "127.0.0.1:7000",
		"101-200":     "127.0.0.1:7001",
		"201-300":     "127.0.0.1:7000",
		"301-16383":   "127.0.0.1:7001",
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := s.clusterShardsReply()
	if err != nil {
		t.Fatal(err)
	}
	text := string(reply)

	if strings.Count(text, "127.0.0.1") != 4 {
		t.Fatalf("expected exactly two shard nodes, reply=%q", text)
	}

	for _, want := range []string{
		":0\r\n",
		":100\r\n",
		":201\r\n",
		":300\r\n",
		":101\r\n",
		":200\r\n",
		":301\r\n",
		":16383\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in reply=%q", want, text)
		}
	}
}


func TestClusterSetSlotMigrationRouting(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	target := "127.0.0.1:7001"
	key := []byte("foo")
	slot := clusterKeySlot(key)

	if err := s.configureClusterSlots(true, local, map[string]string{
		"0-8191":     target,
		"8192-16383": local,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.execute([][]byte{[]byte("SET"), key, []byte("value")}); err != nil {
		t.Fatalf("SET migration key seed: %v", err)
	}

	targetID := clusterNodeID(target)
	if _, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"),
		[]byte(strconv.Itoa(slot)), []byte("MIGRATING"), []byte(targetID),
	}); err != nil {
		t.Fatal(err)
	}

	if got, err := s.execute([][]byte{[]byte("GET"), key}); err != nil || string(got) != "$5\r\nvalue\r\n" {
		t.Fatalf("local migrating GET=%q err=%v", got, err)
	}

	if _, err := s.execute([][]byte{[]byte("DEL"), key}); err != nil {
		t.Fatalf("DEL migrating key: %v", err)
	}
	_, err := s.execute([][]byte{[]byte("GET"), key})
	if err == nil || err.Error() != fmt.Sprintf("ASK %d %s", slot, target) {
		t.Fatalf("missing migrating key err=%v", err)
	}

	if got, err := s.execute([][]byte{[]byte("GET"), key}); err == nil || got != nil {
		t.Fatalf("expected ASK for missing key, got=%q err=%v", got, err)
	}
}

func TestClusterImportingRequiresOneShotAsking(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	source := "127.0.0.1:7001"
	key := []byte("foo")
	slot := clusterKeySlot(key)

	if err := s.configureClusterSlots(true, local, map[string]string{
		"0-8191":     local,
		"8192-16383": source,
	}); err != nil {
		t.Fatal(err)
	}

	sourceID := clusterNodeID(source)
	if _, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"),
		[]byte(strconv.Itoa(slot)), []byte("IMPORTING"), []byte(sourceID),
	}); err != nil {
		t.Fatal(err)
	}

	client := newClientSession(1, nil, "client", local)
	s.executionClient = client
	defer func() { s.executionClient = nil }()

	if _, err := s.execute([][]byte{[]byte("GET"), key}); err == nil ||
		err.Error() != fmt.Sprintf("MOVED %d %s", slot, source) {
		t.Fatalf("GET without ASKING err=%v", err)
	}

	if got, err := s.execute([][]byte{[]byte("ASKING")}); err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("ASKING=%q err=%v", got, err)
	}
	if _, err := s.execute([][]byte{[]byte("SET"), key, []byte("imported")}); err != nil {
		t.Fatalf("ASKING SET err=%v", err)
	}

	if _, err := s.execute([][]byte{[]byte("GET"), key}); err == nil ||
		err.Error() != fmt.Sprintf("MOVED %d %s", slot, source) {
		t.Fatalf("ASKING must be one-shot, err=%v", err)
	}
}

func TestClusterSetSlotNodeRejectsSourceWithRemainingKeys(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	other := "127.0.0.1:7001"
	key := []byte("foo")
	slot := clusterKeySlot(key)

	if err := s.configureClusterSlots(true, local, map[string]string{
		"0-8191":     other,
		"8192-16383": local,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute([][]byte{[]byte("SET"), key, []byte("value")}); err != nil {
		t.Fatal(err)
	}

	_, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"),
		[]byte(strconv.Itoa(slot)), []byte("NODE"), []byte(clusterNodeID(other)),
	})
	want := fmt.Sprintf(
		"ERR Can't assign hashslot %d to a different node while I still hold keys for this hash slot.",
		slot,
	)
	if err == nil || err.Error() != want {
		t.Fatalf("err=%v want=%q", err, want)
	}
	if s.clusterSlotOwners[slot] != local {
		t.Fatalf("slot owner changed after rejected reassignment: %q", s.clusterSlotOwners[slot])
	}

	if _, err := s.execute([][]byte{[]byte("DEL"), key}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"),
		[]byte(strconv.Itoa(slot)), []byte("NODE"), []byte(clusterNodeID(other)),
	}); err != nil {
		t.Fatal(err)
	}
	if s.clusterSlotOwners[slot] != other {
		t.Fatalf("slot owner=%q want=%q", s.clusterSlotOwners[slot], other)
	}
}

func TestClusterSetSlotNodeAndStable(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	other := "127.0.0.1:7001"
	key := []byte("foo")
	slot := clusterKeySlot(key)

	if err := s.configureClusterSlots(true, local, map[string]string{
		"0-8191":     local,
		"8192-16383": other,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"),
		[]byte(strconv.Itoa(slot)), []byte("NODE"), []byte(clusterNodeID(local)),
	}); err != nil {
		t.Fatal(err)
	}
	if s.clusterSlotOwners[slot] != local {
		t.Fatalf("slot owner=%q want=%q", s.clusterSlotOwners[slot], local)
	}

	s.clusterSlotMigrating[slot] = other
	s.clusterSlotImporting[slot] = other
	if _, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"),
		[]byte(strconv.Itoa(slot)), []byte("STABLE"),
	}); err != nil {
		t.Fatal(err)
	}
	if s.clusterSlotMigrating[slot] != "" || s.clusterSlotImporting[slot] != "" {
		t.Fatalf("STABLE did not clear migration state")
	}
}


func TestClusterCountAndGetKeysInSlot(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	remote := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, local, map[string]string{
		"0-8191":     remote,
		"8192-16383": local,
	}); err != nil {
		t.Fatal(err)
	}

	keys := []string{"a{foo}", "b{foo}", "foo"}
	slot := clusterKeySlot([]byte("foo"))
	for _, key := range keys {
		if got := clusterKeySlot([]byte(key)); got != slot {
			t.Fatalf("slot(%q)=%d want=%d", key, got, slot)
		}
		if _, err := s.execute([][]byte{[]byte("SET"), []byte(key), []byte("v")}); err != nil {
			t.Fatal(err)
		}
	}

	count, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("COUNTKEYSINSLOT"), []byte(strconv.Itoa(slot)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != ":3\r\n" {
		t.Fatalf("COUNTKEYSINSLOT=%q", count)
	}

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("GETKEYSINSLOT"),
		[]byte(strconv.Itoa(slot)), []byte("2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "*2\r\n$6\r\na{foo}\r\n$6\r\nb{foo}\r\n"
	if string(got) != want {
		t.Fatalf("GETKEYSINSLOT=%q want=%q", got, want)
	}

	zero, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("GETKEYSINSLOT"),
		[]byte(strconv.Itoa(slot)), []byte("0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(zero) != "*0\r\n" {
		t.Fatalf("GETKEYSINSLOT zero=%q", zero)
	}

	remoteSlot := clusterKeySlot([]byte("hello"))
	if err := s.store.SetPlain("hello", []byte("remote-local-copy")); err != nil {
		t.Fatal(err)
	}
	remoteCount, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("COUNTKEYSINSLOT"), []byte(strconv.Itoa(remoteSlot)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(remoteCount) != ":0\r\n" {
		t.Fatalf("remote COUNTKEYSINSLOT=%q", remoteCount)
	}
}

func TestClusterNodesIncludesMigrationMarkers(t *testing.T) {
	sourceAddr := "127.0.0.1:7000"
	targetAddr := "127.0.0.1:7001"
	slot := clusterKeySlot([]byte("foo"))

	source := New(engine.New())
	if err := source.configureClusterSlots(true, sourceAddr, map[string]string{
		"0-8191":     targetAddr,
		"8192-16383": sourceAddr,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.execute([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("MIGRATING"), []byte(clusterNodeID(targetAddr)),
	}); err != nil {
		t.Fatal(err)
	}
	sourceNodes := string(source.clusterNodesReply())
	wantMigrating := fmt.Sprintf("[%d->-%s]", slot, clusterNodeID(targetAddr))
	if !strings.Contains(sourceNodes, wantMigrating) {
		t.Fatalf("CLUSTER NODES missing migrating marker %q: %q", wantMigrating, sourceNodes)
	}

	target := New(engine.New())
	if err := target.configureClusterSlots(true, targetAddr, map[string]string{
		"0-8191":     targetAddr,
		"8192-16383": sourceAddr,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := target.execute([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("IMPORTING"), []byte(clusterNodeID(sourceAddr)),
	}); err != nil {
		t.Fatal(err)
	}
	targetNodes := string(target.clusterNodesReply())
	wantImporting := fmt.Sprintf("[%d-<-%s]", slot, clusterNodeID(sourceAddr))
	if !strings.Contains(targetNodes, wantImporting) {
		t.Fatalf("CLUSTER NODES missing importing marker %q: %q", wantImporting, targetNodes)
	}
}


func TestClusterTopologyStateConcurrentReadWrite(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	remote := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, local, map[string]string{
		"0-8191":     remote,
		"8192-16383": local,
	}); err != nil {
		t.Fatal(err)
	}

	slot := clusterKeySlot([]byte("foo"))
	targetID := clusterNodeID(remote)
	migrating := [][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("MIGRATING"), []byte(targetID),
	}
	stable := [][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("STABLE"),
	}

	var wg sync.WaitGroup
	errs := make(chan error, 32)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			if _, err := s.execute(migrating); err != nil {
				errs <- fmt.Errorf("MIGRATING: %w", err)
				return
			}
			if _, err := s.execute(stable); err != nil {
				errs <- fmt.Errorf("STABLE: %w", err)
				return
			}
		}
	}()

	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = s.clusterNodesReply()
				_ = s.clusterInfoReply()
				if _, err := s.clusterSlotsReply(); err != nil {
					errs <- fmt.Errorf("SLOTS: %w", err)
					return
				}
				if _, err := s.clusterShardsReply(); err != nil {
					errs <- fmt.Errorf("SHARDS: %w", err)
					return
				}
				_ = s.clusterLocalKeysInSlot(slot, 10)
				_ = s.enforceClusterRouting([][]byte{[]byte("GET"), []byte("missing{foo}")})
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}


func TestClusterAddAndDelSlots(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	if err := s.configureClusterSlots(true, local, nil); err != nil {
		t.Fatal(err)
	}

	response, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("ADDSLOTS"), []byte("1"), []byte("2"),
	})
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("ADDSLOTS response=%q err=%v", response, err)
	}
	state := s.clusterStateSnapshot()
	if state.owners[1] != local || state.owners[2] != local {
		t.Fatalf("owners[1]=%q owners[2]=%q", state.owners[1], state.owners[2])
	}

	response, err = s.execute([][]byte{
		[]byte("CLUSTER"), []byte("DELSLOTS"), []byte("1"),
	})
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("DELSLOTS response=%q err=%v", response, err)
	}
	state = s.clusterStateSnapshot()
	if state.owners[1] != "" || state.owners[2] != local {
		t.Fatalf("owners after DELSLOTS: slot1=%q slot2=%q", state.owners[1], state.owners[2])
	}
}

func TestClusterSlotManagementValidation(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	if err := s.configureClusterSlots(true, local, map[string]string{"1": local}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args [][]byte
		want string
	}{
		{
			name: "add busy",
			args: [][]byte{[]byte("CLUSTER"), []byte("ADDSLOTS"), []byte("1")},
			want: "ERR Slot 1 is already busy",
		},
		{
			name: "add duplicate",
			args: [][]byte{[]byte("CLUSTER"), []byte("ADDSLOTS"), []byte("2"), []byte("2")},
			want: "ERR Slot 2 specified multiple times",
		},
		{
			name: "delete unassigned",
			args: [][]byte{[]byte("CLUSTER"), []byte("DELSLOTS"), []byte("3")},
			want: "ERR Slot 3 is already unassigned",
		},
		{
			name: "invalid slot",
			args: [][]byte{[]byte("CLUSTER"), []byte("ADDSLOTS"), []byte("16384")},
			want: "ERR Invalid or out of range slot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.execute(tt.args)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("err=%v want=%q", err, tt.want)
			}
		})
	}
}

func TestClusterFlushSlotsRequiresEmptyDB(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	remote := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, local, map[string]string{
		"0-100": local,
		"101-200": remote,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.store.SetPlain("local-data", []byte("v")); err != nil {
		t.Fatal(err)
	}
	_, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("FLUSHSLOTS")})
	if err == nil || err.Error() != "ERR DB must be empty to perform CLUSTER FLUSHSLOTS." {
		t.Fatalf("FLUSHSLOTS with data err=%v", err)
	}
	s.store.DeleteMany([]string{"local-data"})

	response, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("FLUSHSLOTS")})
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("FLUSHSLOTS response=%q err=%v", response, err)
	}
	state := s.clusterStateSnapshot()
	for slot := 0; slot <= 100; slot++ {
		if state.owners[slot] != "" {
			t.Fatalf("local slot %d still owned by %q", slot, state.owners[slot])
		}
	}
	for slot := 101; slot <= 200; slot++ {
		if state.owners[slot] != remote {
			t.Fatalf("remote slot %d owner=%q", slot, state.owners[slot])
		}
	}
}

func TestClusterMyID(t *testing.T) {
	s := New(engine.New())
	local := "127.0.0.1:7000"
	if err := s.configureClusterSlots(true, local, nil); err != nil {
		t.Fatal(err)
	}

	got, err := s.execute([][]byte{[]byte("CLUSTER"), []byte("MYID")})
	if err != nil {
		t.Fatal(err)
	}
	wantID := clusterNodeID(local)
	want := fmt.Sprintf("$%d\r\n%s\r\n", len(wantID), wantID)
	if string(got) != want {
		t.Fatalf("MYID=%q want=%q", got, want)
	}
}


func TestClusterRebalancePlanBalancedIsEmpty(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("PLAN"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "$5\r\nmoves\r\n*0\r\n") {
		t.Fatalf("balanced plan must contain empty moves: %q", text)
	}
	if !strings.Contains(text, "$9\r\nprojected\r\n") {
		t.Fatalf("balanced plan missing projected section: %q", text)
	}
	if strings.Count(text, ":8192\r\n") != 2 {
		t.Fatalf("balanced plan projected counts=%q", text)
	}
}

func TestClusterRebalancePlanMovesOnlySurplusSlots(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999":     a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	moves, err := planClusterRebalance(s.clusterStateSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 1 {
		t.Fatalf("moves=%v", moves)
	}
	move := moves[0]
	if move.Start != 8192 || move.End != 9999 || move.Source != a || move.Target != b {
		t.Fatalf("move=%+v", move)
	}
}

func TestClusterRebalancePlanThreeOwnersDeterministic(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	c := "127.0.0.1:7002"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8999":      a,
		"9000-13999":  b,
		"14000-16383": c,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := planClusterRebalance(s.clusterStateSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	second, err := planClusterRebalance(s.clusterStateSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatalf("plan not deterministic: first=%v second=%v", first, second)
	}

	targetCounts := map[string]int{a: 5462, b: 5461, c: 5461}
	counts := map[string]int{a: 9000, b: 5000, c: 2384}
	for _, move := range first {
		n := move.End - move.Start + 1
		counts[move.Source] -= n
		counts[move.Target] += n
	}
	for owner, want := range targetCounts {
		if counts[owner] != want {
			t.Fatalf("owner %s count=%d want=%d plan=%v", owner, counts[owner], want, first)
		}
	}
}

func TestClusterRebalancePlanRequiresFullCoverage(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-100": a,
		"200-300": b,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("PLAN"),
	})
	if err == nil || err.Error() != "ERR REBALANCE PLAN requires all hash slots to be assigned" {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterRebalancePlanDoesNotMutateTopology(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}
	before := s.clusterStateSnapshot()

	if _, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("PLAN"),
	}); err != nil {
		t.Fatal(err)
	}
	after := s.clusterStateSnapshot()

	if before.owners != after.owners ||
		before.migrating != after.migrating ||
		before.importing != after.importing {
		t.Fatal("REBALANCE PLAN mutated cluster topology")
	}
}


func TestClusterRebalancePlanWireShape(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	c := "127.0.0.1:7002"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8999":      a,
		"9000-13999":  b,
		"14000-16383": c,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("PLAN"),
	})
	if err != nil {
		t.Fatal(err)
	}

	wantA := clusterNodeID(a)
	wantB := clusterNodeID(b)
	wantC := clusterNodeID(c)
	text := string(got)
	for _, token := range []string{
		"plan_id",
		"moves",
		"projected",
		"start",
		"end",
		"source_id",
		"source_addr",
		"target_id",
		"target_addr",
		"node_id",
		"addr",
		"slots",
		wantA,
		wantB,
		wantC,
		a,
		b,
		c,
	} {
		if !strings.Contains(text, token) {
			t.Fatalf("plan missing %q: %q", token, text)
		}
	}
	for _, projectedCount := range []string{":5462\r\n", ":5461\r\n"} {
		if !strings.Contains(text, projectedCount) {
			t.Fatalf("plan missing projected count %q: %q", projectedCount, text)
		}
	}

	before := s.clusterStateSnapshot()
	if _, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("PLAN"),
	}); err != nil {
		t.Fatal(err)
	}
	after := s.clusterStateSnapshot()
	if before.owners != after.owners {
		t.Fatal("wire planning call mutated ownership")
	}
}


func TestClusterRebalancePlanIDChangesWithTopology(t *testing.T) {
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
	first := clusterRebalancePlanID(state, moves)

	s.clusterMu.Lock()
	s.clusterSlotOwners[9999] = b
	s.clusterMu.Unlock()

	state = s.clusterStateSnapshot()
	moves, err = planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	second := clusterRebalancePlanID(state, moves)

	if first == second {
		t.Fatalf("plan id did not change after topology mutation: %s", first)
	}
}

func TestClusterRebalancePlanIDDeterministic(t *testing.T) {
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
	first := clusterRebalancePlanID(state, moves)
	second := clusterRebalancePlanID(state, moves)
	if first != second {
		t.Fatalf("plan id not deterministic: %s != %s", first, second)
	}
	if len(first) != 40 {
		t.Fatalf("plan id length=%d want=40", len(first))
	}
}


func TestClusterRebalanceApplyDryRunAcceptsFreshPlan(t *testing.T) {
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

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("DRYRUN"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, token := range []string{"plan_id", planID, "status", "ready", "moves"} {
		if !strings.Contains(text, token) {
			t.Fatalf("dry-run response missing %q: %q", token, text)
		}
	}
	before := s.clusterStateSnapshot()
	after := s.clusterStateSnapshot()
	if before.owners != after.owners || before.migrating != after.migrating || before.importing != after.importing {
		t.Fatal("dry-run mutated cluster state")
	}
}

func TestClusterRebalanceApplyDryRunRejectsStalePlan(t *testing.T) {
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

	s.clusterMu.Lock()
	s.clusterSlotOwners[9999] = b
	s.clusterMu.Unlock()

	_, err = s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("DRYRUN"),
	})
	if err == nil || err.Error() != "ERR REBALANCE plan is stale; run CLUSTER REBALANCE PLAN again" {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterRebalanceApplyDryRunRejectsActiveTransition(t *testing.T) {
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

	s.clusterMu.Lock()
	s.clusterSlotMigrating[9999] = b
	s.clusterMu.Unlock()

	_, err = s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("DRYRUN"),
	})
	if err == nil || err.Error() != "ERR REBALANCE APPLY refused while slots are migrating or importing" {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterRebalanceApplyDryRunRequiresExplicitMode(t *testing.T) {
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

	_, err = s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"), []byte(planID),
	})
	if err == nil || err.Error() != "ERR syntax error" {
		t.Fatalf("err=%v", err)
	}
}


func TestClusterRebalanceStatusBalanced(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("STATUS"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, token := range []string{
		"status", "balanced",
		"plan_id",
		"active_transition", ":0\r\n",
		"move_ranges",
		"slots_to_move",
		"nodes",
		"current_slots",
		"target_slots",
		"delta",
	} {
		if !strings.Contains(text, token) {
			t.Fatalf("status missing %q: %q", token, text)
		}
	}
	if strings.Count(text, ":8192\r\n") < 4 {
		t.Fatalf("balanced status missing expected current/target counts: %q", text)
	}
}

func TestClusterRebalanceStatusReadyShowsRemainingWork(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("STATUS"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, token := range []string{
		"status", "ready",
		"active_transition",
		"move_ranges",
		"slots_to_move",
		":1808\r\n",
		":10000\r\n",
		":6384\r\n",
		":8192\r\n",
		":-1808\r\n",
	} {
		if !strings.Contains(text, token) {
			t.Fatalf("status missing %q: %q", token, text)
		}
	}
}

func TestClusterRebalanceStatusTransitioning(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	s.clusterMu.Lock()
	s.clusterSlotMigrating[9999] = b
	s.clusterMu.Unlock()

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("STATUS"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "$6\r\nstatus\r\n$13\r\ntransitioning\r\n") {
		t.Fatalf("status not transitioning: %q", text)
	}
	if !strings.Contains(text, "$17\r\nactive_transition\r\n:1\r\n") {
		t.Fatalf("active_transition not set: %q", text)
	}
}
