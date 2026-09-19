package server

import (
	"encoding/binary"
	"errors"
	"sort"
	"strconv"
)

const (
	lpEOF = byte(0xff)
)

func listpackCanonicalInt(value []byte) (int64, bool) {
	if len(value) == 0 || len(value) >= 21 {
		return 0, false
	}
	n, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != string(value) {
		return 0, false
	}
	return n, true
}

func encodeListpackInteger(n int64) []byte {
	switch {
	case n >= 0 && n <= 127:
		return []byte{byte(n)}
	case n >= -4096 && n <= 4095:
		v := n
		if v < 0 {
			v += 1 << 13
		}
		return []byte{byte((v >> 8) | 0xc0), byte(v)}
	case n >= -32768 && n <= 32767:
		v := uint16(int16(n))
		return []byte{0xf1, byte(v), byte(v >> 8)}
	case n >= -8388608 && n <= 8388607:
		v := n
		if v < 0 {
			v += 1 << 24
		}
		return []byte{0xf2, byte(v), byte(v >> 8), byte(v >> 16)}
	case n >= -2147483648 && n <= 2147483647:
		v := uint32(int32(n))
		return []byte{0xf3, byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	default:
		v := uint64(n)
		return []byte{
			0xf4,
			byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24),
			byte(v >> 32), byte(v >> 40), byte(v >> 48), byte(v >> 56),
		}
	}
}

func encodeListpackString(value []byte) []byte {
	n := len(value)
	switch {
	case n < 64:
		out := make([]byte, 1+n)
		out[0] = 0x80 | byte(n)
		copy(out[1:], value)
		return out
	case n < 4096:
		out := make([]byte, 2+n)
		out[0] = 0xe0 | byte(n>>8)
		out[1] = byte(n)
		copy(out[2:], value)
		return out
	default:
		out := make([]byte, 5+n)
		out[0] = 0xf0
		binary.LittleEndian.PutUint32(out[1:5], uint32(n))
		copy(out[5:], value)
		return out
	}
}

func appendListpackBacklen(dst []byte, n uint64) []byte {
	switch {
	case n <= 127:
		return append(dst, byte(n))
	case n < 16383:
		return append(dst, byte(n>>7), byte(n&127)|0x80)
	case n < 2097151:
		return append(dst, byte(n>>14), byte((n>>7)&127)|0x80, byte(n&127)|0x80)
	case n < 268435455:
		return append(dst,
			byte(n>>21),
			byte((n>>14)&127)|0x80,
			byte((n>>7)&127)|0x80,
			byte(n&127)|0x80,
		)
	default:
		return append(dst,
			byte(n>>28),
			byte((n>>21)&127)|0x80,
			byte((n>>14)&127)|0x80,
			byte((n>>7)&127)|0x80,
			byte(n&127)|0x80,
		)
	}
}

func encodeRedisListpack(values [][]byte) ([]byte, error) {
	if len(values) > 0xffff {
		return nil, errors.New("listpack element count exceeds supported limit")
	}

	out := make([]byte, 6, 64)
	for _, value := range values {
		var entry []byte
		if n, ok := listpackCanonicalInt(value); ok {
			entry = encodeListpackInteger(n)
		} else {
			entry = encodeListpackString(value)
		}
		out = append(out, entry...)
		out = appendListpackBacklen(out, uint64(len(entry)))
	}
	out = append(out, lpEOF)
	if len(out) > int(^uint32(0)) {
		return nil, errors.New("listpack exceeds 32-bit size")
	}
	binary.LittleEndian.PutUint32(out[:4], uint32(len(out)))
	binary.LittleEndian.PutUint16(out[4:6], uint16(len(values)))
	return out, nil
}

func listpackBacklenBytes(n int) int {
	switch {
	case n <= 127:
		return 1
	case n < 16383:
		return 2
	case n < 2097151:
		return 3
	case n < 268435455:
		return 4
	default:
		return 5
	}
}

