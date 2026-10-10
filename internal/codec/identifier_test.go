package codec

import (
	"bytes"
	"crypto/rand"
	"math/big"
	"strings"
	"testing"
)

func mustRoundTrip(t *testing.T, r *Registry, in string, want ID) Record {
	t.Helper()
	rec := r.Encode([]byte(in))
	if rec.ID != want {
		t.Fatalf("%q: codec = %v, want %v", in, rec.ID, want)
	}
	out, err := r.Decode(rec, len(in))
	if err != nil || string(out) != in {
		t.Fatalf("%q: round trip = %q, %v", in, out, err)
	}
	return rec
}

func TestHexRoundTripVariants(t *testing.T) {
	r := NewRegistry()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	lower := bytesToHex(raw, false)
	upper := strings.ToUpper(lower)
	mixed := []byte(lower)
	for i := 0; i < len(mixed); i += 3 {
		if mixed[i] >= 'a' && mixed[i] <= 'f' {
			mixed[i] -= 32
		}
	}
	// make sure mixed really mixes
	mixed[0], mixed[1] = 'a', 'B'

	for _, tc := range []struct {
		name, in string
		max      int
	}{
		{"lower", lower, 33},
		{"lower0x", "0x" + lower, 33},
		{"upper", upper, 33},
		{"upper0x", "0x" + upper, 33},
		{"mixed", string(mixed), 33 + 8},
		{"mixed0x", "0x" + string(mixed), 33 + 8},
	} {
		rec := mustRoundTrip(t, r, tc.in, Hex)
		if len(rec.Data) > tc.max {
			t.Errorf("%s: payload %d > %d", tc.name, len(rec.Data), tc.max)
		}
	}
}

func bytesToHex(b []byte, upper bool) string {
	d := "0123456789abcdef"
	if upper {
		d = "0123456789ABCDEF"
	}
	var sb strings.Builder
	for _, x := range b {
		sb.WriteByte(d[x>>4])
		sb.WriteByte(d[x&15])
	}
	return sb.String()
}

func TestHexRejects(t *testing.T) {
	r := NewRegistry()
	good := strings.Repeat("ab", 20)
	for _, in := range []string{
		good[:len(good)-1],         // odd digits
		good[:30],                  // too short
		good[:38] + "zz",           // non-hex
		"0X" + good,                // uppercase X prefix not supported
		"0x" + good[:len(good)-1],  // odd after prefix
		strings.Repeat("ab", 2100), // over max
		strings.Repeat("0123456789abcdef", 2) + " ", // trailing space
	} {
		if rec := r.Encode([]byte(in)); rec.ID == Hex {
			t.Errorf("%q encoded as hex", in)
		}
	}
}

