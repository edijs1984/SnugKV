package server

import (
	"encoding/binary"
	"errors"
	"strconv"

	"snugkv/internal/persistence"
)

const (
	redisZipEnd       = byte(0xff)
	redisZipBigPrev   = byte(0xfe)
	redisZipInt16     = byte(0xc0)
	redisZipInt32     = byte(0xd0)
	redisZipInt64     = byte(0xe0)
	redisZipInt24     = byte(0xf0)
	redisZipIntImmMin = byte(0xf1)
	redisZipIntImmMax = byte(0xfd)
	redisZipInt8      = byte(0xfe)
)

func decodeRedisZiplist(raw []byte) ([][]byte, error) {
	if len(raw) < 11 || len(raw) > persistence.MaxFrameBytes {
		return nil, errors.New("invalid Redis ziplist length")
	}
	if binary.LittleEndian.Uint32(raw[:4]) != uint32(len(raw)) || raw[len(raw)-1] != redisZipEnd {
		return nil, errors.New("invalid Redis ziplist header")
	}

	declared := binary.LittleEndian.Uint16(raw[8:10])
	values := make([][]byte, 0)
	pos := 10
	prevRawLen := 0

	for pos < len(raw)-1 {
		entryStart := pos

		var prevLen int
		switch raw[pos] {
		case redisZipBigPrev:
			if len(raw)-pos < 5 {
				return nil, errors.New("truncated Redis ziplist prevlen")
			}
			prevLen = int(binary.LittleEndian.Uint32(raw[pos+1 : pos+5]))
			pos += 5
		default:
			prevLen = int(raw[pos])
			pos++
		}
		if len(values) == 0 {
			if prevLen != 0 {
				return nil, errors.New("invalid Redis ziplist first prevlen")
			}
		} else if prevLen != prevRawLen {
			return nil, errors.New("invalid Redis ziplist prevlen")
		}

		if pos >= len(raw)-1 {
			return nil, errors.New("truncated Redis ziplist encoding")
		}
		encoding := raw[pos]
		pos++

		var value []byte
		if encoding < 0xc0 {
			var n int
			switch encoding >> 6 {
			case 0:
				n = int(encoding & 0x3f)
			case 1:
				if pos >= len(raw)-1 {
					return nil, errors.New("truncated Redis ziplist 14-bit string")
				}
				n = (int(encoding&0x3f) << 8) | int(raw[pos])
				pos++
			case 2:
				if encoding != 0x80 || len(raw)-pos < 4 {
					return nil, errors.New("invalid Redis ziplist 32-bit string")
				}
				length := binary.BigEndian.Uint32(raw[pos : pos+4])
				pos += 4
				if length > uint32(persistence.MaxFrameBytes) {
					return nil, errors.New("Redis ziplist string exceeds limit")
				}
				n = int(length)
			default:
				return nil, errors.New("invalid Redis ziplist string encoding")
			}
			if n < 0 || n > len(raw)-1-pos {
				return nil, errors.New("truncated Redis ziplist string")
			}
			value = append([]byte(nil), raw[pos:pos+n]...)
			pos += n
		} else {
			var number int64
			switch encoding {
			case redisZipInt8:
				if pos >= len(raw)-1 {
					return nil, errors.New("truncated Redis ziplist int8")
				}
				number = int64(int8(raw[pos]))
				pos++
			case redisZipInt16:
				if len(raw)-1-pos < 2 {
					return nil, errors.New("truncated Redis ziplist int16")
				}
				number = int64(int16(binary.LittleEndian.Uint16(raw[pos : pos+2])))
				pos += 2
			case redisZipInt24:
				if len(raw)-1-pos < 3 {
					return nil, errors.New("truncated Redis ziplist int24")
				}
				v := int32(raw[pos]) | int32(raw[pos+1])<<8 | int32(raw[pos+2])<<16
				if v&0x800000 != 0 {
					v |= ^int32(0xffffff)
				}
				number = int64(v)
				pos += 3
			case redisZipInt32:
				if len(raw)-1-pos < 4 {
					return nil, errors.New("truncated Redis ziplist int32")
				}
				number = int64(int32(binary.LittleEndian.Uint32(raw[pos : pos+4])))
				pos += 4
			case redisZipInt64:
				if len(raw)-1-pos < 8 {
					return nil, errors.New("truncated Redis ziplist int64")
				}
				number = int64(binary.LittleEndian.Uint64(raw[pos : pos+8]))
				pos += 8
			default:
				if encoding < redisZipIntImmMin || encoding > redisZipIntImmMax {
					return nil, errors.New("invalid Redis ziplist integer encoding")
				}
				number = int64(encoding&0x0f) - 1
			}
			value = []byte(strconv.FormatInt(number, 10))
		}

		prevRawLen = pos - entryStart
		values = append(values, value)
		if len(values) > persistence.MaxFrameBytes {
			return nil, errors.New("Redis ziplist entry count exceeds limit")
		}
	}

	if pos != len(raw)-1 || raw[pos] != redisZipEnd {
		return nil, errors.New("invalid Redis ziplist terminator")
	}
	if declared != 0xffff && int(declared) != len(values) {
		return nil, errors.New("invalid Redis ziplist entry count")
	}
	return values, nil
}
