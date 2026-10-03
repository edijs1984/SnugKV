package resp

import (
	"strings"
	"bufio"
	"bytes"
	"reflect"
	"testing"
)

func TestParseCommand(t *testing.T) {
	data := []byte("*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n")

	cmds, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	want := [][]byte{[]byte("SET"), []byte("k"), []byte("v")}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("Parse mismatch: got %v want %v", cmds, want)
	}
}

func TestParseBulkString(t *testing.T) {
	data := []byte("$5\r\nhello\r\n")

	v, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if got, want := string(v[0]), "hello"; got != want {
		t.Fatalf("bulk string mismatch: got %q want %q", got, want)
	}
}


func TestReadBufferedSET(t *testing.T) {
	frame := []byte("*3\r\n$3\r\nSET\r\n$3\r\nkey\r\n$5\r\nvalue\r\n*1\r\n$4\r\nPING\r\n")
	reader := bufio.NewReaderSize(bytes.NewReader(frame), 1024)
	if _, err := reader.Peek(len(frame)); err != nil {
		t.Fatal(err)
	}
	decoder, err := NewDecoder(reader, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}

	key, value, ok, err := decoder.ReadBufferedSET(make([]byte, 0, 16), make([]byte, 0, 256))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(key) != "key" || string(value) != "value" {
		t.Fatalf("key=%q value=%q ok=%t", key, value, ok)
	}

	msg, err := decoder.ReadCommand()
	if err != nil {
		t.Fatal(err)
	}
	if len(msg) != 1 || string(msg[0]) != "PING" {
		t.Fatalf("next command=%q", msg)
	}
}

func TestReadBufferedSETLeavesNonPlainSETBuffered(t *testing.T) {
	frame := []byte("*4\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n$2\r\nNX\r\n")
	reader := bufio.NewReaderSize(bytes.NewReader(frame), 1024)
	if _, err := reader.Peek(len(frame)); err != nil {
		t.Fatal(err)
	}
	decoder, err := NewDecoder(reader, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}

	_, _, ok, err := decoder.ReadBufferedSET(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("optioned SET must use generic decoder")
	}
	msg, err := decoder.ReadCommand()
	if err != nil {
		t.Fatal(err)
	}
	if len(msg) != 4 || string(msg[3]) != "NX" {
		t.Fatalf("fallback command=%q", msg)
	}
}


func TestReadBufferedNativeMutation(t *testing.T) {
	tests := []struct {
		frame string
		cmd   string
		args  []string
	}{
		{"*4\r\n$4\r\nHSET\r\n$1\r\nh\r\n$1\r\nf\r\n$1\r\nv\r\n", "HSET", []string{"h", "f", "v"}},
		{"*3\r\n$5\r\nRPUSH\r\n$1\r\nl\r\n$1\r\nv\r\n", "RPUSH", []string{"l", "v"}},
		{"*3\r\n$4\r\nSADD\r\n$1\r\ns\r\n$1\r\nm\r\n", "SADD", []string{"s", "m"}},
		{"*4\r\n$4\r\nZADD\r\n$1\r\nz\r\n$1\r\n1\r\n$1\r\nm\r\n", "ZADD", []string{"z", "1", "m"}},
	}
	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			reader := bufio.NewReaderSize(bytes.NewReader([]byte(tc.frame)), 1024)
			if _, err := reader.Peek(len(tc.frame)); err != nil {
				t.Fatal(err)
			}
			decoder, err := NewDecoder(reader, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			var scratch [3][]byte
			cmd, args, argc, ok, err := decoder.ReadBufferedNativeMutation(&scratch)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || cmd != tc.cmd || argc != len(tc.args) {
				t.Fatalf("cmd=%q argc=%d ok=%t", cmd, argc, ok)
			}
			for i, want := range tc.args {
				if got := string(args[i]); got != want {
					t.Fatalf("arg[%d]=%q want=%q", i, got, want)
				}
			}
			if reader.Buffered() != 0 {
				t.Fatalf("buffered=%d want=0", reader.Buffered())
			}
		})
	}
}

func TestReadBufferedNativeMutationLeavesComplexFormBuffered(t *testing.T) {
	frame := []byte("*5\r\n$4\r\nZADD\r\n$1\r\nz\r\n$2\r\nNX\r\n$1\r\n1\r\n$1\r\nm\r\n")
	reader := bufio.NewReaderSize(bytes.NewReader(frame), 1024)
	if _, err := reader.Peek(len(frame)); err != nil {
		t.Fatal(err)
	}
	decoder, err := NewDecoder(reader, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var scratch [3][]byte
	_, _, _, ok, err := decoder.ReadBufferedNativeMutation(&scratch)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("optioned ZADD must use generic decoder")
	}
	msg, err := decoder.ReadCommand()
	if err != nil {
		t.Fatal(err)
	}
	if len(msg) != 5 || string(msg[2]) != "NX" {
		t.Fatalf("fallback command=%q", msg)
	}
}


func TestReadBufferedSADD(t *testing.T) {
	raw := "*3\r\n$4\r\nSADD\r\n$3\r\nkey\r\n$6\r\nmember\r\n"
	reader := bufio.NewReaderSize(strings.NewReader(raw), 1024)
	if _, err := reader.Peek(len(raw)); err != nil {
		t.Fatal(err)
	}
	decoder, err := NewDecoder(reader, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	key, member, ok, err := decoder.ReadBufferedSADD(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected buffered SADD")
	}
	if string(key) != "key" || string(member) != "member" {
		t.Fatalf("key=%q member=%q", key, member)
	}
	if reader.Buffered() != 0 {
		t.Fatalf("buffered=%d", reader.Buffered())
	}
}
