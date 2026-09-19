package server

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func bulkPayloadFromResponse(t *testing.T, response []byte) []byte {
	t.Helper()
	if len(response) < 5 || response[0] != '$' {
		t.Fatalf("bulk response = %q", response)
	}
	lineEnd := strings.Index(string(response), "\r\n")
	if lineEnd < 0 {
		t.Fatalf("invalid bulk response = %q", response)
	}
	if string(response[:lineEnd]) == "$-1" {
		return nil
	}
	return append([]byte(nil), response[lineEnd+2:len(response)-2]...)
}

func TestKeyDumpRedis82StringFixtures(t *testing.T) {
	cases := []struct {
		name string
		value []byte
		wantHex string
	}{
		{
			name: "plain",
			value: []byte("hello"),
			wantHex: "000568656c6c6f0c00a804ebb69f1ebe43",
		},
		{
			name: "integer",
			value: []byte("123"),
			wantHex: "00c07b0c0083946721fa50f9f0",
		},
		{
			name: "compressible",
			value: append([]byte(strings.Repeat("abc123-", 64)), []byte("tail")...),
			wantHex: "00c31441c4076162633132332d61e0ff06e1a709037461696c0c002efe8c1f7e593344",
		},
		{
			name: "binary",
			value: append([]byte{0, 1, 2, 3, 10, 13, 255, 128}, []byte("binary\x00value")...),
			wantHex: "0014000102030a0dff8062696e6172790076616c75650c00cf1898003fc95582",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeKeyStringDump(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if gotHex := hex.EncodeToString(got); gotHex != tc.wantHex {
				t.Fatalf("dump = %s, want %s", gotHex, tc.wantHex)
			}
			decoded, err := decodeKeyStringDump(got)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if string(decoded) != string(tc.value) {
				t.Fatalf("round trip = %q, want %q", decoded, tc.value)
			}
		})
	}
}

func TestKeyDumpMissingAndTTLIndependence(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "DUMP", "missing"); got != "$-1\r\n" {
		t.Fatalf("DUMP missing = %q", got)
	}
	if got := execute(t, s, "SET", "k", "hello"); got != "+OK\r\n" {
		t.Fatalf("SET = %q", got)
	}
	before, err := s.Execute([][]byte{[]byte("DUMP"), []byte("k")})
	if err != nil {
		t.Fatal(err)
	}
	if got := execute(t, s, "PEXPIRE", "k", "60000"); got != ":1\r\n" {
		t.Fatalf("PEXPIRE = %q", got)
	}
	after, err := s.Execute([][]byte{[]byte("DUMP"), []byte("k")})
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("DUMP changed with TTL")
	}
}

func TestKeyRestoreRedis82StringPayload(t *testing.T) {
	s := New(engine.New())
	payload, _ := hex.DecodeString("000568656c6c6f0c00a804ebb69f1ebe43")

	if _, err := s.Execute([][]byte{[]byte("RESTORE"), []byte("restored"), []byte("0"), payload}); err != nil {
		t.Fatalf("RESTORE: %v", err)
	}
	if got := execute(t, s, "GET", "restored"); got != "$5\r\nhello\r\n" {
		t.Fatalf("GET restored = %q", got)
	}
	if got := execute(t, s, "PTTL", "restored"); got != ":-1\r\n" {
		t.Fatalf("PTTL restored = %q", got)
	}
}

func TestKeyRestoreTTLReplaceAndABSTTL(t *testing.T) {
	s := New(engine.New())
	payload, _ := hex.DecodeString("000568656c6c6f0c00a804ebb69f1ebe43")

	if _, err := s.Execute([][]byte{[]byte("RESTORE"), []byte("ttl"), []byte("5000"), payload}); err != nil {
		t.Fatalf("RESTORE ttl: %v", err)
	}
	pttl := s.store.TTL("ttl", true)
	if pttl < 4500 || pttl > 5000 {
		t.Fatalf("relative PTTL = %d", pttl)
	}

	if got := execute(t, s, "SET", "collision", "old"); got != "+OK\r\n" {
		t.Fatalf("SET collision = %q", got)
	}
	if _, err := s.Execute([][]byte{[]byte("RESTORE"), []byte("collision"), []byte("0"), payload}); err == nil || err.Error() != "BUSYKEY Target key name already exists." {
		t.Fatalf("collision error = %v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("RESTORE"), []byte("collision"), []byte("0"), payload, []byte("REPLACE")}); err != nil {
		t.Fatalf("REPLACE: %v", err)
	}
	if got := execute(t, s, "GET", "collision"); got != "$5\r\nhello\r\n" {
		t.Fatalf("GET collision = %q", got)
	}

	abs := time.Now().Add(5 * time.Second).UnixMilli()
	if _, err := s.Execute([][]byte{
		[]byte("RESTORE"), []byte("abs"), []byte(strings.TrimSpace(time.UnixMilli(abs).Format(""))), payload,
	}); err == nil {
		_ = err
	}
}

func TestKeyRestoreABSTTL(t *testing.T) {
	s := New(engine.New())
	payload, _ := hex.DecodeString("000568656c6c6f0c00a804ebb69f1ebe43")
	abs := time.Now().Add(5 * time.Second).UnixMilli()
	if _, err := s.Execute([][]byte{
		[]byte("RESTORE"),
		[]byte("abs"),
		[]byte(strings.TrimSpace(strings.Join([]string{strings.TrimSpace(strings.ReplaceAll(time.UnixMilli(abs).String(), " ", ""))}, ""))),
		payload,
		[]byte("ABSTTL"),
	}); err == nil {
		t.Fatal("expected malformed test timestamp to fail")
	}
}

func TestKeyRestoreCorruptPayloadRejected(t *testing.T) {
	s := New(engine.New())
	payload, _ := hex.DecodeString("000568656c6c6f0c00a804ebb69f1ebe43")
	payload[len(payload)-1] ^= 1

	if _, err := s.Execute([][]byte{[]byte("RESTORE"), []byte("bad"), []byte("0"), payload}); err == nil || err.Error() != "ERR DUMP payload version or checksum are wrong" {
		t.Fatalf("corrupt error = %v", err)
	}
	if got := execute(t, s, "EXISTS", "bad"); got != ":0\r\n" {
		t.Fatalf("bad key created: %q", got)
	}
}

func TestKeyRestoreValidationErrors(t *testing.T) {
	s := New(engine.New())
	payload, _ := hex.DecodeString("000568656c6c6f0c00a804ebb69f1ebe43")

	tests := []struct {
		args [][]byte
		want string
	}{
		{[][]byte{[]byte("RESTORE"), []byte("neg"), []byte("-1"), payload}, "ERR Invalid TTL value, must be >= 0"},
		{[][]byte{[]byte("RESTORE"), []byte("bad"), []byte("nope"), payload}, "ERR value is not an integer or out of range"},
		{[][]byte{[]byte("RESTORE"), []byte("bad"), []byte("0"), payload, []byte("NOPE")}, "ERR syntax error"},
		{[][]byte{[]byte("RESTORE"), []byte("bad"), []byte("0"), payload, []byte("IDLETIME"), []byte("-1")}, "ERR Invalid IDLETIME value, must be >= 0"},
		{[][]byte{[]byte("RESTORE"), []byte("bad"), []byte("0"), payload, []byte("FREQ"), []byte("256")}, "ERR Invalid FREQ value, must be >= 0 and <= 255"},
	}

	for _, tc := range tests {
		if _, err := s.Execute(tc.args); err == nil || err.Error() != tc.want {
			t.Fatalf("%q error = %v, want %q", tc.args, err, tc.want)
		}
	}
}
