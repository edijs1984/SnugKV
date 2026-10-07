package engine

import (
	"bytes"
	"encoding/binary"
	"math"
)

func float64frombitsLE(b []byte) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(b))
}

func appendFloat64LE(dst []byte, v float64) []byte {
	var raw [8]byte
	binary.LittleEndian.PutUint64(raw[:], math.Float64bits(v))
	return append(dst, raw[:]...)
}

// zsetAppendLastMaxMember bounds the member length handled without allocating.
const zsetAppendLastMaxMember = 256

// packedZSetAppendLast adds one new member that sorts after every existing
// member to a packed sorted set without decoding it into items or re-sorting.
// It scans the encoded value once (rebuilding members in stack buffers to detect
// an existing member and to find the last element), then writes the original
// bytes plus one record into dst's storage. The value keeps its encoding mode.
//
// ok is false whenever the fast path does not apply: unknown or legacy format,
// the member already exists, the new member does not sort last, the score does
// not fit an integer-delta value, the member is long, or the set would reach
// the indexed-promotion size. The caller then uses the general path.
func packedZSetAppendLast(data, member []byte, score float64, dst []byte) (out []byte, ok bool) {
	if len(data) < 4 || data[0] != 'S' || data[1] != 'Z' || len(member) > zsetAppendLastMaxMember {
		return nil, false
	}
	score = normalizeZSetScore(score)
	version := data[2]
	off := 3
	var mode byte
	switch version {
	case packedZSetHeaderAdaptive[2]:
		mode = data[3]
		off = 4
		if mode&^(zsetModeIntDelta|zsetModeMemberPrefix) != 0 || mode&zsetModeMemberPrefix == 0 {
			return nil, false
		}
	case packedZSetHeaderIntDelta[2], packedZSetHeaderFloat64[2]:
	default:
		return nil, false
	}
	useInt := version == packedZSetHeaderIntDelta[2] || version == packedZSetHeaderAdaptive[2] && mode&zsetModeIntDelta != 0
	usePrefix := version == packedZSetHeaderAdaptive[2]

	countOff := off
	count64, err := readZSetUvarint(data, &off)
	if err != nil || count64 == 0 || off-countOff != 1 || count64+1 >= uint64(indexedZSetPromoteMembers) {
		return nil, false
	}
	count := int(count64)

	var newInt int64
	if useInt {
		var exact bool
		newInt, exact = zsetExactInt64(score)
		if !exact {
			return nil, false
		}
	}

	var bufA, bufB [zsetAppendLastMaxMember]byte
	cur, prev := bufA[:0], bufB[:0]
	var prevInt int64
	var prevScore float64
	for i := 0; i < count; i++ {
		if useInt {
			encoded, e := readZSetUvarint(data, &off)
			if e != nil {
				return nil, false
			}
			if i == 0 {
				prevInt = zsetUnZigZag(encoded)
			} else {
				next := int64(uint64(prevInt) + encoded)
				if next < prevInt {
					return nil, false
				}
				prevInt = next
			}
			prevScore = float64(prevInt)
		} else {
			if len(data)-off < 8 {
				return nil, false
			}
			prevScore = normalizeZSetScore(float64frombitsLE(data[off : off+8]))
			off += 8
		}
		if !usePrefix || i == 0 {
			n, e := readZSetUvarint(data, &off)
			if e != nil || n > uint64(len(data)-off) || n > zsetAppendLastMaxMember {
				return nil, false
			}
			cur = append(cur[:0], data[off:off+int(n)]...)
			off += int(n)
		} else {
			p, e := readZSetUvarint(data, &off)
			if e != nil || p > uint64(len(prev)) {
				return nil, false
			}
			sfx, e := readZSetUvarint(data, &off)
			if e != nil || sfx > uint64(len(data)-off) || p+sfx > zsetAppendLastMaxMember {
				return nil, false
			}
			cur = append(cur[:0], prev[:int(p)]...)
			cur = append(cur, data[off:off+int(sfx)]...)
			off += int(sfx)
		}
		if bytes.Equal(cur, member) {
			return nil, false
		}
		cur, prev = prev, cur
	}
	if off != len(data) {
		return nil, false
	}
	if !zsetLess(ZSetItem{Member: prev, Score: prevScore}, ZSetItem{Member: member, Score: score}) {
		return nil, false
	}

	size := len(data) + 2*10 + 8 + len(member)
	if size > maxPackedZSetBytes {
		return nil, false
	}
	if cap(dst) >= size {
		out = dst[:0]
	} else {
		out = make([]byte, 0, size)
	}
	out = append(out, data...)
	out[countOff] = byte(count + 1)
	if useInt {
		out = appendZSetUvarint(out, uint64(newInt)-uint64(prevInt))
	} else {
		out = appendFloat64LE(out, score)
	}
	if usePrefix {
		p := zsetCommonPrefix(prev, member)
		out = appendZSetUvarint(out, uint64(p))
		out = appendZSetUvarint(out, uint64(len(member)-p))
		out = append(out, member[p:]...)
	} else {
		out = appendZSetUvarint(out, uint64(len(member)))
		out = append(out, member...)
	}
	return out, true
}
