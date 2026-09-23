package server

import (
	"errors"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func topKArgs(values ...string) [][]byte {
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = []byte(value)
	}
	return out
}

func TestTopKOracleCore(t *testing.T) {
	s := New(engine.New())

	response, err := s.Execute(topKArgs("TOPK.RESERVE", "tk", "3"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("RESERVE=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.INFO", "tk"))
	if err != nil || string(response) != "*8\r\n+k\r\n:3\r\n+width\r\n:8\r\n+depth\r\n:7\r\n+decay\r\n$3\r\n0.9\r\n" {
		t.Fatalf("INFO=%q err=%v", response, err)
	}

	if response, err = s.Execute(topKArgs("TOPK.RESERVE", "custom", "3", "50", "5", "0.9")); err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("custom reserve=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.ADD", "custom", "a", "b", "c"))
	if err != nil || string(response) != "*3\r\n$-1\r\n$-1\r\n$-1\r\n" {
		t.Fatalf("ADD=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.QUERY", "custom", "a", "b", "c", "z"))
	if err != nil || string(response) != "*4\r\n:1\r\n:1\r\n:1\r\n:0\r\n" {
		t.Fatalf("QUERY=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.COUNT", "custom", "a", "b", "c", "z"))
	if err != nil || string(response) != "*4\r\n:1\r\n:1\r\n:1\r\n:0\r\n" {
		t.Fatalf("COUNT=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.LIST", "custom", "WITHCOUNT"))
	if err != nil || string(response) != "*6\r\n$1\r\na\r\n:1\r\n$1\r\nc\r\n:1\r\n$1\r\nb\r\n:1\r\n" {
		t.Fatalf("LIST=%q err=%v", response, err)
	}
}

func TestTopKOracleEjectionAndZeroIncrement(t *testing.T) {
	s := New(engine.New())
	if _, err := s.Execute(topKArgs("TOPK.RESERVE", "rank", "2", "100", "5", "0.9")); err != nil {
		t.Fatal(err)
	}
	response, err := s.Execute(topKArgs("TOPK.INCRBY", "rank", "a", "10", "b", "20"))
	if err != nil || string(response) != "*2\r\n$-1\r\n$-1\r\n" {
		t.Fatalf("rank initial=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.INCRBY", "rank", "c", "30"))
	if err != nil || string(response) != "*1\r\n$1\r\na\r\n" {
		t.Fatalf("rank c=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.QUERY", "rank", "a", "b", "c"))
	if err != nil || string(response) != "*3\r\n:0\r\n:1\r\n:1\r\n" {
		t.Fatalf("rank query=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.COUNT", "rank", "a", "b", "c"))
	if err != nil || string(response) != "*3\r\n:10\r\n:20\r\n:30\r\n" {
		t.Fatalf("rank count=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.LIST", "rank", "WITHCOUNT"))
	if err != nil || string(response) != "*4\r\n$1\r\nc\r\n:30\r\n$1\r\nb\r\n:20\r\n" {
		t.Fatalf("rank list=%q err=%v", response, err)
	}

	if _, err := s.Execute(topKArgs("TOPK.RESERVE", "zero", "2", "100", "5", "0.9")); err != nil {
		t.Fatal(err)
	}
	response, err = s.Execute(topKArgs("TOPK.INCRBY", "zero", "x", "0"))
	if err != nil || string(response) != "*1\r\n$-1\r\n" {
		t.Fatalf("zero incr=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.QUERY", "zero", "x"))
	if err != nil || string(response) != "*1\r\n:1\r\n" {
		t.Fatalf("zero query=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.COUNT", "zero", "x"))
	if err != nil || string(response) != "*1\r\n:0\r\n" {
		t.Fatalf("zero count=%q err=%v", response, err)
	}
	response, err = s.Execute(topKArgs("TOPK.LIST", "zero", "WITHCOUNT"))
	if err != nil || string(response) != "*0\r\n" {
		t.Fatalf("zero list=%q err=%v", response, err)
	}
}

func TestTopKOracleErrors(t *testing.T) {
	s := New(engine.New())

	for _, tc := range []struct {
		args []string
		err  string
	}{
		{[]string{"TOPK.RESERVE", "badk", "0"}, "TopK: invalid k"},
		{[]string{"TOPK.RESERVE", "badw", "2", "0", "5", "0.9"}, "TopK: invalid width"},
		{[]string{"TOPK.RESERVE", "badd", "2", "10", "0", "0.9"}, "TopK: invalid depth"},
		{[]string{"TOPK.RESERVE", "baddecay0", "2", "10", "5", "0"}, "TopK: invalid decay value. must be '<= 1' & '> 0'"},
		{[]string{"TOPK.ADD", "missing", "x"}, "TopK: key does not exist"},
		{[]string{"TOPK.QUERY", "missing", "x"}, "TopK: key does not exist"},
		{[]string{"TOPK.COUNT", "missing", "x"}, "TopK: key does not exist"},
		{[]string{"TOPK.LIST", "missing"}, "TopK: key does not exist"},
		{[]string{"TOPK.INFO", "missing"}, "TopK: key does not exist"},
	} {
		if _, err := s.Execute(topKArgs(tc.args...)); err == nil || err.Error() != tc.err {
			t.Fatalf("%v err=%v want=%q", tc.args, err, tc.err)
		}
	}

	if _, err := s.Execute(topKArgs("TOPK.RESERVE", "custom", "3", "50", "5", "0.9")); err != nil {
		t.Fatal(err)
	}
	response, err := s.Execute(topKArgs("TOPK.INCRBY", "custom", "x", "-1"))
	want := "*1\r\n-" + topKIncrementError + "\r\n"
	if err != nil || string(response) != want {
		t.Fatalf("invalid increment=%q err=%v want=%q", response, err, want)
	}
	if _, err := s.Execute(topKArgs("TOPK.LIST", "custom", "BOGUS")); err == nil || err.Error() != "WITHCOUNT keyword expected" {
		t.Fatalf("bad list err=%v", err)
	}

	if _, err := s.Execute(topKArgs("SET", "plain", "value")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"TOPK.ADD", "plain", "x"},
		{"TOPK.QUERY", "plain", "x"},
		{"TOPK.COUNT", "plain", "x"},
		{"TOPK.LIST", "plain"},
		{"TOPK.INFO", "plain"},
	} {
		if _, err := s.Execute(topKArgs(args...)); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE ") {
			t.Fatalf("%v err=%v", args, err)
		}
	}

	if got := string(errorResponse(errors.New("TopK: key does not exist"))); got != "-TopK: key does not exist\r\n" {
		t.Fatalf("TopK wire error=%q", got)
	}
	if got := string(errorResponse(errors.New("WITHCOUNT keyword expected"))); got != "-WITHCOUNT keyword expected\r\n" {
		t.Fatalf("WITHCOUNT wire error=%q", got)
	}
}
