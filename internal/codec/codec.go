// Package codec contains deterministic, self-contained value representations.
package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"snugkv/internal/codec/jsonshape"
	"strconv"
	"time"
)

type ID uint8

const (
	Raw             ID = 0
	Integer         ID = 1
	UnsignedInteger ID = 2
	UUID            ID = 3
	Timestamp       ID = 4
	Float64         ID = 6
	Boolean         ID = 7
	ULID            ID = 12
)

type Record struct {
	Schema    *jsonshape.Schema
	ID        ID
	RawLength int
	Data      []byte
}
type Codec interface {
	ID() ID
	Name() string
	Encode([]byte) ([]byte, bool)
	Decode([]byte, int) ([]byte, error)
}
type Registry struct {
	codecs map[ID]Codec
	order  []Codec
}

func NewRegistry() *Registry {
	r := &Registry{codecs: make(map[ID]Codec)}
	for _, c := range []Codec{
		rawCodec{},
		integerCodec{},
		unsignedIntegerCodec{},
		uuidCodec{},
		ulidCodec{},
		timestampCodec{},
		float64Codec{},
		boolCodec{},
		repeatByteCodec{},
		lz4Codec{},
		zstdCodec{},
		periodicCodec{},
		hexCodec{},
		base58Codec{},
		bigDecimalCodec{},
	} {
		if err := r.Register(c); err != nil {
			panic(err)
		}
	}
	return r
}
func (r *Registry) Register(c Codec) error {
	if _, ok := r.codecs[c.ID()]; ok {
		return errors.New("duplicate codec ID")
	}
	r.codecs[c.ID()] = c
	r.order = append(r.order, c)
	return nil
}
func (r *Registry) Name(id ID) string {
	if id == 5 {
		return "json-shape"
	}
	if c, ok := r.codecs[id]; ok {
		return c.Name()
	}
	return "unknown"
}
func (r *Registry) Encode(src []byte) Record {
	// Canonical UUID text has an exact 36-byte shape. Handle it before creating
	// the RAW fallback or trying unrelated scalar parsers. uuidCodec.Encode
	// performs the full canonical validation, so a successful result is already
	// safe to publish and does not need an encode->decode verification round trip.
	if len(src) == 36 {
		if data, ok := (uuidCodec{}).Encode(src); ok {
			return Record{ID: UUID, RawLength: len(src), Data: data}
		}
	}

	// Canonical ULID text is exactly 26 uppercase Crockford Base32 characters.
	// Like UUID, this is a terminal 128-bit identifier representation: validate
	// and encode directly instead of cloning RAW data and scanning scalar codecs.
	if len(src) == 26 {
		if data, ok := (ulidCodec{}).Encode(src); ok {
			return Record{ID: ULID, RawLength: len(src), Data: data}
		}
	}

	// Canonical INT64 strings are extremely common cache/counter values. Detect
	// them before cloning the RAW fallback and avoid the generic encode/decode
	// verification loop: canonicalInt64Bytes performs exact syntax and overflow
	// validation, and varint encoding is self-contained and deterministic.
	if len(src) <= 20 {
		if n, ok := canonicalInt64Bytes(src); ok {
			var buf [10]byte
			size := binary.PutVarint(buf[:], n)
			if size < len(src) {
				return Record{ID: Integer, RawLength: len(src), Data: bytes.Clone(buf[:size])}
			}
		}
	}

	// Hex hashes and addresses are exact binary values written in text and
	// convert cheaply. Base58 and big decimal are left to the optimizer.
	if rec, ok := r.EncodeHexForeground(src); ok {
		return rec
	}

	best := Record{ID: Raw, RawLength: len(src), Data: bytes.Clone(src)}

	// No synchronous scalar codec can represent a canonical value longer
	// than a UUID (36 bytes). Larger values are handled by background
	// compression/shape optimization instead of paying scalar parse costs.
	if len(src) > 36 {
		return best
	}

	// JSON objects and arrays cannot be represented by the cheap scalar
	// codecs below. Avoid feeding them through integer/float/UUID/timestamp
	// parsers just to reject them. JSON-shape and compression are evaluated
	// separately by the background optimizer.
	if len(src) != 0 && (src[0] == '{' || src[0] == '[') {
		return best
	}

	for _, c := range r.order {
		if c.ID() == Raw || c.ID() == Integer || c.ID() == UUID || c.ID() == 5 || c.ID() >= RepeatByte {
			continue
		}
		data, ok := c.Encode(src)
		if !ok || len(data) >= len(best.Data) {
			continue
		}
		decoded, err := c.Decode(data, len(src))
		if err != nil || !bytes.Equal(decoded, src) {
			continue
		}
		best = Record{ID: c.ID(), RawLength: len(src), Data: data}
	}
	return best
}
type decodeIntoCodec interface {
	DecodeInto([]byte, int, []byte) ([]byte, error)
}

