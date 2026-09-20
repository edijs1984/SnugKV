package resp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadBufferedGETUsesReusableKeyScratch(t *testing.T) {
	wire := "*2\r\n$3\r\nGET\r\n$5\r\na\x00bcd\r\n*1\r\n$4\r\nPING\r\n"
	reader := bufio.NewReader(strings.NewReader(wire))
	d, _ := NewDecoder(reader, DefaultLimits())

	// Prime the reader so the conservative fast path sees an already-buffered
	// complete frame, matching pipelined TCP operation after the first read.
	if _, err := reader.Peek(1); err != nil {
		t.Fatal(err)
	}

	scratch := make([]byte, 0, 16)
	key, ok, err := d.ReadBufferedGET(scratch)
	if err != nil || !ok {
		t.Fatalf("buffered GET ok=%t err=%v", ok, err)
	}
	if !bytes.Equal(key, []byte{'a', 0, 'b', 'c', 'd'}) {
		t.Fatalf("buffered GET key=%q", key)
	}
	if len(key) > 0 && &key[0] != &scratch[:cap(scratch)][0] {
		t.Fatal("buffered GET did not reuse caller scratch")
	}

	next, err := d.ReadCommand()
	if err != nil || len(next) != 1 || string(next[0]) != "PING" {
		t.Fatalf("next command=%q err=%v", next, err)
	}
}

func TestReadBufferedGETFallsBackWithoutConsuming(t *testing.T) {
	for _, wire := range []string{
		"*2\r\n$3\r\nSET\r\n$1\r\nk\r\n",
		"*2\r\n$3\r\nGET\r\n$5\r\nabc",
	} {
		reader := bufio.NewReader(strings.NewReader(wire))
		d, _ := NewDecoder(reader, DefaultLimits())
		if _, err := reader.Peek(1); err != nil {
			t.Fatal(err)
		}
		before := reader.Buffered()
		key, ok, err := d.ReadBufferedGET(nil)
		if err != nil || ok || len(key) != 0 {
			t.Fatalf("wire %q key=%q ok=%t err=%v", wire, key, ok, err)
		}
		if got := reader.Buffered(); got != before {
			t.Fatalf("wire %q consumed bytes: before=%d after=%d", wire, before, got)
		}
	}
}

func TestStreamingBinaryPipeline(t *testing.T) {
	wire := "*3\r\n$3\r\nSET\r\n$0\r\n\r\n$5\r\na\r\n\x00b\r\n*1\r\n$4\r\nPING\r\n"
	d, _ := NewDecoder(bufio.NewReader(iotest.OneByteReader(strings.NewReader(wire))), DefaultLimits())
	got, err := d.ReadCommand()
	want := [][]byte{[]byte("SET"), {}, []byte("a\r\n\x00b")}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = d.ReadCommand()
	if err != nil || string(got[0]) != "PING" {
		t.Fatalf("pipeline: %q %v", got, err)
	}
	if _, err = d.ReadCommand(); err != io.EOF {
		t.Fatalf("end: %v", err)
	}
}
func TestMalformedCommands(t *testing.T) {
	for _, wire := range []string{
		"*0\r\n", "*-1\r\n", "*+1\r\n", "*1\n", "*1\rX", "*\r\n",
		"*1\r\n$-1\r\n", "*1\r\n+PING\r\n", "*1\r\n:1\r\n",
		"*1\r\n*1\r\n$4\r\nPING\r\n", "$4\r\nPING\r\n",
		"*1\r\n$4\r\nPINGxx", "*99999999999999999999999999999\r\n",
		"*1\r\n$999999999999999999999999999\r\n", "*" + strings.Repeat("0", 21) + "1\r\n",
	} {
		t.Run(fmt.Sprintf("%q", wire), func(t *testing.T) {
			d, _ := NewDecoder(bufio.NewReader(strings.NewReader(wire)), DefaultLimits())
			if _, err := d.ReadCommand(); err == nil {
				t.Fatal("accepted invalid command")
			}
		})
	}
}
func TestTruncatedCommand(t *testing.T) {
	wire := "*2\r\n$4\r\nECHO\r\n$3\r\nx\x00y\r\n"
	for n := 1; n < len(wire); n++ {
		d, _ := NewDecoder(bufio.NewReader(strings.NewReader(wire[:n])), DefaultLimits())
		if _, err := d.ReadCommand(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("prefix %d: %v", n, err)
		}
	}
}
func TestLimits(t *testing.T) {
	wire := "*1\r\n$4\r\nPING\r\n"
	for _, tc := range []struct {
		limits Limits
		valid  bool
	}{
		{Limits{len(wire), 4, 1}, true}, {Limits{len(wire) - 1, 4, 1}, false},
		{Limits{len(wire), 3, 1}, false},
	} {
		d, err := NewDecoder(bufio.NewReader(strings.NewReader(wire)), tc.limits)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.ReadCommand()
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	for _, limits := range []Limits{{}, {4, 5, 1}, {4, -1, 1}, {4, 0, 0}} {
		if _, err := NewDecoder(bufio.NewReader(strings.NewReader("")), limits); err == nil {
			t.Fatal("invalid limits accepted")
		}
	}
	d, _ := NewDecoder(bufio.NewReader(strings.NewReader("*2\r\n")), Limits{100, 10, 1})
	if _, err := d.ReadCommand(); err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("argument cap not enforced: %v", err)
	}
}
func TestParseRejectsTrailingBytes(t *testing.T) {
	if _, err := Parse([]byte("*1\r\n$4\r\nPING\r\nx")); err == nil {
		t.Fatal("trailing bytes accepted")
	}
}
func FuzzReadCommand(f *testing.F) {
	for _, seed := range []string{"", "*1\r\n$4\r\nPING\r\n", "*1\r\n$0\r\n\r\n", "*1\r\n*1\r\n", "*99999999999999999999\r\n", "*1\r\n$3\r\n\x00\r\n\r\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		d, _ := NewDecoder(bufio.NewReader(bytes.NewReader(data)), Limits{4096, 2048, 32})
		args, err := d.ReadCommand()
		if err != nil {
			return
		}
		var wire bytes.Buffer
		fmt.Fprintf(&wire, "*%d\r\n", len(args))
		for _, arg := range args {
			fmt.Fprintf(&wire, "$%d\r\n", len(arg))
			wire.Write(arg)
			wire.WriteString("\r\n")
		}
		got, err := Parse(wire.Bytes())
		if err != nil || !reflect.DeepEqual(args, got) {
			t.Fatalf("round trip: %q %v", got, err)
		}
	})
}
func BenchmarkStreamingCommand(b *testing.B) {
	wire := []byte("*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$5\r\na\r\n\x00b\r\n")
	source := bytes.NewReader(wire)
	reader := bufio.NewReader(source)
	d, _ := NewDecoder(reader, DefaultLimits())
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	for i := 0; i < b.N; i++ {
		source.Reset(wire)
		reader.Reset(source)
		if _, err := d.ReadCommand(); err != nil {
			b.Fatal(err)
		}
	}
}
