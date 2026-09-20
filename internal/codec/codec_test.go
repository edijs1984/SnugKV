package codec

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

func TestExactCodecs(t *testing.T) {
	r := NewRegistry()
	for _, tc := range []struct {
		s  string
		id ID
	}{{"123456789", Integer}, {"0042", Raw}, {"-0", Raw}, {"+1", Raw}, {" 1", Raw}, {"9223372036854775808", UnsignedInteger}, {"550e8400-e29b-41d4-a716-446655440000", UUID}, {"550E8400-E29B-41D4-A716-446655440000", Raw}, {"2026-09-07T12:34:56Z", Timestamp}, {"2026-09-07T12:34:56.000Z", Raw}, {"", Raw}} {
		rec := r.Encode([]byte(tc.s))
		if rec.ID != tc.id {
			t.Errorf("%q ID %d", tc.s, rec.ID)
		}
		got, err := r.Decode(rec, 1024)
		if err != nil || string(got) != tc.s {
			t.Fatalf("%q %v", got, err)
		}
	}
}
func FuzzExactRoundTrip(f *testing.F) {
	for _, v := range []string{"", "0042", "-9223372036854775808", "550e8400-e29b-41d4-a716-446655440000", "2026-09-07T12:34:56Z", "\x00\xff"} {
		f.Add([]byte(v))
	}
	r := NewRegistry()
	f.Fuzz(func(t *testing.T, v []byte) {
		rec := r.Encode(v)
		got, err := r.Decode(rec, len(v))
		if err != nil || !bytes.Equal(v, got) {
			t.Fatalf("round trip failed %v", err)
		}
	})
}
func FuzzDecode(f *testing.F) {
	f.Add(uint8(1), []byte{128}, int64(10))
	r := NewRegistry()
	f.Fuzz(func(t *testing.T, id uint8, data []byte, length int64) {
		if length < 0 || length > 4096 {
			return
		}
		out, err := r.Decode(Record{ID: ID(id), RawLength: int(length), Data: data}, 4096)
		if err == nil && len(out) != int(length) {
			t.Fatal("length mismatch")
		}
	})
}
func TestDecodeLimits(t *testing.T) {
	r := NewRegistry()
	for _, rec := range []Record{{ID: 99}, {ID: Raw, RawLength: 100, Data: []byte("x")}, {ID: Integer, RawLength: 1, Data: []byte{128}}, {ID: UUID, RawLength: 36}, {ID: Timestamp, RawLength: 20}} {
		if _, err := r.Decode(rec, 64); err == nil {
			t.Fatal("accepted corrupt record")
		}
	}
}

func TestGeneralCompression(t *testing.T) {
	r := NewRegistry()
	value := bytes.Repeat([]byte("long repeated value "), 1024)
	for _, id := range []ID{LZ4, Zstandard} {
		c := r.codecs[id]
		data, ok := c.Encode(value)
		if !ok {
			t.Fatalf("codec %d rejected", id)
		}
		rec := Record{ID: id, RawLength: len(value), Data: data}
		out, err := r.Decode(rec, len(value))
		if err != nil || !bytes.Equal(out, value) {
			t.Fatalf("codec %d: %v", id, err)
		}
		rec.RawLength = 1
		if _, err = r.Decode(rec, 1); err == nil {
			t.Fatal("decoder exceeded output bound")
		}
	}
}

func TestBooleanUsesZeroPayload(t *testing.T) {
	r := NewRegistry()

	for _, value := range []string{"true", "false"} {
		rec := r.Encode([]byte(value))

		if rec.ID != Boolean {
			t.Fatalf("%q codec = %s, want bool", value, r.Name(rec.ID))
		}

		if len(rec.Data) != 0 {
			t.Fatalf("%q encoded bytes = %d, want 0", value, len(rec.Data))
		}

		got, err := r.Decode(rec, len(value))
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != value {
			t.Fatalf("got %q want %q", got, value)
		}
	}
}