func (r *Registry) DecodeInto(rec Record, max int, dst []byte) ([]byte, error) {
	if rec.RawLength < 0 || rec.RawLength > max {
		return nil, errors.New("decoded length exceeds limit")
	}

	// Keep the hottest decode paths free of map lookup and interface assertion
	// overhead. These codecs are stateless; direct dispatch is equivalent to
	// retrieving the registered implementation.
	var (
		out []byte
		err error
	)
	switch rec.ID {
	case Raw:
		out, err = (rawCodec{}).DecodeInto(rec.Data, rec.RawLength, dst)
	case UUID:
		out, err = (uuidCodec{}).DecodeInto(rec.Data, rec.RawLength, dst)
	case ULID:
		out, err = (ulidCodec{}).DecodeInto(rec.Data, rec.RawLength, dst)
	case RepeatByte:
		out, err = (repeatByteCodec{}).DecodeInto(rec.Data, rec.RawLength, dst)
	case LZ4:
		out, err = (lz4Codec{}).DecodeInto(rec.Data, rec.RawLength, dst)
	case Zstandard:
		out, err = (zstdCodec{}).DecodeInto(rec.Data, rec.RawLength, dst)
	case Periodic:
		out, err = (periodicCodec{}).DecodeInto(rec.Data, rec.RawLength, dst)
	case 5:
		out, err = jsonshape.DecodeInto(rec.Schema, rec.Data, rec.RawLength, dst)
	default:
		c, ok := r.codecs[rec.ID]
		if !ok {
			return nil, errors.New("unknown codec ID")
		}
		if into, ok := c.(decodeIntoCodec); ok {
			out, err = into.DecodeInto(rec.Data, rec.RawLength, dst)
		} else {
			return r.Decode(rec, max)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(out) != rec.RawLength {
		return nil, errors.New("decoded length mismatch")
	}
	return out, nil
}

func (r *Registry) Decode(rec Record, max int) ([]byte, error) {
	if rec.RawLength < 0 || rec.RawLength > max {
		return nil, errors.New("decoded length exceeds limit")
	}
	if rec.ID == 5 {
		out, err := jsonshape.Decode(rec.Schema, rec.Data, rec.RawLength)
		if err == nil && len(out) != rec.RawLength {
			return nil, errors.New("decoded length mismatch")
		}
		return out, err
	}
	c, ok := r.codecs[rec.ID]
	if !ok {
		return nil, errors.New("unknown codec ID")
	}
	out, err := c.Decode(rec.Data, rec.RawLength)
	if err != nil {
		return nil, err
	}
	if len(out) != rec.RawLength {
		return nil, errors.New("decoded length mismatch")
	}
	return out, nil
}

type rawCodec struct{}

func (rawCodec) ID() ID                           { return Raw }
func (rawCodec) Name() string                     { return "raw" }
func (rawCodec) Encode(src []byte) ([]byte, bool) { return bytes.Clone(src), true }
func (rawCodec) Decode(src []byte, n int) ([]byte, error) {
	if len(src) != n {
		return nil, errors.New("invalid raw length")
	}
	return bytes.Clone(src), nil
}
func (rawCodec) DecodeInto(src []byte, n int, dst []byte) ([]byte, error) {
	if len(src) != n {
		return nil, errors.New("invalid raw length")
	}
	if cap(dst) < n {
		dst = make([]byte, n)
	} else {
		dst = dst[:n]
	}
	copy(dst, src)
	return dst, nil
}

func canonicalInt64Bytes(src []byte) (int64, bool) {
	if len(src) == 0 {
		return 0, false
	}

	negative := src[0] == '-'
	start := 0
	if negative {
		if len(src) == 1 {
			return 0, false
		}
		start = 1
	} else if src[0] == '+' {
		return 0, false
	}

	if src[start] == '0' && len(src)-start > 1 {
		return 0, false
	}

	limit := uint64(math.MaxInt64)
	if negative {
		limit++
	}

	var magnitude uint64
	for _, b := range src[start:] {
		if b < '0' || b > '9' {
			return 0, false
		}
		digit := uint64(b - '0')
		if magnitude > (limit-digit)/10 {
			return 0, false
		}
		magnitude = magnitude*10 + digit
	}

	if negative {
		if magnitude == 0 {
			return 0, false
		}
		if magnitude == uint64(math.MaxInt64)+1 {
			return math.MinInt64, true
		}
		return -int64(magnitude), true
	}
	return int64(magnitude), true
}

type integerCodec struct{}

func (integerCodec) ID() ID       { return Integer }
func (integerCodec) Name() string { return "integer" }
func (integerCodec) Encode(src []byte) ([]byte, bool) {
	n, ok := canonicalInt64Bytes(src)
	if !ok {
		return nil, false
	}
	var buf [10]byte
	size := binary.PutVarint(buf[:], n)
	return bytes.Clone(buf[:size]), true
}
func (integerCodec) Decode(src []byte, _ int) ([]byte, error) {
	n, size := binary.Varint(src)
	if size <= 0 || size != len(src) {
		return nil, errors.New("invalid integer")
	}
	return []byte(strconv.FormatInt(n, 10)), nil
}
func (integerCodec) DecodeInto(src []byte, _ int, dst []byte) ([]byte, error) {
	n, size := binary.Varint(src)
	if size <= 0 || size != len(src) {
		return nil, errors.New("invalid integer")
	}
	return strconv.AppendInt(dst[:0], n, 10), nil
}

type unsignedIntegerCodec struct{}

func (unsignedIntegerCodec) ID() ID {
	return UnsignedInteger
}

func (unsignedIntegerCodec) Name() string {
	return "unsigned-integer"
}

func (unsignedIntegerCodec) Encode(src []byte) ([]byte, bool) {
	if len(src) == 0 {
		return nil, false
	}

	n, err := strconv.ParseUint(string(src), 10, 64)
	if err != nil {
		return nil, false
	}

	// Only canonical unsigned decimal strings are eligible.
	//
	// Reject:
	//   00123
	//   +123
	//   00
	if strconv.FormatUint(n, 10) != string(src) {
		return nil, false
	}

	// Signed INT64 values already have their own codec.
	// UINT64 is reserved for values above MaxInt64.
	if n <= uint64(^uint64(0)>>1) {
		return nil, false
	}

	out := make([]byte, 8)
	binary.LittleEndian.PutUint64(out, n)

	return out, true
}

func (unsignedIntegerCodec) Decode(
	src []byte,
	_ int,
) ([]byte, error) {
	if len(src) != 8 {
		return nil, errors.New("invalid unsigned integer")
	}

	n := binary.LittleEndian.Uint64(src)

	// This codec must never contain values representable as INT64.
	if n <= uint64(^uint64(0)>>1) {
		return nil, errors.New("invalid unsigned integer range")
	}

	return []byte(strconv.FormatUint(n, 10)), nil
}
func (unsignedIntegerCodec) DecodeInto(src []byte, _ int, dst []byte) ([]byte, error) {
	if len(src) != 8 {
		return nil, errors.New("invalid unsigned integer")
	}
	n := binary.LittleEndian.Uint64(src)
	if n <= uint64(^uint64(0)>>1) {
		return nil, errors.New("invalid unsigned integer range")
	}
	return strconv.AppendUint(dst[:0], n, 10), nil
}

type float64Codec struct{}

func (float64Codec) ID() ID       { return Float64 }
func (float64Codec) Name() string { return "float64" }

func canonicalFloat64(src []byte) (float64, bool) {
	if len(src) == 0 {
		return 0, false
	}

	text := string(src)

	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}

	// The binary float must be able to reconstruct the exact textual form.
	//
	// This intentionally rejects alternate textual representations such as:
	//   1.0
	//   1.00
	//   1e3
	//   1E+20
	//   +1.5
	//
	// even when they represent the same numeric value.
	if strconv.FormatFloat(n, 'g', -1, 64) != text {
		return 0, false
	}

	return n, true
}

func (float64Codec) Encode(src []byte) ([]byte, bool) {
	n, ok := canonicalFloat64(src)
	if !ok {
		return nil, false
	}

	// Canonical integers have dedicated INT64 / UINT64 codecs.
	// Avoid treating their canonical decimal form as FLOAT64.
	if i, err := strconv.ParseInt(string(src), 10, 64); err == nil &&
		strconv.FormatInt(i, 10) == string(src) {
		return nil, false
	}

	if u, err := strconv.ParseUint(string(src), 10, 64); err == nil &&
		strconv.FormatUint(u, 10) == string(src) {
		return nil, false
	}

	out := make([]byte, 8)
	binary.LittleEndian.PutUint64(out, math.Float64bits(n))

	return out, true
}

func (float64Codec) Decode(src []byte, _ int) ([]byte, error) {
	if len(src) != 8 {
		return nil, errors.New("invalid float64")
	}

	n := math.Float64frombits(binary.LittleEndian.Uint64(src))

	if math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, errors.New("invalid float64 value")
	}

	return []byte(strconv.FormatFloat(n, 'g', -1, 64)), nil
}
func (float64Codec) DecodeInto(src []byte, _ int, dst []byte) ([]byte, error) {
	if len(src) != 8 {
		return nil, errors.New("invalid float64")
	}
	n := math.Float64frombits(binary.LittleEndian.Uint64(src))
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, errors.New("invalid float64 value")
	}
	return strconv.AppendFloat(dst[:0], n, 'g', -1, 64), nil
}

