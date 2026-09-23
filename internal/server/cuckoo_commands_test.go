package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func cuckooArgs(values ...string) [][]byte {
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = []byte(value)
	}
	return out
}

func TestCuckooOracleCore(t *testing.T) {
	s := New(engine.New())

	response, err := s.Execute(cuckooArgs("CF.RESERVE", "cf", "10"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("CF.RESERVE=%q err=%v", response, err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"CF.ADD","cf","alice"}, ":1\r\n"},
		{[]string{"CF.ADD","cf","alice"}, ":1\r\n"},
		{[]string{"CF.ADDNX","cf","alice"}, ":0\r\n"},
		{[]string{"CF.ADDNX","cf","bob"}, ":1\r\n"},
		{[]string{"CF.EXISTS","cf","alice"}, ":1\r\n"},
		{[]string{"CF.EXISTS","cf","missing"}, ":0\r\n"},
		{[]string{"CF.COUNT","cf","alice"}, ":2\r\n"},
		{[]string{"CF.COUNT","cf","bob"}, ":1\r\n"},
	} {
		response, err = s.Execute(cuckooArgs(tc.args...))
		if err != nil || string(response) != tc.want {
			t.Fatalf("%v=%q err=%v want=%q", tc.args, response, err, tc.want)
		}
	}
	response, err = s.Execute(cuckooArgs("CF.MEXISTS","cf","alice","bob","missing"))
	if err != nil || string(response) != "*3\r\n:1\r\n:1\r\n:0\r\n" {
		t.Fatalf("MEXISTS=%q err=%v", response, err)
	}

	response, err = s.Execute(cuckooArgs("CF.DEL","cf","alice"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("DEL1=%q err=%v", response, err)
	}
	response, err = s.Execute(cuckooArgs("CF.COUNT","cf","alice"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("COUNT after del=%q err=%v", response, err)
	}
	response, err = s.Execute(cuckooArgs("CF.DEL","cf","alice"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("DEL2=%q err=%v", response, err)
	}
	response, err = s.Execute(cuckooArgs("CF.COUNT","cf","alice"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("COUNT final=%q err=%v", response, err)
	}

	response, err = s.Execute(cuckooArgs("CF.INFO","cf"))
	wantInfo := "*16\r\n+Size\r\n:72\r\n+Number of buckets\r\n:8\r\n+Number of filters\r\n:1\r\n+Number of items inserted\r\n:1\r\n+Number of items deleted\r\n:2\r\n+Bucket size\r\n:2\r\n+Expansion rate\r\n:1\r\n+Max iterations\r\n:20\r\n"
	if err != nil || string(response) != wantInfo {
		t.Fatalf("INFO=%q err=%v", response, err)
	}
}

func TestCuckooOracleAutoCreateInsertAndWrongType(t *testing.T) {
	s := New(engine.New())

	response, err := s.Execute(cuckooArgs("CF.ADD","auto","x"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("auto add=%q err=%v", response, err)
	}
	response, err = s.Execute(cuckooArgs("CF.INFO","auto"))
	wantAuto := "*16\r\n+Size\r\n:1080\r\n+Number of buckets\r\n:512\r\n+Number of filters\r\n:1\r\n+Number of items inserted\r\n:1\r\n+Number of items deleted\r\n:0\r\n+Bucket size\r\n:2\r\n+Expansion rate\r\n:1\r\n+Max iterations\r\n:20\r\n"
	if err != nil || string(response) != wantAuto {
		t.Fatalf("auto info=%q err=%v", response, err)
	}

	response, err = s.Execute(cuckooArgs("CF.INSERT","ins","CAPACITY","5","ITEMS","a","a","b","c"))
	if err != nil || string(response) != "*4\r\n:1\r\n:1\r\n:1\r\n:1\r\n" {
		t.Fatalf("insert=%q err=%v", response, err)
	}
	response, err = s.Execute(cuckooArgs("CF.COUNT","ins","a"))
	if err != nil || string(response) != ":4\r\n" {
		t.Fatalf("insert count=%q err=%v", response, err)
	}

	response, err = s.Execute(cuckooArgs("CF.INSERTNX","insnx","CAPACITY","5","ITEMS","a","a","b","c"))
	if err != nil || string(response) != "*4\r\n:1\r\n:0\r\n:1\r\n:1\r\n" {
		t.Fatalf("insertnx=%q err=%v", response, err)
	}
	response, err = s.Execute(cuckooArgs("CF.COUNT","insnx","a"))
	if err != nil || string(response) != ":2\r\n" {
		t.Fatalf("insertnx count=%q err=%v", response, err)
	}

	if _, err := s.Execute(cuckooArgs("CF.RESERVE","bad0","0")); err == nil ||
		err.Error() != "Capacity must be in the range [2 * BUCKETSIZE, 1073741824]" {
		t.Fatalf("bad0=%v", err)
	}
	response, err = s.Execute(cuckooArgs("CF.COUNT","missing","x"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("missing count=%q err=%v", response, err)
	}
	if _, err := s.Execute(cuckooArgs("CF.INFO","missing")); err == nil || err.Error() != "ERR not found" {
		t.Fatalf("missing info=%v", err)
	}

	if _, err := s.Execute(cuckooArgs("SET","plain","value")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(cuckooArgs("CF.ADD","plain","x")); err == nil ||
		!strings.HasPrefix(err.Error(), "WRONGTYPE ") {
		t.Fatalf("plain add=%v", err)
	}
	for _, command := range []string{"CF.EXISTS","CF.COUNT"} {
		response, err = s.Execute(cuckooArgs(command,"plain","x"))
		if err != nil || string(response) != ":0\r\n" {
			t.Fatalf("%s plain=%q err=%v", command, response, err)
		}
	}
	if _, err := s.Execute(cuckooArgs("CF.DEL","plain","x")); err == nil || err.Error() != "Not found" {
		t.Fatalf("plain del=%v", err)
	}
}
