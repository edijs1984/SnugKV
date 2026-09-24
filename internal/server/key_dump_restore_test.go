package server

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
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

}

func TestKeyRestoreABSTTL(t *testing.T) {
	s := New(engine.New())
	payload, _ := hex.DecodeString("000568656c6c6f0c00a804ebb69f1ebe43")
	abs := time.Now().Add(5 * time.Second).UnixMilli()
	if _, err := s.Execute([][]byte{
		[]byte("RESTORE"),
		[]byte("abs"),
		[]byte(strconv.FormatInt(abs, 10)),
		payload,
		[]byte("ABSTTL"),
	}); err != nil {
		t.Fatalf("RESTORE ABSTTL: %v", err)
	}
	pttl := s.store.TTL("abs", true)
	if pttl < 4500 || pttl > 5000 {
		t.Fatalf("ABSTTL PTTL = %d", pttl)
	}
}

func TestKeyRestoreRedis16TemplateHashPayload(t *testing.T) {
	fields := [][]byte{[]byte("age"), []byte("name"), []byte("email")}
	body := []byte{redisRDBTypeHashTmplArray}
	body = appendRDBLen(body, 1) // FIELDS_RAW
	body = appendRDBLen(body, uint64(len(fields)))
	for _, field := range fields {
		body = appendRDBRawString(body, field)
	}
	body = appendRDBRawString(body, []byte("42"))
	body = appendRDBRawString(body, []byte("Edijs"))
	body = appendRDBRawString(body, []byte("edijs@example.com"))

	var version [2]byte
	binary.LittleEndian.PutUint16(version[:], uint16(redisRDBMaxSupportedVersion))
	payload := append(append([]byte(nil), body...), version[:]...)
	var checksum [8]byte
	binary.LittleEndian.PutUint64(checksum[:], redisCRC64(payload))
	payload = append(payload, checksum[:]...)

	s := New(engine.New())
	if _, err := s.Execute([][]byte{[]byte("RESTORE"), []byte("tmpl"), []byte("0"), payload}); err != nil {
		t.Fatalf("RESTORE: %v", err)
	}
	got, err := s.Execute([][]byte{[]byte("HGETALL"), []byte("tmpl")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"age", "42", "email", "edijs@example.com", "name", "Edijs"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("HGETALL missing %q: %q", want, got)
		}
	}
}

func TestKeyRestoreAcceptsRedisRawDumpWithZeroChecksum(t *testing.T) {
	body := []byte{redisRDBTypeHashTmplArray}
	body = appendRDBLen(body, 1)
	body = appendRDBLen(body, 3)
	body = appendRDBRawString(body, []byte("age"))
	body = appendRDBRawString(body, []byte("name"))
	body = appendRDBRawString(body, []byte("email"))
	body = appendRDBRawString(body, []byte("42"))
	body = appendRDBRawString(body, []byte("Edijs"))
	body = appendRDBRawString(body, []byte("edijs@example.com"))

	var version [2]byte
	binary.LittleEndian.PutUint16(version[:], uint16(redisRDBMaxSupportedVersion))
	payload := append(append([]byte(nil), body...), version[:]...)
	payload = append(payload, make([]byte, 8)...)

	s := New(engine.New())
	if _, err := s.Execute([][]byte{
		[]byte("RESTORE"), []byte("tmpl:raw"), []byte("0"), payload, []byte("REPLACE"),
	}); err != nil {
		t.Fatalf("RESTORE zero-checksum payload: %v", err)
	}

	got, err := s.Execute([][]byte{[]byte("HGETALL"), []byte("tmpl:raw")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"age", "42", "name", "Edijs", "email", "edijs@example.com"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("HGETALL missing %q: %q", want, got)
		}
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


func TestKeyDumpRedis82NativeFixtures(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*Server)
		key     string
		wantHex string
	}{
		{
			name: "hash-listpack",
			setup: func(s *Server) {
				if got := execute(t, s, "HSET", "h", "a", "1", "b", "two", "c", "three"); got != ":3\r\n" {
					t.Fatalf("HSET = %q", got)
				}
			},
			key: "h",
			wantHex: "101e1e000000060081610201018162028374776f0481630285746872656506ff0c00db36cf028de770b0",
		},
		{
			name: "set-listpack",
			setup: func(s *Server) {
				if got := execute(t, s, "SADD", "ss", "alpha", "beta", "gamma"); got != ":3\r\n" {
					t.Fatalf("SADD = %q", got)
				}
			},
			key: "ss",
			wantHex: "141b1b000000030085616c706861068462657461058567616d6d6106ff0c009a7345486301b8e9",
		},
		{
			name: "set-intset",
			setup: func(s *Server) {
				if got := execute(t, s, "SADD", "si", "1", "2", "3", "1000"); got != ":4\r\n" {
					t.Fatalf("SADD ints = %q", got)
				}
			},
			key: "si",
			wantHex: "0b100200000004000000010002000300e8030c0095c987bb742ffce2",
		},
		{
			name: "list-quicklist2",
			setup: func(s *Server) {
				if got := execute(t, s, "RPUSH", "l", "one", "two", "three", "four"); got != ":4\r\n" {
					t.Fatalf("RPUSH = %q", got)
				}
			},
			key: "l",
			wantHex: "1201021e1e0000000400836f6e65048374776f048574687265650684666f757205ff0c003c3bb7048e6be326",
		},
		{
			name: "zset-listpack",
			setup: func(s *Server) {
				if got := execute(t, s, "ZADD", "z", "1.5", "one", "2", "two", "-3.25", "three"); got != ":3\r\n" {
					t.Fatalf("ZADD = %q", got)
				}
			},
			key: "z",
			wantHex: "112626000000060085746872656506852d332e323506836f6e650483312e35048374776f040201ff0c0083f1147074462567",
		},
		{
			name: "hll-string",
			setup: func(s *Server) {
				if got := execute(t, s, "PFADD", "hll", "alice", "bob", "carol"); got != ":1\r\n" {
					t.Fatalf("PFADD = %q", got)
				}
			},
			key: "hll",
			wantHex: "001b48594c4c010000000000000000000080453c9458108451698c51440c002b7fd5455fdb37b0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(engine.New())
			tc.setup(s)
			response, err := s.Execute([][]byte{[]byte("DUMP"), []byte(tc.key)})
			if err != nil {
				t.Fatal(err)
			}
			payload := bulkPayloadFromResponse(t, response)
			if gotHex := hex.EncodeToString(payload); gotHex != tc.wantHex {
				t.Fatalf("dump = %s, want %s", gotHex, tc.wantHex)
			}
		})
	}
}