type boolCodec struct{}

func (boolCodec) ID() ID       { return Boolean }
func (boolCodec) Name() string { return "bool" }

func (boolCodec) Encode(src []byte) ([]byte, bool) {
	switch string(src) {
	case "false", "true":
		// No payload is required. RawLength distinguishes the exact
		// canonical values:
		//   true  -> 4
		//   false -> 5
		return nil, true
	default:
		return nil, false
	}
}

func (boolCodec) Decode(src []byte, rawLength int) ([]byte, error) {
	if len(src) != 0 {
		return nil, errors.New("invalid bool payload")
	}

	switch rawLength {
	case 4:
		return []byte("true"), nil
	case 5:
		return []byte("false"), nil
	default:
		return nil, errors.New("invalid bool length")
	}
}

type uuidCodec struct{}

func (uuidCodec) ID() ID       { return UUID }
func (uuidCodec) Name() string { return "uuid" }

func uuidHexNibble(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	default:
		return 0, false
	}
}

func (uuidCodec) Encode(src []byte) ([]byte, bool) {
	if len(src) != 36 ||
		src[8] != '-' || src[13] != '-' || src[18] != '-' || src[23] != '-' {
		return nil, false
	}

	out := make([]byte, 16)
	si := 0
	for oi := 0; oi < len(out); oi++ {
		for si == 8 || si == 13 || si == 18 || si == 23 {
			si++
		}
		hi, ok := uuidHexNibble(src[si])
		if !ok {
			return nil, false
		}
		lo, ok := uuidHexNibble(src[si+1])
		if !ok {
			return nil, false
		}
		out[oi] = hi<<4 | lo
		si += 2
	}
	return out, true
}

