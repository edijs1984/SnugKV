package codec

import (
	"bytes"
	"errors"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

const (
	LZ4       ID = 9
	Zstandard ID = 10
)

type lz4Codec struct{}

func (lz4Codec) ID() ID       { return LZ4 }
func (lz4Codec) Name() string { return "lz4" }
func (lz4Codec) Encode(src []byte) ([]byte, bool) {
	if len(src) < 256 {
		return nil, false
	}
	dst := make([]byte, lz4.CompressBlockBound(len(src)))
	n, err := lz4.CompressBlock(src, dst, nil)
	if err != nil || n == 0 || n*8 > len(src)*7 {
		return nil, false
	}
	return bytes.Clone(dst[:n]), true
}
func (lz4Codec) Decode(src []byte, n int) ([]byte, error) {
	out := make([]byte, n)
	size, err := lz4.UncompressBlock(src, out)
	if err != nil {
		return nil, err
	}
	if size != n {
		return nil, errors.New("LZ4 length mismatch")
	}
	return out, nil
}

type zstdCodec struct{}

func (zstdCodec) ID() ID       { return Zstandard }
func (zstdCodec) Name() string { return "zstd" }
func (zstdCodec) Encode(src []byte) ([]byte, bool) {
	if len(src) < 1024 {
		return nil, false
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithWindowSize(64<<10))
	if err != nil {
		return nil, false
	}
	defer encoder.Close()
	out := encoder.EncodeAll(src, nil)
	if len(out)*5 > len(src)*4 {
		return nil, false
	}
	return out, true
}
func (zstdCodec) Decode(src []byte, n int) ([]byte, error) {
	bound := uint64(n)
	if bound < 1<<20 {
		bound = 1 << 20
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(bound), zstd.WithDecodeAllCapLimit(true))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	out, err := decoder.DecodeAll(src, make([]byte, 0, n))
	if err != nil {
		return nil, err
	}
	if len(out) != n {
		return nil, errors.New("Zstandard length mismatch")
	}
	return out, nil
}
func alreadyCompressed(src []byte) bool {
	for _, sig := range [][]byte{{0x1f, 0x8b}, {0x89, 'P', 'N', 'G'}, {0xff, 0xd8, 0xff}, {0x28, 0xb5, 0x2f, 0xfd}, {0x04, 0x22, 0x4d, 0x18}, {'P', 'K', 3, 4}} {
		if bytes.HasPrefix(src, sig) {
			return true
		}
	}
	return false
}

// EncodeGeneral is called only by the optimizer, never ordinary SET.
func (r *Registry) EncodeGeneral(src []byte, cold bool) Record {
	best := Record{ID: Raw, RawLength: len(src), Data: bytes.Clone(src)}
	if len(src) < 256 || alreadyCompressed(src) {
		return best
	}
	candidates := []ID{LZ4}
	if cold {
		candidates = append(candidates, Zstandard)
	}
	for _, id := range candidates {
		c := r.codecs[id]
		data, ok := c.Encode(src)
		if !ok || len(data) >= len(best.Data) {
			continue
		}
		out, err := c.Decode(data, len(src))
		if err == nil && bytes.Equal(out, src) {
			best = Record{ID: id, RawLength: len(src), Data: data}
		}
	}
	return best
}