func TestKeyRestoreRedis82NativeFixtures(t *testing.T) {
	tests := []struct {
		name    string
		hex     string
		key     string
		check   []string
		want    string
	}{
		{
			name: "hash-listpack",
			hex: "101e1e000000060081610201018162028374776f0481630285746872656506ff0c00db36cf028de770b0",
			key: "h2",
			check: []string{"HGETALL", "h2"},
			want: "*6\r\n$1\r\na\r\n$1\r\n1\r\n$1\r\nb\r\n$3\r\ntwo\r\n$1\r\nc\r\n$5\r\nthree\r\n",
		},
		{
			name: "set-listpack",
			hex: "141b1b000000030085616c706861068462657461058567616d6d6106ff0c009a7345486301b8e9",
			key: "ss2",
			check: []string{"SMEMBERS", "ss2"},
			want: "*3\r\n$5\r\nalpha\r\n$4\r\nbeta\r\n$5\r\ngamma\r\n",
		},
		{
			name: "set-intset",
			hex: "0b100200000004000000010002000300e8030c0095c987bb742ffce2",
			key: "si2",
			check: []string{"SMEMBERS", "si2"},
			want: "*4\r\n$1\r\n1\r\n$4\r\n1000\r\n$1\r\n2\r\n$1\r\n3\r\n",
		},
		{
			name: "list-quicklist2",
			hex: "1201021e1e0000000400836f6e65048374776f048574687265650684666f757205ff0c003c3bb7048e6be326",
			key: "l2",
			check: []string{"LRANGE", "l2", "0", "-1"},
			want: "*4\r\n$3\r\none\r\n$3\r\ntwo\r\n$5\r\nthree\r\n$4\r\nfour\r\n",
		},
		{
			name: "zset-listpack",
			hex: "112626000000060085746872656506852d332e323506836f6e650483312e35048374776f040201ff0c0083f1147074462567",
			key: "z2",
			check: []string{"ZRANGE", "z2", "0", "-1", "WITHSCORES"},
			want: "*6\r\n$5\r\nthree\r\n$5\r\n-3.25\r\n$3\r\none\r\n$3\r\n1.5\r\n$3\r\ntwo\r\n$1\r\n2\r\n",
		},
		{
			name: "hll-string",
			hex: "001b48594c4c010000000000000000000080453c9458108451698c51440c002b7fd5455fdb37b0",
			key: "hll2",
			check: []string{"PFCOUNT", "hll2"},
			want: ":3\r\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(engine.New())
			payload, err := hex.DecodeString(tc.hex)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Execute([][]byte{[]byte("RESTORE"), []byte(tc.key), []byte("0"), payload}); err != nil {
				t.Fatalf("RESTORE: %v", err)
			}
			args := make([][]byte, len(tc.check))
			for i := range tc.check {
				args[i] = []byte(tc.check[i])
			}
			got, err := s.Execute(args)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("check = %q, want %q", got, tc.want)
			}
		})
	}
}


