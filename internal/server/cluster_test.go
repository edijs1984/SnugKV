package server

import (
	"fmt"
	"strings"
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
