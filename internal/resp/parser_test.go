package resp

import (
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
