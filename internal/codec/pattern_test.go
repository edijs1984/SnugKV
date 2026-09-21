package codec

import (
	"bytes"
	"testing"
)

func TestRepeatByteCodecRoundTrip(t *testing.T) {
	r := NewRegistry()
	value := bytes.Repeat([]byte{'x'}, 256)

	record := r.EncodeGeneralBorrowed(value, false)
	if record.ID != RepeatByte {
		t.Fatalf("codec=%s want repeat-byte", r.Name(record.ID))
	}
	if len(record.Data) != 1 {
		t.Fatalf("encoded bytes=%d want 1", len(record.Data))
	}

	got, err := r.Decode(record, len(value))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatal("repeat-byte round trip mismatch")
	}
}

func TestPeriodicCodecBeatsLZ4ForBenchmarkPattern(t *testing.T) {
	r := NewRegistry()
	pattern := []byte("snugkv-benchmark-")
	value := make([]byte, 256)
	offset := 7
	for i := range value {
		value[i] = pattern[(i+offset)%len(pattern)]
	}

	record := r.EncodeGeneralBorrowed(value, false)
	if record.ID != Periodic {
		t.Fatalf("codec=%s want periodic", r.Name(record.ID))
	}
	if len(record.Data) != len(pattern) {
		t.Fatalf("encoded bytes=%d want %d", len(record.Data), len(pattern))
	}

	got, err := r.Decode(record, len(value))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatal("periodic round trip mismatch")
	}
}

func TestPeriodicCodecRejectsNonPeriodicData(t *testing.T) {
	r := NewRegistry()
	value := make([]byte, 256)
	x := uint64(0x9e3779b97f4a7c15)
	for i := range value {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		value[i] = byte(x)
	}

	record := r.EncodeGeneralBorrowed(value, false)
	if record.ID == Periodic || record.ID == RepeatByte {
		t.Fatalf("unexpected specialized codec %s", r.Name(record.ID))
	}

	got, err := r.Decode(record, len(value))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatal("non-periodic round trip mismatch")
	}
}

func TestPeriodicCodecHandlesPartialFinalPeriod(t *testing.T) {
	r := NewRegistry()
	pattern := []byte("abcde")
	value := make([]byte, 257)
	for i := range value {
		value[i] = pattern[i%len(pattern)]
	}

	data, ok := (periodicCodec{}).Encode(value)
	if !ok {
		t.Fatal("periodic codec rejected exact partial-final-period value")
	}
	record := Record{ID: Periodic, RawLength: len(value), Data: data}
	got, err := r.Decode(record, len(value))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatal("partial-period round trip mismatch")
	}
}
