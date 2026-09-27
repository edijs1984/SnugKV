package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestTimeSeriesCreateRuleAndInfo(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "TS.CREATE", "src")
	execute(t, s, "TS.CREATE", "dst")
	execute(t, s, "PEXPIRE", "src", "60000")
	execute(t, s, "PEXPIRE", "dst", "60000")

	if got := execute(t, s, "TS.CREATERULE", "src", "dst", "AGGREGATION", "MIN", "3"); got != "+OK\r\n" {
		t.Fatalf("TS.CREATERULE=%q", got)
	}

	srcInfo := execute(t, s, "TS.INFO", "src")
	for _, want := range []string{"dst", ":3\r\n", "+MIN\r\n", ":0\r\n"} {
		if !strings.Contains(srcInfo, want) {
			t.Fatalf("source TS.INFO missing %q: %q", want, srcInfo)
		}
	}
	dstInfo := execute(t, s, "TS.INFO", "dst")
	if !strings.Contains(dstInfo, "src") {
		t.Fatalf("destination TS.INFO missing sourceKey: %q", dstInfo)
	}

	for _, key := range []string{"src", "dst"} {
		if got := execute(t, s, "PTTL", key); got == ":-1\r\n" || got == ":-2\r\n" {
			t.Fatalf("%s lost TTL: %q", key, got)
		}
	}
}

func TestTimeSeriesRuleCompactsCompletedBucket(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "TS.CREATE", "src")
	execute(t, s, "TS.CREATE", "dst")
	execute(t, s, "TS.CREATERULE", "src", "dst", "AGGREGATION", "MIN", "3")

	execute(t, s, "TS.ADD", "src", "0", "75")
	execute(t, s, "TS.ADD", "src", "1", "77")
	execute(t, s, "TS.ADD", "src", "2", "78")

	if got := execute(t, s, "TS.RANGE", "dst", "-", "+"); got != "*0\r\n" {
		t.Fatalf("destination should still be empty before bucket closes: %q", got)
	}

	execute(t, s, "TS.ADD", "src", "3", "79")
	if got := execute(t, s, "TS.RANGE", "dst", "-", "+"); got != "*1\r\n*2\r\n:0\r\n+75\r\n" {
		t.Fatalf("compacted destination=%q", got)
	}

	execute(t, s, "TS.ADD", "src", "4", "70")
	execute(t, s, "TS.ADD", "src", "6", "80")
	got := execute(t, s, "TS.RANGE", "dst", "-", "+")
	if !strings.Contains(got, ":3\r\n+70\r\n") {
		t.Fatalf("second compacted bucket missing: %q", got)
	}
}

func TestTimeSeriesRuleAlignment(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "TS.CREATE", "src")
	execute(t, s, "TS.CREATE", "dst")
	execute(t, s, "TS.CREATERULE", "src", "dst", "AGGREGATION", "SUM", "10", "5")

	execute(t, s, "TS.ADD", "src", "5", "2")
	execute(t, s, "TS.ADD", "src", "9", "3")
	execute(t, s, "TS.ADD", "src", "15", "4")

	if got := execute(t, s, "TS.RANGE", "dst", "-", "+"); got != "*1\r\n*2\r\n:5\r\n+5\r\n" {
		t.Fatalf("aligned compaction=%q", got)
	}
}

func TestTimeSeriesDeleteRule(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "TS.CREATE", "src")
	execute(t, s, "TS.CREATE", "dst")
	execute(t, s, "TS.CREATERULE", "src", "dst", "AGGREGATION", "AVG", "10")

	if got := execute(t, s, "TS.DELETERULE", "src", "dst"); got != "+OK\r\n" {
		t.Fatalf("TS.DELETERULE=%q", got)
	}

	if got := execute(t, s, "TS.INFO", "src"); strings.Contains(got, "dst") {
		t.Fatalf("source still contains rule: %q", got)
	}
	if got := execute(t, s, "TS.INFO", "dst"); strings.Contains(got, "src") {
		t.Fatalf("destination still contains sourceKey: %q", got)
	}
}

func TestTimeSeriesRuleErrors(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "TS.CREATE", "src")
	execute(t, s, "TS.CREATE", "dst")
	execute(t, s, "SET", "plain", "x")

	assertErrContains := func(want string, args ...string) {
		t.Helper()
		raw := make([][]byte, len(args))
		for i := range args {
			raw[i] = []byte(args[i])
		}
		_, err := s.Execute(raw)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q error=%v want substring %q", args, err, want)
		}
	}

	assertErrContains("greater than 0", "TS.CREATERULE", "src", "dst", "AGGREGATION", "MIN", "0")
	assertErrContains("Unknown aggregation type", "TS.CREATERULE", "src", "dst", "AGGREGATION", "NOPE", "10")
	assertErrContains("WRONGTYPE", "TS.CREATERULE", "plain", "dst", "AGGREGATION", "MIN", "10")

	execute(t, s, "TS.CREATERULE", "src", "dst", "AGGREGATION", "MIN", "10")
	assertErrContains("already exists", "TS.CREATERULE", "src", "dst", "AGGREGATION", "MIN", "10")
	assertErrContains("does not exist", "TS.DELETERULE", "dst", "src")
}
