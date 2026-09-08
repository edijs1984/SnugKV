// Package codec contains deterministic, self-contained value representations.
package codec

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"morphcache/internal/codec/jsonshape"
	"strconv"
	"time"
)

type ID uint8

const (
	Raw       ID = 0
	Integer   ID = 1
	UUID      ID = 3
	Timestamp ID = 4
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
	for _, c := range []Codec{rawCodec{}, integerCodec{}, uuidCodec{}, timestampCodec{}, lz4Codec{}, zstdCodec{}} {
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
	best := Record{ID: Raw, RawLength: len(src), Data: bytes.Clone(src)}
	for _, c := range r.order {
		if c.ID() == Raw || c.ID() >= 5 {
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

type integerCodec struct{}

func (integerCodec) ID() ID       { return Integer }
func (integerCodec) Name() string { return "integer" }
func (integerCodec) Encode(src []byte) ([]byte, bool) {
	n, err := strconv.ParseInt(string(src), 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != string(src) {
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

type uuidCodec struct{}

func (uuidCodec) ID() ID       { return UUID }
func (uuidCodec) Name() string { return "uuid" }
func (uuidCodec) Encode(src []byte) ([]byte, bool) {
	if len(src) != 36 {
		return nil, false
	}
	out := make([]byte, 0, 32)
	for i, b := range src {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if b != '-' {
				return nil, false
			}
			continue
		}
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return nil, false
		}
		out = append(out, b)
	}
	decoded := make([]byte, 16)
	if _, err := hex.Decode(decoded, out); err != nil {
		return nil, false
	}
	return decoded, true
}
func (uuidCodec) Decode(src []byte, _ int) ([]byte, error) {
	if len(src) != 16 {
		return nil, errors.New("invalid UUID")
	}
	out := make([]byte, 36)
	digits := make([]byte, 32)
	hex.Encode(digits, src)
	j := 0
	for i := range out {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			out[i] = '-'
		} else {
			out[i] = digits[j]
			j++
		}
	}
	return out, nil
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