func appendUUIDHex(dst []byte, b byte) []byte {
	const digits = "0123456789abcdef"
	return append(dst, digits[b>>4], digits[b&0x0f])
}

func (uuidCodec) Decode(src []byte, _ int) ([]byte, error) {
	if len(src) != 16 {
		return nil, errors.New("invalid UUID")
	}
	out := make([]byte, 0, 36)
	for i, b := range src {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = appendUUIDHex(out, b)
	}
	return out, nil
}

func (uuidCodec) DecodeInto(src []byte, _ int, dst []byte) ([]byte, error) {
	if len(src) != 16 {
		return nil, errors.New("invalid UUID")
	}
	if cap(dst) < 36 {
		dst = make([]byte, 0, 36)
	} else {
		dst = dst[:0]
	}
	for i, b := range src {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			dst = append(dst, '-')
		}
		dst = appendUUIDHex(dst, b)
	}
	return dst, nil
}


type ulidCodec struct{}

func (ulidCodec) ID() ID       { return ULID }
func (ulidCodec) Name() string { return "ulid" }

const ulidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func ulidDecodeValue(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'A' && b <= 'H':
		return b - 'A' + 10, true
	case b >= 'J' && b <= 'K':
		return b - 'J' + 18, true
	case b >= 'M' && b <= 'N':
		return b - 'M' + 20, true
	case b >= 'P' && b <= 'T':
		return b - 'P' + 22, true
	case b >= 'V' && b <= 'Z':
		return b - 'V' + 27, true
	default:
		return 0, false
	}
}

