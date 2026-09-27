package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestTimeSeriesMGet(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "TS.CREATE", "ts:a", "LABELS", "sensor", "temp", "region", "eu")
	execute(t, s, "TS.CREATE", "ts:b", "LABELS", "sensor", "temp", "region", "us")
	execute(t, s, "TS.ADD", "ts:a", "100", "1.5")
	execute(t, s, "TS.ADD", "ts:a", "200", "2.5")
	execute(t, s, "TS.ADD", "ts:b", "150", "3.5")

	got := execute(t, s, "TS.MGET", "FILTER", "sensor=temp")
	want := "*2\r\n" +
		"*3\r\n$4\r\nts:a\r\n*0\r\n*2\r\n:200\r\n+2.5\r\n" +
		"*3\r\n$4\r\nts:b\r\n*0\r\n*2\r\n:150\r\n+3.5\r\n"
	if got != want {
		t.Fatalf("TS.MGET=%q want=%q", got, want)
	}

	got = execute(t, s, "TS.MGET", "WITHLABELS", "FILTER", "sensor=temp", "region=eu")
	for _, wantPart := range []string{"ts:a", "sensor", "temp", "region", "eu", ":200", "+2.5"} {
		if !strings.Contains(got, wantPart) {
			t.Fatalf("TS.MGET WITHLABELS missing %q: %q", wantPart, got)
		}
	}
	if strings.Contains(got, "ts:b") {
		t.Fatalf("TS.MGET compound filter unexpectedly included ts:b: %q", got)
	}
}

func TestTimeSeriesMGetSelectedLabels(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "TS.CREATE", "ts:a", "LABELS", "sensor", "temp", "region", "eu", "host", "a")
	execute(t, s, "TS.ADD", "ts:a", "100", "1")

	got := execute(t, s, "TS.MGET", "SELECTED_LABELS", "region", "host", "FILTER", "sensor=temp")
	if !strings.Contains(got, "region") || !strings.Contains(got, "eu") ||
		!strings.Contains(got, "host") || !strings.Contains(got, "a") {
		t.Fatalf("TS.MGET SELECTED_LABELS=%q", got)
	}
	if strings.Contains(got, "sensor") {
		t.Fatalf("TS.MGET SELECTED_LABELS included unselected sensor: %q", got)
	}
}

func TestTimeSeriesMRangeAndReverse(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "TS.CREATE", "ts:a", "LABELS", "sensor", "temp")
	execute(t, s, "TS.CREATE", "ts:b", "LABELS", "sensor", "temp")
	for _, cmd := range [][]string{
		{"TS.ADD", "ts:a", "100", "1"},
		{"TS.ADD", "ts:a", "200", "2"},
		{"TS.ADD", "ts:a", "300", "3"},
		{"TS.ADD", "ts:b", "150", "4"},
		{"TS.ADD", "ts:b", "250", "5"},
	} {
		execute(t, s, cmd...)
	}

	got := execute(t, s, "TS.MRANGE", "150", "250", "FILTER", "sensor=temp")
	for _, want := range []string{"ts:a", ":200", "+2", "ts:b", ":150", "+4", ":250", "+5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("TS.MRANGE missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, ":100") || strings.Contains(got, ":300") {
		t.Fatalf("TS.MRANGE included out-of-range samples: %q", got)
	}

	rev := execute(t, s, "TS.MREVRANGE", "100", "300", "FILTER", "sensor=temp")
	pos300 := strings.Index(rev, ":300\r\n")
	pos200 := strings.Index(rev, ":200\r\n")
	pos100 := strings.Index(rev, ":100\r\n")
	if pos300 < 0 || pos200 < 0 || pos100 < 0 || !(pos300 < pos200 && pos200 < pos100) {
		t.Fatalf("TS.MREVRANGE order=%q", rev)
	}
}

func TestTimeSeriesMultiErrors(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "TS.CREATE", "ts", "LABELS", "sensor", "temp")

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

	assertErrContains("missing FILTER", "TS.MGET", "WITHLABELS", "sensor=temp")
	assertErrContains("missing FILTER", "TS.MRANGE", "-", "+", "WITHLABELS")
	assertErrContains("SELECTED_LABELS", "TS.MGET", "WITHLABELS", "SELECTED_LABELS", "sensor", "FILTER", "sensor=temp")
	assertErrContains("invalid filter", "TS.MGET", "FILTER", "broken")
}
