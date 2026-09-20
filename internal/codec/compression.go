package codec

import (
	"bytes"
	"errors"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"sync"
)

const (
	LZ4       ID = 9
	Zstandard ID = 10
)

const (
	maxPooledLZ4Scratch = 4 << 20
	zstdDecoderMaxMemory = 64 << 20
)

var lz4ScratchPool = sync.Pool{
	New: func() any {
		return make([]byte, 0, 64<<10)
	},
}

var zstdEncoderPool = sync.Pool{
	New: func() any {
		encoder, err := zstd.NewWriter(
			nil,
			zstd.WithEncoderConcurrency(1),
			zstd.WithEncoderLevel(zstd.SpeedFastest),
			zstd.WithWindowSize(64<<10),
		)
		if err != nil {
			panic(err)
		}
		return encoder
	},
}

var zstdDecoderPool = sync.Pool{
	New: func() any {
		decoder, err := zstd.NewReader(
			nil,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(zstdDecoderMaxMemory),
			zstd.WithDecodeAllCapLimit(true),
		)
		if err != nil {
			panic(err)
		}
		return decoder
	},
}

type lz4Codec struct{}

func (lz4Codec) ID() ID       { return LZ4 }
func (lz4Codec) Name() string { return "lz4" }
func (lz4Codec) Encode(src []byte) ([]byte, bool) {
	if len(src) < 256 {
		return nil, false
	}

	bound := lz4.CompressBlockBound(len(src))
	scratch := lz4ScratchPool.Get().([]byte)
	if cap(scratch) < bound {
		scratch = make([]byte, bound)
	} else {
		scratch = scratch[:bound]
	}

	n, err := lz4.CompressBlock(src, scratch, nil)
	if err != nil || n == 0 || n*8 > len(src)*7 {
		if cap(scratch) <= maxPooledLZ4Scratch {
			lz4ScratchPool.Put(scratch[:0])
		}
		return nil, false
	}

	out := make([]byte, n)
	copy(out, scratch[:n])
	if cap(scratch) <= maxPooledLZ4Scratch {
		lz4ScratchPool.Put(scratch[:0])
	}
	return out, true
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
func (lz4Codec) DecodeInto(src []byte, n int, dst []byte) ([]byte, error) {
	if cap(dst) < n {
		dst = make([]byte, n)
	} else {
		dst = dst[:n]
	}
	size, err := lz4.UncompressBlock(src, dst)
	if err != nil {
		return nil, err
	}
	if size != n {
		return nil, errors.New("LZ4 length mismatch")
	}
	return dst, nil
}

type zstdCodec struct{}

func (zstdCodec) ID() ID       { return Zstandard }
func (zstdCodec) Name() string { return "zstd" }
func (zstdCodec) Encode(src []byte) ([]byte, bool) {
	if len(src) < 1024 {
		return nil, false
	}

	encoder := zstdEncoderPool.Get().(*zstd.Encoder)
	out := encoder.EncodeAll(src, nil)
	zstdEncoderPool.Put(encoder)

	if len(out)*5 > len(src)*4 {
		return nil, false
	}
	return out, true
}
func (zstdCodec) Decode(src []byte, n int) ([]byte, error) {
	if n < 0 || n > zstdDecoderMaxMemory {
		return nil, errors.New("Zstandard length out of range")
	}

	decoder := zstdDecoderPool.Get().(*zstd.Decoder)
	out, err := decoder.DecodeAll(src, make([]byte, 0, n))
	zstdDecoderPool.Put(decoder)
	if err != nil {
		return nil, err
	}
	if len(out) != n {
		return nil, errors.New("Zstandard length mismatch")
	}
	return out, nil
}
func (zstdCodec) DecodeInto(src []byte, n int, dst []byte) ([]byte, error) {
	if n < 0 || n > zstdDecoderMaxMemory {
		return nil, errors.New("Zstandard length out of range")
	}
	if cap(dst) < n {
		dst = make([]byte, 0, n)
	} else {
		dst = dst[:0]
	}
	decoder := zstdDecoderPool.Get().(*zstd.Decoder)
	out, err := decoder.DecodeAll(src, dst)
	zstdDecoderPool.Put(decoder)
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

func likelyIncompressible(src []byte) bool {
	if len(src) < 256 {
		return false
	}

	// Sample the whole value uniformly into a fixed-size stack buffer. Random
	// payloads have high byte diversity and almost never repeat an 8-byte chunk.
	// Repetitive/text/JSON values usually fail one or both checks and continue
	// to the real codec, so this gate stays deliberately conservative.
	const sampleSize = 256
	var sample [sampleSize]byte
	var seen [256]bool
	unique := 0

	if len(src) == sampleSize {
		copy(sample[:], src)
	} else {
		for i := 0; i < sampleSize; i++ {
			idx := i * (len(src) - 1) / (sampleSize - 1)
			sample[i] = src[idx]
		}
	}

	for _, b := range sample {
		if !seen[b] {
			seen[b] = true
			unique++
		}
	}

	// A uniform random 256-byte sample averages about 162 distinct byte values.
	// Keep enough headroom to avoid rejecting normal textual/structured data.
	if unique < 144 {
		return false
	}

	const chunkSize = 8
	for i := chunkSize; i < sampleSize; i += chunkSize {
		chunk := sample[i : i+chunkSize]
		for j := 0; j < i; j += chunkSize {
			if bytes.Equal(chunk, sample[j:j+chunkSize]) {
				return false
			}
		}
	}

	return true
}

type CompressionCandidate struct {
	Name  string
	Bytes int
	ID    ID
}

// CompressionCandidates evaluates physical compression representations for
// diagnostics. It does not decide policy; the engine reports whether a codec
// is eligible for the key's current heat class.
func (r *Registry) CompressionCandidates(src []byte) []CompressionCandidate {
	if len(src) < 256 || alreadyCompressed(src) {
		return nil
	}

	out := make([]CompressionCandidate, 0, 2)

	for _, id := range []ID{LZ4, Zstandard} {
		c := r.codecs[id]
		if c == nil {
			continue
		}

		data, ok := c.Encode(src)
		if !ok {
			continue
		}

		decoded, err := c.Decode(data, len(src))
		if err != nil || !bytes.Equal(decoded, src) {
			continue
		}

		out = append(out, CompressionCandidate{
			Name:  c.Name(),
			Bytes: len(data),
			ID:    id,
		})
	}

	return out
}

// EncodeGeneral is called only by the optimizer, never ordinary SET.
func (r *Registry) EncodeGeneral(src []byte, cold bool) Record {
	best := Record{ID: Raw, RawLength: len(src), Data: bytes.Clone(src)}
	if len(src) < 256 || alreadyCompressed(src) || likelyIncompressible(src) {
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