func decodeListpackEntry(data []byte, pos int) ([]byte, int, error) {
	if pos >= len(data) {
		return nil, 0, errors.New("invalid listpack entry")
	}
	first := data[pos]
	var value []byte
	var entryLen int

	switch {
	case first&0x80 == 0:
		value = []byte(strconv.FormatInt(int64(first), 10))
		entryLen = 1

	case first&0xc0 == 0x80:
		n := int(first & 0x3f)
		if pos+1+n > len(data) {
			return nil, 0, errors.New("invalid listpack string")
		}
		value = append([]byte(nil), data[pos+1:pos+1+n]...)
		entryLen = 1 + n

	case first&0xe0 == 0xc0:
		if pos+2 > len(data) {
			return nil, 0, errors.New("invalid listpack int13")
		}
		u := int64(first&0x1f)<<8 | int64(data[pos+1])
		if u&(1<<12) != 0 {
			u -= 1 << 13
		}
		value = []byte(strconv.FormatInt(u, 10))
		entryLen = 2

	case first&0xf0 == 0xe0:
		if pos+2 > len(data) {
			return nil, 0, errors.New("invalid listpack string12")
		}
		n := int(first&0x0f)<<8 | int(data[pos+1])
		if pos+2+n > len(data) {
			return nil, 0, errors.New("invalid listpack string12")
		}
		value = append([]byte(nil), data[pos+2:pos+2+n]...)
		entryLen = 2 + n

	case first == 0xf0:
		if pos+5 > len(data) {
			return nil, 0, errors.New("invalid listpack string32")
		}
		n := int(binary.LittleEndian.Uint32(data[pos+1 : pos+5]))
		if n < 0 || pos+5+n > len(data) {
			return nil, 0, errors.New("invalid listpack string32")
		}
		value = append([]byte(nil), data[pos+5:pos+5+n]...)
		entryLen = 5 + n

	case first == 0xf1:
		if pos+3 > len(data) {
			return nil, 0, errors.New("invalid listpack int16")
		}
		n := int16(binary.LittleEndian.Uint16(data[pos+1 : pos+3]))
		value = []byte(strconv.FormatInt(int64(n), 10))
		entryLen = 3

	case first == 0xf2:
		if pos+4 > len(data) {
			return nil, 0, errors.New("invalid listpack int24")
		}
		u := int64(data[pos+1]) | int64(data[pos+2])<<8 | int64(data[pos+3])<<16
		if u&(1<<23) != 0 {
			u -= 1 << 24
		}
		value = []byte(strconv.FormatInt(u, 10))
		entryLen = 4

	case first == 0xf3:
		if pos+5 > len(data) {
			return nil, 0, errors.New("invalid listpack int32")
		}
		n := int32(binary.LittleEndian.Uint32(data[pos+1 : pos+5]))
		value = []byte(strconv.FormatInt(int64(n), 10))
		entryLen = 5

	case first == 0xf4:
		if pos+9 > len(data) {
			return nil, 0, errors.New("invalid listpack int64")
		}
		n := int64(binary.LittleEndian.Uint64(data[pos+1 : pos+9]))
		value = []byte(strconv.FormatInt(n, 10))
		entryLen = 9

	default:
		return nil, 0, errors.New("unsupported listpack encoding")
	}

	backlen := listpackBacklenBytes(entryLen)
	if pos+entryLen+backlen > len(data) {
		return nil, 0, errors.New("invalid listpack backlen")
	}
	return value, entryLen + backlen, nil
}

func decodeRedisListpack(data []byte) ([][]byte, error) {
	if len(data) < 7 {
		return nil, errors.New("invalid listpack")
	}
	if int(binary.LittleEndian.Uint32(data[:4])) != len(data) {
		return nil, errors.New("invalid listpack length")
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	pos := 6
	values := make([][]byte, 0, count)
	for pos < len(data)-1 {
		value, consumed, err := decodeListpackEntry(data, pos)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		pos += consumed
	}
	if pos != len(data)-1 || data[pos] != lpEOF {
		return nil, errors.New("invalid listpack eof")
	}
	if count != 0xffff && len(values) != count {
		return nil, errors.New("invalid listpack count")
	}
	return values, nil
}

func encodeRedisIntset(values [][]byte) ([]byte, bool) {
	ints := make([]int64, len(values))
	width := 2
	for i, value := range values {
		n, ok := listpackCanonicalInt(value)
		if !ok {
			return nil, false
		}
		ints[i] = n
		if n < -2147483648 || n > 2147483647 {
			width = 8
		} else if width < 4 && (n < -32768 || n > 32767) {
			width = 4
		}
	}
	sort.Slice(ints, func(i, j int) bool { return ints[i] < ints[j] })
	for i := 1; i < len(ints); i++ {
		if ints[i] == ints[i-1] {
			return nil, false
		}
	}

	out := make([]byte, 8+width*len(ints))
	binary.LittleEndian.PutUint32(out[:4], uint32(width))
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(ints)))
	pos := 8
	for _, n := range ints {
		switch width {
		case 2:
			binary.LittleEndian.PutUint16(out[pos:pos+2], uint16(int16(n)))
		case 4:
			binary.LittleEndian.PutUint32(out[pos:pos+4], uint32(int32(n)))
		case 8:
			binary.LittleEndian.PutUint64(out[pos:pos+8], uint64(n))
		}
		pos += width
	}
	return out, true
}

func decodeRedisIntset(data []byte) ([][]byte, error) {
	if len(data) < 8 {
		return nil, errors.New("invalid intset")
	}
	width := int(binary.LittleEndian.Uint32(data[:4]))
	count := int(binary.LittleEndian.Uint32(data[4:8]))
	if width != 2 && width != 4 && width != 8 {
		return nil, errors.New("invalid intset encoding")
	}
	if count <= 0 || 8+count*width != len(data) {
		return nil, errors.New("invalid intset length")
	}
	out := make([][]byte, 0, count)
	var prev int64
	for i := 0; i < count; i++ {
		pos := 8 + i*width
		var n int64
		switch width {
		case 2:
			n = int64(int16(binary.LittleEndian.Uint16(data[pos : pos+2])))
		case 4:
			n = int64(int32(binary.LittleEndian.Uint32(data[pos : pos+4])))
		case 8:
			n = int64(binary.LittleEndian.Uint64(data[pos : pos+8]))
		}
		if i > 0 && n <= prev {
			return nil, errors.New("invalid intset order")
		}
		out = append(out, []byte(strconv.FormatInt(n, 10)))
		prev = n
	}
	return out, nil
}