func TestKeyDumpRedis82StreamEntriesFixture(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "XADD", "st", "1000-0", "f1", "v1", "f2", "v2"); got != "$6\r\n1000-0\r\n" {
		t.Fatalf("XADD 1000-0 = %q", got)
	}
	if got := execute(t, s, "XADD", "st", "1001-0", "f1", "v3"); got != "$6\r\n1001-0\r\n" {
		t.Fatalf("XADD 1001-0 = %q", got)
	}
	response, err := s.Execute([][]byte{[]byte("DUMP"), []byte("st")})
	if err != nil {
		t.Fatal(err)
	}
	payload := bulkPayloadFromResponse(t, response)
	want := "15011000000000000003e80000000000000000c33439133900000013000201000102018266310382663203400b00002001018276200f0376320305200b000160036021057633030601ff0243e90043e800000002000c00f30b683747c98465"
	if got := hex.EncodeToString(payload); got != want {
		t.Fatalf("stream dump = %s, want %s", got, want)
	}
}

func TestKeyRestoreRedis82StreamDeletedMetadata(t *testing.T) {
	payload, err := hex.DecodeString("15011000000000000003e80000000000000000393900000013000101010102018266310382663203000102010001000182763103827632030501010101010001010182663103827633030601ff0143e90043e80043e90002000c002d7ac7eef0f0549a")
	if err != nil {
		t.Fatal(err)
	}
	object, err := decodeKeyDumpObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	if object.stream == nil {
		t.Fatal("missing stream snapshot")
	}
	snapshot := object.stream
	if snapshot.LastID != (engine.StreamID{Millis: 1001, Sequence: 0}) {
		t.Fatalf("LastID = %#v", snapshot.LastID)
	}
	if snapshot.MaxDeletedID != (engine.StreamID{Millis: 1001, Sequence: 0}) {
		t.Fatalf("MaxDeletedID = %#v", snapshot.MaxDeletedID)
	}
	if snapshot.EntriesAdded != 2 || len(snapshot.Entries) != 1 || snapshot.Entries[0].ID != (engine.StreamID{Millis: 1000, Sequence: 0}) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestKeyRestoreRedis82StreamGroupPendingFixture(t *testing.T) {
	payload, err := hex.DecodeString("15011000000000000003e80000000000000000c34041404b144b0000001a00020101010201826631038266320300200b00002001018276200f0376320305201d0001200f000180210376330306200d4023c0110034201100ff0243ea0043e80043e900030102673143ea00030100000000000003ea00000000000000001fb757b9a001000001010263311fb757b9a00100001fb757b9a00100000100000000000003ea00000000000000000c00aac646f291c3f120")
	if err != nil {
		t.Fatal(err)
	}
	object, err := decodeKeyDumpObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	if object.stream == nil {
		t.Fatal("missing stream snapshot")
	}
	snapshot := object.stream
	if snapshot.LastID != (engine.StreamID{Millis: 1002}) ||
		snapshot.MaxDeletedID != (engine.StreamID{Millis: 1001}) ||
		snapshot.EntriesAdded != 3 ||
		len(snapshot.Entries) != 2 {
		t.Fatalf("stream metadata = %#v", snapshot)
	}
	if len(snapshot.Groups) != 1 {
		t.Fatalf("groups = %#v", snapshot.Groups)
	}
	group := snapshot.Groups[0]
	if group.Name != "g1" || group.LastDeliveredID != (engine.StreamID{Millis: 1002}) || group.EntriesRead != 3 {
		t.Fatalf("group = %#v", group)
	}
	if len(group.Consumers) != 1 || group.Consumers[0].Name != "c1" {
		t.Fatalf("consumers = %#v", group.Consumers)
	}
	if len(group.Pending) != 1 ||
		group.Pending[0].ID != (engine.StreamID{Millis: 1002}) ||
		group.Pending[0].Consumer != "c1" ||
		group.Pending[0].Deliveries != 1 {
		t.Fatalf("pending = %#v", group.Pending)
	}

	s := New(engine.New())
	if _, err := s.Execute([][]byte{[]byte("RESTORE"), []byte("st2"), []byte("0"), payload}); err != nil {
		t.Fatalf("RESTORE stream: %v", err)
	}
	if got := execute(t, s, "XRANGE", "st2", "-", "+"); got != "*2\r\n*2\r\n$6\r\n1000-0\r\n*4\r\n$2\r\nf1\r\n$2\r\nv1\r\n$2\r\nf2\r\n$2\r\nv2\r\n*2\r\n$6\r\n1002-0\r\n*2\r\n$2\r\nf1\r\n$2\r\nv4\r\n" {
		t.Fatalf("XRANGE restored stream = %q", got)
	}
	if got := execute(t, s, "XPENDING", "st2", "g1"); !strings.HasPrefix(got, "*4\r\n:1\r\n$6\r\n1002-0\r\n$6\r\n1002-0\r\n") {
		t.Fatalf("XPENDING restored stream = %q", got)
	}
}