func TestUnsignedIntegerUsesEightBytes(t *testing.T) {
	r := NewRegistry()

	values := []string{
		"9223372036854775808",
		"18446744073709551615",
	}

	for _, value := range values {
		rec := r.Encode([]byte(value))

		if rec.ID != UnsignedInteger {
			t.Fatalf("%q codec = %s, want unsigned-integer", value, r.Name(rec.ID))
		}

		if len(rec.Data) != 8 {
			t.Fatalf("%q encoded bytes = %d, want 8", value, len(rec.Data))
		}

		got, err := r.Decode(rec, len(value))
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != value {
			t.Fatalf("got %q want %q", got, value)
		}
	}
}

func TestBooleanRejectsInvalidStoredForms(t *testing.T) {
	r := NewRegistry()

	for _, rec := range []Record{
		{ID: Boolean, RawLength: 4, Data: []byte{1}},
		{ID: Boolean, RawLength: 3},
		{ID: Boolean, RawLength: 6},
	} {
		if _, err := r.Decode(rec, 16); err == nil {
			t.Fatalf("accepted invalid bool record: %+v", rec)
		}
	}
}


func TestGeneralCompressionConcurrent(t *testing.T) {
	r := NewRegistry()
	value := bytes.Repeat([]byte("concurrent compression payload "), 256)

	var wg sync.WaitGroup
	errs := make(chan error, 16)

	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				for _, id := range []ID{LZ4, Zstandard} {
					c := r.codecs[id]
					data, ok := c.Encode(value)
					if !ok {
						errs <- errors.New("compression rejected")
						return
					}
					out, err := c.Decode(data, len(value))
					if err != nil || !bytes.Equal(out, value) {
						if err == nil {
							err = errors.New("compression round trip mismatch")
						}
						errs <- err
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestDecodeIntoReusesRawBuffer(t *testing.T) {
	r := NewRegistry()
	value := []byte("raw decode into")
	rec := Record{ID: Raw, RawLength: len(value), Data: value}
	scratch := make([]byte, 0, len(value))

	out, err := r.DecodeInto(rec, len(value), scratch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, value) {
		t.Fatalf("round trip mismatch: got %q want %q", out, value)
	}
	if len(out) > 0 && &out[0] != &scratch[:cap(scratch)][0] {
		t.Fatal("raw DecodeInto did not reuse provided buffer")
	}
}

func TestDecodeIntoReusesCompressionBuffer(t *testing.T) {
	r := NewRegistry()
	value := bytes.Repeat([]byte("decode-into-reuse-"), 128)

	for _, id := range []ID{LZ4, Zstandard} {
		codec := r.codecs[id]
		data, ok := codec.Encode(value)
		if !ok {
			t.Fatalf("codec %d rejected", id)
		}

		rec := Record{ID: id, RawLength: len(value), Data: data}
		scratch := make([]byte, 0, len(value))
		out, err := r.DecodeInto(rec, len(value), scratch)
		if err != nil {
			t.Fatalf("codec %d: %v", id, err)
		}
		if !bytes.Equal(out, value) {
			t.Fatalf("codec %d round trip mismatch", id)
		}
		if len(out) > 0 && &out[0] != &scratch[:cap(scratch)][0] {
			t.Fatalf("codec %d did not reuse provided buffer", id)
		}
	}
}


func TestEncodeGeneralBorrowedRawAliasesInput(t *testing.T) {
	r := NewRegistry()
	value := make([]byte, 256)
	for i := range value {
		value[i] = byte(i)
	}

	record := r.EncodeGeneralBorrowed(value, false)
	if record.ID != Raw {
		t.Fatalf("codec=%d want raw", record.ID)
	}
	if len(record.Data) == 0 || &record.Data[0] != &value[0] {
		t.Fatal("borrowed raw fallback unexpectedly cloned input")
	}

	owned := r.EncodeGeneral(value, false)
	if owned.ID != Raw {
		t.Fatalf("owned codec=%d want raw", owned.ID)
	}
	if len(owned.Data) == 0 || &owned.Data[0] == &value[0] {
		t.Fatal("ownership-preserving EncodeGeneral aliased input")
	}
}