func TestBase58SolanaShapes(t *testing.T) {
	r := NewRegistry()
	for _, n := range []int{32, 64} {
		for i := 0; i < 200; i++ {
			raw := make([]byte, n)
			rand.Read(raw)
			if i%7 == 0 {
				raw[0], raw[1] = 0, 0 // leading zeros -> leading '1'
			}
			text := string(base58Encode(raw))
			rec := r.Encode([]byte(text))
			if rec.ID != Base58 || len(rec.Data) != n {
				t.Fatalf("n=%d %q: id=%v len=%d", n, text, rec.ID, len(rec.Data))
			}
			out, err := r.Decode(rec, len(text))
			if err != nil || string(out) != text {
				t.Fatalf("round trip %q: %q %v", text, out, err)
			}
		}
	}
	// System program id: 32 zero bytes. It is also a valid decimal, and the
	// smaller of the two exact forms wins.
	mustRoundTrip(t, r, strings.Repeat("1", 32), BigDecimal)
	// Well-known token program.
	mustRoundTrip(t, r, "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Base58)
}

func TestBase58Rejects(t *testing.T) {
	r := NewRegistry()
	for _, in := range []string{
		"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5D0",  // '0' not in alphabet
		"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DAA", // decodes to 33 bytes
		"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5Dl",  // 'l' not in alphabet
	} {
		if rec := r.Encode([]byte(in)); rec.ID == Base58 {
			t.Errorf("%q encoded as base58", in)
		}
	}
}

func TestBigDecimal(t *testing.T) {
	r := NewRegistry()
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	for _, in := range []string{
		"18446744073709551616", // 2^64
		"1000000000000000000000000",
		"115792089237316195423570985008687907853269984665640564039457584007913129639935",
		max.String(),
	} {
		rec := mustRoundTrip(t, r, in, BigDecimal)
		if len(rec.Data) > 32 {
			t.Errorf("%q: payload %d", in, len(rec.Data))
		}
	}
	for _, in := range []string{
		"18446744073709551615",    // fits uint64: integer codecs own it
		"01000000000000000000000", // leading zero
		"115792089237316195423570985008687907853269984665640564039457584007913129639936", // 2^256
		"1000000000000000000000x",
		"-1000000000000000000000",
		"+1000000000000000000000",
	} {
		if rec := r.Encode([]byte(in)); rec.ID == BigDecimal {
			t.Errorf("%q encoded as bigdecimal", in)
		}
	}
}

func TestIdentifierCorruptPayloads(t *testing.T) {
	r := NewRegistry()
	for _, tc := range []struct {
		id  ID
		raw int
		p   []byte
	}{
		{Hex, 66, nil},
		{Hex, 66, []byte{0xff}},
		{Hex, 66, []byte{0x01, 1, 2}},
		{Hex, 10, bytes.Repeat([]byte{1}, 6)},
		{Hex, 66, append([]byte{0x06}, make([]byte, 32)...)}, // case mode 3
		{Base58, 44, make([]byte, 31)},
		{Base58, 10, make([]byte, 32)},
		{BigDecimal, 30, nil},
		{BigDecimal, 30, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
		{BigDecimal, 30, make([]byte, 40)},
	} {
		if _, err := r.Decode(Record{ID: tc.id, RawLength: tc.raw, Data: tc.p}, tc.raw); err == nil {
			t.Errorf("id=%v raw=%d payload=%x decoded without error", tc.id, tc.raw, tc.p)
		}
	}
}

func FuzzIdentifierRoundTrip(f *testing.F) {
	f.Add([]byte("0x" + strings.Repeat("ab", 32)))
	f.Add([]byte(strings.Repeat("AbC1", 12)))
	f.Add([]byte("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"))
	f.Add([]byte("18446744073709551616"))
	r := NewRegistry()
	f.Fuzz(func(t *testing.T, in []byte) {
		rec, ok := r.EncodeIdentifier(in)
		if !ok {
			return
		}
		out, err := r.Decode(rec, len(in))
		if err != nil || !bytes.Equal(out, in) {
			t.Fatalf("%q: id=%v out=%q err=%v", in, rec.ID, out, err)
		}
		if len(rec.Data) >= len(in) {
			t.Fatalf("%q: no saving", in)
		}
	})
}

func FuzzIdentifierDecodeNeverPanics(f *testing.F) {
	f.Add(byte(Hex), 66, []byte{1, 2, 3})
	f.Add(byte(Base58), 44, make([]byte, 32))
	f.Add(byte(BigDecimal), 30, make([]byte, 12))
	r := NewRegistry()
	f.Fuzz(func(t *testing.T, id byte, raw int, p []byte) {
		switch ID(id) {
		case Hex, Base58, BigDecimal:
		default:
			return
		}
		if raw < 0 || raw > 8192 {
			return
		}
		out, err := r.Decode(Record{ID: ID(id), RawLength: raw, Data: p}, 8192)
		if err == nil && len(out) != raw {
			t.Fatalf("decoded %d bytes, raw length %d", len(out), raw)
		}
	})
}

func TestLimbConversionMatchesBigInt(t *testing.T) {
	for i := 0; i < 5000; i++ {
		n := 9 + i%24 // 9..32 bytes
		raw := make([]byte, n)
		rand.Read(raw)
		if raw[0] == 0 {
			raw[0] = 1
		}
		want := new(big.Int).SetBytes(raw).String()
		if got := string(radixEncode(raw, 10, 9, 1_000_000_000, "0123456789")); got != want {
			t.Fatalf("encode %x: %s != %s", raw, got, want)
		}
		back, ok := radixDecode([]byte(want), 10, 9, decimalDigit)
		if !ok || !bytes.Equal(back, raw) {
			t.Fatalf("decode %s: %x ok=%v", want, back, ok)
		}
		b58 := new(big.Int).SetBytes(raw).Text(58)
		_ = b58 // math/big base 58 uses a different alphabet; compare structure below
		text := base58Encode(raw)
		dec, ok := base58Decode(text)
		if !ok || !bytes.Equal(dec, raw) {
			t.Fatalf("base58 %x: %s -> %x", raw, text, dec)
		}
	}
}

func BenchmarkIdentifierDecode(b *testing.B) {
	r := NewRegistry()
	for name, in := range map[string]string{
		"sol-signature": "5VERv8NMvzbJMEkV8xnrLkEaWRtSz9CosKDYjCJjBRnbJLgp8uirBgmQpjKhoR4tjF3ZpRzrFmBV6UjKdiSZkQUW",
		"sol-pubkey":    "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",
		"eth-hash":      "0x" + strings.Repeat("ab12", 16),
		"uint256":       "115792089237316195423570985008687907853269984665640564039457584007913129639935",
	} {
		rec := r.Encode([]byte(in))
		b.Run(name+"/decode", func(b *testing.B) {
			dst := make([]byte, 0, 128)
			for i := 0; i < b.N; i++ {
				out, err := r.DecodeInto(rec, 128, dst)
				if err != nil || len(out) != len(in) {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/encode", func(b *testing.B) {
			src := []byte(in)
			for i := 0; i < b.N; i++ {
				if _, ok := r.EncodeIdentifier(src); !ok {
					b.Fatal("no encode")
				}
			}
		})
	}
}
