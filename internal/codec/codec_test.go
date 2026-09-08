package codec

import (
	"bytes"
	"testing"
)

func TestExactCodecs(t *testing.T) {
	r := NewRegistry()
	for _, tc := range []struct {
		s  string
		id ID
	}{{"123456789", Integer}, {"0042", Raw}, {"-0", Raw}, {"+1", Raw}, {" 1", Raw}, {"9223372036854775808", Raw}, {"550e8400-e29b-41d4-a716-446655440000", UUID}, {"550E8400-E29B-41D4-A716-446655440000", Raw}, {"2026-09-07T12:34:56Z", Timestamp}, {"2026-09-07T12:34:56.000Z", Raw}, {"", Raw}} {
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
