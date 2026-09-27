package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestTimeSeriesAlter(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "TS.CREATE", "ts", "RETENTION", "10000", "DUPLICATE_POLICY", "LAST", "LABELS", "sensor", "a"); got != "+OK\r\n" {
		t.Fatalf("TS.CREATE=%q", got)
	}
	execute(t, s, "TS.ADD", "ts", "1000", "1")
	execute(t, s, "TS.ADD", "ts", "2000", "2")
	execute(t, s, "PEXPIRE", "ts", "60000")

	if got := execute(t, s, "TS.ALTER", "ts", "RETENTION", "500", "DUPLICATE_POLICY", "MAX", "LABELS", "sensor", "b", "region", "eu"); got != "+OK\r\n" {
		t.Fatalf("TS.ALTER=%q", got)
	}

	if got := execute(t, s, "TS.RANGE", "ts", "-", "+"); got != "*1\r\n*2\r\n:2000\r\n+2\r\n" {
		t.Fatalf("TS.RANGE after retention=%q", got)
	}
	if got := execute(t, s, "PTTL", "ts"); got == ":-1\r\n" || got == ":-2\r\n" {
		t.Fatalf("TS.ALTER lost TTL: %q", got)
	}

	info := execute(t, s, "TS.INFO", "ts")
	for _, want := range []string{"max", "sensor", "b", "region", "eu"} {
		if !strings.Contains(info, want) {
			t.Fatalf("TS.INFO missing %q: %q", want, info)
		}
	}
}

func TestTimeSeriesMAdd(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "TS.CREATE", "a")
	execute(t, s, "TS.CREATE", "b")

	if got := execute(t, s, "TS.MADD", "a", "100", "1.5", "b", "200", "2.5", "a", "300", "3.5"); got != "*3\r\n:100\r\n:200\r\n:300\r\n" {
		t.Fatalf("TS.MADD=%q", got)
	}
	if got := execute(t, s, "TS.GET", "a"); got != "*2\r\n:300\r\n+3.5\r\n" {
		t.Fatalf("TS.GET a=%q", got)
	}
	if got := execute(t, s, "TS.GET", "b"); got != "*2\r\n:200\r\n+2.5\r\n" {
		t.Fatalf("TS.GET b=%q", got)
	}
}

func TestTimeSeriesQueryIndex(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "TS.CREATE", "ts:a", "LABELS", "sensor", "temp", "region", "eu")
	execute(t, s, "TS.CREATE", "ts:b", "LABELS", "sensor", "temp", "region", "us")
	execute(t, s, "TS.CREATE", "ts:c", "LABELS", "sensor", "humid", "region", "eu")

	if got := execute(t, s, "TS.QUERYINDEX", "sensor=temp"); got != "*2\r\n$4\r\nts:a\r\n$4\r\nts:b\r\n" {
		t.Fatalf("sensor query=%q", got)
	}
	if got := execute(t, s, "TS.QUERYINDEX", "sensor=temp", "region=eu"); got != "*1\r\n$4\r\nts:a\r\n" {
		t.Fatalf("compound query=%q", got)
	}
}

func TestTimeSeriesCoreErrors(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "x")
	execute(t, s, "TS.CREATE", "ts")

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

	assertErrContains("WRONGTYPE", "TS.ALTER", "plain", "RETENTION", "1")
	assertErrContains("does not exist", "TS.MADD", "missing", "1", "1")
	assertErrContains("invalid filter", "TS.QUERYINDEX", "broken")
}