func (ulidCodec) Encode(src []byte) ([]byte, bool) {
	if len(src) != 26 {
		return nil, false
	}

	first, ok := ulidDecodeValue(src[0])
	if !ok || first > 7 {
		return nil, false
	}

	out := make([]byte, 16)
	var acc uint32 = uint32(first)
	bits := 3
	oi := 0

	for i := 1; i < len(src); i++ {
		v, ok := ulidDecodeValue(src[i])
		if !ok {
			return nil, false
		}
		acc = (acc << 5) | uint32(v)
		bits += 5

		for bits >= 8 {
			bits -= 8
			out[oi] = byte(acc >> bits)
			oi++
			if bits == 0 {
				acc = 0
			} else {
				acc &= (1 << bits) - 1
			}
		}
	}

	if oi != 16 || bits != 0 {
		return nil, false
	}
	return out, true
}

func appendULIDEncoded(dst []byte, src []byte) []byte {
	var acc uint32
	bits := 2 // ULID text has two leading zero padding bits: 26*5 = 130.
	for _, b := range src {
		acc = (acc << 8) | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			dst = append(dst, ulidAlphabet[(acc>>bits)&31])
			if bits == 0 {
				acc = 0
			} else {
				acc &= (1 << bits) - 1
			}
		}
	}
	return dst
}

func (ulidCodec) Decode(src []byte, _ int) ([]byte, error) {
	if len(src) != 16 {
		return nil, errors.New("invalid ULID")
	}
	out := make([]byte, 0, 26)
	out = appendULIDEncoded(out, src)
	if len(out) != 26 {
		return nil, errors.New("invalid ULID encoding")
	}
	return out, nil
}

func (ulidCodec) DecodeInto(src []byte, _ int, dst []byte) ([]byte, error) {
	if len(src) != 16 {
		return nil, errors.New("invalid ULID")
	}
	if cap(dst) < 26 {
		dst = make([]byte, 0, 26)
	} else {
		dst = dst[:0]
	}
	dst = appendULIDEncoded(dst, src)
	if len(dst) != 26 {
		return nil, errors.New("invalid ULID encoding")
	}
	return dst, nil
}

type timestampCodec struct{}

func (timestampCodec) ID() ID       { return Timestamp }
func (timestampCodec) Name() string { return "timestamp" }

const timestampLayout = "2006-01-02T15:04:05Z"

func (timestampCodec) Encode(src []byte) ([]byte, bool) {
	if len(src) != 20 {
		return nil, false
	}
	t, err := time.Parse(timestampLayout, string(src))
	if err != nil || t.Format(timestampLayout) != string(src) {
		return nil, false
	}
	out := make([]byte, 8)
	binary.LittleEndian.PutUint64(out, uint64(t.Unix()))
	return out, true
}
func (timestampCodec) Decode(src []byte, _ int) ([]byte, error) {
	if len(src) != 8 {
		return nil, errors.New("invalid timestamp")
	}
	t := time.Unix(int64(binary.LittleEndian.Uint64(src)), 0).UTC()
	if t.Year() < 0 || t.Year() > 9999 {
		return nil, errors.New("timestamp out of range")
	}
	return []byte(t.Format(timestampLayout)), nil
}
func (timestampCodec) DecodeInto(src []byte, _ int, dst []byte) ([]byte, error) {
	if len(src) != 8 {
		return nil, errors.New("invalid timestamp")
	}
	t := time.Unix(int64(binary.LittleEndian.Uint64(src)), 0).UTC()
	if t.Year() < 0 || t.Year() > 9999 {
		return nil, errors.New("timestamp out of range")
	}
	return t.AppendFormat(dst[:0], timestampLayout), nil
}
