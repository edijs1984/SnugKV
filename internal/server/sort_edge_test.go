package server

import (
	"testing"

	"snugkv/internal/engine"
)

func TestSortByNoSortDescAndLimit(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "RPUSH", "order", "a", "b", "c", "d")
	if got := execute(t, s, "SORT", "order", "BY", "nosort", "DESC"); got != "*4\r\n$1\r\nd\r\n$1\r\nc\r\n$1\r\nb\r\n$1\r\na\r\n" {
		t.Fatalf("SORT BY nosort DESC = %q", got)
	}
	if got := execute(t, s, "SORT", "order", "BY", "nosort", "DESC", "LIMIT", "1", "2"); got != "*2\r\n$1\r\nc\r\n$1\r\nb\r\n" {
		t.Fatalf("SORT BY nosort DESC LIMIT = %q", got)
	}
}

func TestSortAlphaByMissingWeightsSortBeforePresent(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "RPUSH", "ids:alpha", "2", "1", "3")
	execute(t, s, "SET", "label_3", "z")
	if got := execute(t, s, "SORT", "ids:alpha", "BY", "label_*", "ALPHA"); got != "*3\r\n$1\r\n2\r\n$1\r\n1\r\n$1\r\n3\r\n" {
		t.Fatalf("SORT ALPHA missing weights = %q", got)
	}
}

func TestSortSetNoSortStoreUsesDeterministicAlphaOrder(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SADD", "set:nosort", "c", "a", "b")
	if got := execute(t, s, "SORT", "set:nosort", "BY", "nosort", "STORE", "set:nosort:dst"); got != ":3\r\n" {
		t.Fatalf("SORT set nosort STORE = %q", got)
	}
	if got := execute(t, s, "LRANGE", "set:nosort:dst", "0", "-1"); got != "*3\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n" {
		t.Fatalf("stored set nosort order = %q", got)
	}
}
