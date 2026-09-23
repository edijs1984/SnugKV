package server

import (
	"testing"

	"snugkv/internal/engine"
)

func TestBloomExpansionOracle(t *testing.T) {
	s := New(engine.New())

	response, err := s.Execute(bloomArgs("BF.RESERVE", "scale", "0.01", "2"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("reserve=%q err=%v", response, err)
	}
	for i, value := range []string{"v1","v2","v3","v4","v5","v6","v7","v8"} {
		response, err = s.Execute(bloomArgs("BF.ADD", "scale", value))
		if err != nil || string(response) != ":1\r\n" {
			t.Fatalf("add %d=%q err=%v", i+1, response, err)
		}
	}
	response, err = s.Execute(bloomArgs("BF.INFO", "scale"))
	want := "*10\r\n+Capacity\r\n:14\r\n+Size\r\n:256\r\n+Number of filters\r\n:3\r\n+Number of items inserted\r\n:8\r\n+Expansion rate\r\n:2\r\n"
	if err != nil || string(response) != want {
		t.Fatalf("info=%q err=%v", response, err)
	}

	response, err = s.Execute(bloomArgs("BF.RESERVE", "scale3", "0.01", "2", "EXPANSION", "3"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("reserve scale3=%q err=%v", response, err)
	}
	for i := 1; i <= 9; i++ {
		if _, err := s.Execute(bloomArgs("BF.ADD", "scale3", string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	response, err = s.Execute(bloomArgs("BF.INFO", "scale3"))
	want = "*10\r\n+Capacity\r\n:26\r\n+Size\r\n:280\r\n+Number of filters\r\n:3\r\n+Number of items inserted\r\n:9\r\n+Expansion rate\r\n:3\r\n"
	if err != nil || string(response) != want {
		t.Fatalf("scale3 info=%q err=%v", response, err)
	}
}

func TestBloomNonScalingOracle(t *testing.T) {
	s := New(engine.New())
	if response, err := s.Execute(bloomArgs("BF.RESERVE", "fixed", "0.01", "2", "NONSCALING")); err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("reserve=%q err=%v", response, err)
	}
	for _, item := range []string{"a","b"} {
		if response, err := s.Execute(bloomArgs("BF.ADD", "fixed", item)); err != nil || string(response) != ":1\r\n" {
			t.Fatalf("add=%q err=%v", response, err)
		}
	}
	if _, err := s.Execute(bloomArgs("BF.ADD", "fixed", "c")); err == nil || err.Error() != "ERR non scaling filter is full" {
		t.Fatalf("full error=%v", err)
	}
	response, err := s.Execute(bloomArgs("BF.INFO", "fixed"))
	want := "*10\r\n+Capacity\r\n:2\r\n+Size\r\n:104\r\n+Number of filters\r\n:1\r\n+Number of items inserted\r\n:2\r\n+Expansion rate\r\n$-1\r\n"
	if err != nil || string(response) != want {
		t.Fatalf("fixed info=%q err=%v", response, err)
	}

	response, err = s.Execute(bloomArgs("BF.INSERT", "ins2", "CAPACITY", "2", "NONSCALING", "ITEMS", "a", "b", "c"))
	want = "*3\r\n:1\r\n:1\r\n-ERR non scaling filter is full\r\n"
	if err != nil || string(response) != want {
		t.Fatalf("insert=%q err=%v", response, err)
	}
}

func TestBloomExpansionOptionErrorsOracle(t *testing.T) {
	s := New(engine.New())
	if response, err := s.Execute(bloomArgs("BF.RESERVE", "zero", "0.01", "2", "EXPANSION", "0")); err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("expansion zero=%q err=%v", response, err)
	}
	if _, err := s.Execute(bloomArgs("BF.RESERVE", "negative", "0.01", "2", "EXPANSION", "-1")); err == nil ||
		err.Error() != "ERR expansion must be in the range [0, 32768]" {
		t.Fatalf("negative expansion=%v", err)
	}
	if response, err := s.Execute(bloomArgs("BF.RESERVE", "bogus", "0.01", "2", "BOGUS")); err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("bogus=%q err=%v", response, err)
	}
	if _, err := s.Execute(bloomArgs("BF.RESERVE", "dup", "0.01", "2", "EXPANSION", "2", "EXPANSION", "3")); err == nil ||
		err.Error() != "ERR wrong number of arguments for 'bf.reserve' command" {
		t.Fatalf("duplicate options error=%v", err)
	}
}
