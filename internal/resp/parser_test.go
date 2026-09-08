package resp

import (
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
