package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func bloomArgs(values ...string) [][]byte {
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = []byte(value)
	}
	return out
}

func TestBloomOracleCore(t *testing.T) {
	s := New(engine.New())

	response, err := s.Execute(bloomArgs("BF.RESERVE", "bf", "0.01", "100"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("BF.RESERVE = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.ADD", "bf", "alice"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("BF.ADD first = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.ADD", "bf", "alice"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("BF.ADD duplicate = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.EXISTS", "bf", "alice"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("BF.EXISTS present = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.EXISTS", "bf", "missing"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("BF.EXISTS missing = %q err=%v", response, err)
	}

	response, err = s.Execute(bloomArgs("BF.MADD", "bf", "bob", "carol", "dave"))
	if err != nil || string(response) != "*3\r\n:1\r\n:1\r\n:1\r\n" {
		t.Fatalf("BF.MADD = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.MEXISTS", "bf", "alice", "bob", "missing"))
	if err != nil || string(response) != "*3\r\n:1\r\n:1\r\n:0\r\n" {
		t.Fatalf("BF.MEXISTS = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.CARD", "bf"))
	if err != nil || string(response) != ":4\r\n" {
		t.Fatalf("BF.CARD = %q err=%v", response, err)
	}

	response, err = s.Execute(bloomArgs("BF.INFO", "bf"))
	wantInfo := "*10\r\n+Capacity\r\n:100\r\n+Size\r\n:240\r\n+Number of filters\r\n:1\r\n+Number of items inserted\r\n:4\r\n+Expansion rate\r\n:2\r\n"
	if err != nil || string(response) != wantInfo {
		t.Fatalf("BF.INFO = %q err=%v", response, err)
	}
	for field, want := range map[string]string{
		"CAPACITY": "*1\r\n:100\r\n",
		"SIZE": "*1\r\n:240\r\n",
		"FILTERS": "*1\r\n:1\r\n",
		"ITEMS": "*1\r\n:4\r\n",
		"EXPANSION": "*1\r\n:2\r\n",
	} {
		response, err = s.Execute(bloomArgs("BF.INFO", "bf", field))
		if err != nil || string(response) != want {
			t.Fatalf("BF.INFO %s = %q err=%v", field, response, err)
		}
	}
}

func TestBloomOracleAutoCreateInsertAndErrors(t *testing.T) {
	s := New(engine.New())

	response, err := s.Execute(bloomArgs("BF.ADD", "auto", "x"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("auto BF.ADD = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.EXISTS", "auto", "x"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("auto BF.EXISTS = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.CARD", "auto"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("auto BF.CARD = %q err=%v", response, err)
	}

	response, err = s.Execute(bloomArgs("BF.INSERT", "inserted", "CAPACITY", "10", "ERROR", "0.01", "ITEMS", "a", "b", "c"))
	if err != nil || string(response) != "*3\r\n:1\r\n:1\r\n:1\r\n" {
		t.Fatalf("BF.INSERT = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.MEXISTS", "inserted", "a", "b", "z"))
	if err != nil || string(response) != "*3\r\n:1\r\n:1\r\n:0\r\n" {
		t.Fatalf("inserted BF.MEXISTS = %q err=%v", response, err)
	}
	response, err = s.Execute(bloomArgs("BF.CARD", "inserted"))
	if err != nil || string(response) != ":3\r\n" {
		t.Fatalf("inserted BF.CARD = %q err=%v", response, err)
	}

	if _, err := s.Execute(bloomArgs("BF.RESERVE", "baderr", "0", "100")); err == nil ||
		err.Error() != "ERR error rate must be in the range (0.000000, 1.000000)" {
		t.Fatalf("bad error rate = %v", err)
	}
	if _, err := s.Execute(bloomArgs("BF.RESERVE", "badcap", "0.01", "0")); err == nil ||
		err.Error() != "ERR capacity must be in the range [1, 1073741824]" {
		t.Fatalf("bad capacity = %v", err)
	}
	if _, err := s.Execute(bloomArgs("BF.INFO", "missing")); err == nil || err.Error() != "ERR not found" {
		t.Fatalf("missing BF.INFO = %v", err)
	}

	if _, err := s.Execute(bloomArgs("SET", "plain", "value")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(bloomArgs("BF.ADD", "plain", "x")); err == nil ||
		!strings.HasPrefix(err.Error(), "WRONGTYPE ") {
		t.Fatalf("plain BF.ADD = %v", err)
	}
	response, err = s.Execute(bloomArgs("BF.EXISTS", "plain", "x"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("plain BF.EXISTS = %q err=%v", response, err)
	}
}
