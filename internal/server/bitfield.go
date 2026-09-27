package server

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
)

type bitFieldEncoding struct {
	signed bool
	bits   int
}

type bitFieldOp struct {
	kind     string
	encoding bitFieldEncoding
	offset   int64
	value    int64
	overflow string
}

const maxRedisBitOffset int64 = (1 << 32) - 1

func parseBitFieldEncoding(arg []byte) (bitFieldEncoding, error) {
	s := string(arg)
	if len(s) < 2 {
		return bitFieldEncoding{}, errors.New("ERR Invalid bitfield type. Use something like i16 u8. Note that u64 is not supported but i64 is.")
	}
	var enc bitFieldEncoding
	switch s[0] {
	case 'i', 'I':
		enc.signed = true
	case 'u', 'U':
		enc.signed = false
	default:
		return bitFieldEncoding{}, errors.New("ERR Invalid bitfield type. Use something like i16 u8. Note that u64 is not supported but i64 is.")
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil || n < 1 || enc.signed && n > 64 || !enc.signed && n > 63 {
		return bitFieldEncoding{}, errors.New("ERR Invalid bitfield type. Use something like i16 u8. Note that u64 is not supported but i64 is.")
	}
	enc.bits = n
	return enc, nil
}

func parseBitFieldOffset(arg []byte, bits int) (int64, error) {
	s := string(arg)
	mult := false
	if strings.HasPrefix(s, "#") {
		mult = true
		s = s[1:]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("ERR bit offset is not an integer or out of range")
	}
	if mult {
		if n > maxRedisBitOffset/int64(bits) {
			return 0, errors.New("ERR bit offset is not an integer or out of range")
		}
		n *= int64(bits)
	}
	if n > maxRedisBitOffset || int64(bits)-1 > maxRedisBitOffset-n {
		return 0, errors.New("ERR bit offset is not an integer or out of range")
	}
	return n, nil
}

func parseBitFieldOps(args [][]byte, readonly bool) ([]bitFieldOp, error) {
	ops := make([]bitFieldOp, 0)
	overflow := "WRAP"
	for i := 2; i < len(args); {
		token := strings.ToUpper(string(args[i]))
		switch token {
		case "OVERFLOW":
			if readonly {
				return nil, errors.New("ERR BITFIELD_RO only supports the GET subcommand")
			}
			if i+1 >= len(args) {
				return nil, errors.New("ERR syntax error")
			}
			mode := strings.ToUpper(string(args[i+1]))
			if mode != "WRAP" && mode != "SAT" && mode != "FAIL" {
				return nil, errors.New("ERR Invalid OVERFLOW type specified")
			}
			overflow = mode
			i += 2
			continue
		case "GET", "SET", "INCRBY":
		default:
			return nil, errors.New("ERR syntax error")
		}

		if readonly && token != "GET" {
			return nil, errors.New("ERR BITFIELD_RO only supports the GET subcommand")
		}
		need := 3
		if token != "GET" {
			need = 4
		}
		if i+need-1 >= len(args) {
			return nil, errors.New("ERR syntax error")
		}
		enc, err := parseBitFieldEncoding(args[i+1])
		if err != nil {
			return nil, err
		}
		offset, err := parseBitFieldOffset(args[i+2], enc.bits)
		if err != nil {
			return nil, err
		}
		op := bitFieldOp{kind: token, encoding: enc, offset: offset, overflow: overflow}
		if token != "GET" {
			v, err := strconv.ParseInt(string(args[i+3]), 10, 64)
			if err != nil {
				return nil, errors.New("ERR value is not an integer or out of range")
			}
			op.value = v
		}
		ops = append(ops, op)
		i += need
	}
	return ops, nil
}

func (s *Server) readBitField(key string, enc bitFieldEncoding, offset int64) (int64, error) {
	var raw uint64
	for i := 0; i < enc.bits; i++ {
		bit, err := s.store.GetBit(key, offset+int64(i))
		if err != nil {
			return 0, err
		}
		raw = (raw << 1) | uint64(bit)
	}
	if !enc.signed {
		return int64(raw), nil
	}
	if enc.bits == 64 {
		return int64(raw), nil
	}
	sign := uint64(1) << (enc.bits - 1)
	if raw&sign != 0 {
		raw |= ^((uint64(1) << enc.bits) - 1)
	}
	return int64(raw), nil
}

func (s *Server) writeBitField(key string, enc bitFieldEncoding, offset int64, value int64) error {
	raw := uint64(value)
	if enc.bits < 64 {
		raw &= (uint64(1) << enc.bits) - 1
	}
	for i := 0; i < enc.bits; i++ {
		shift := enc.bits - 1 - i
		bit := int((raw >> shift) & 1)
		if _, err := s.store.SetBit(key, offset+int64(i), bit); err != nil {
			return err
		}
	}
	return nil
}

func signedBounds(bits int) (int64, int64) {
	if bits == 64 {
		return -1 << 63, 1<<63 - 1
	}
	max := int64(1<<(bits-1)) - 1
	return -max - 1, max
}

func wrapSigned(x *big.Int, bits int) int64 {
	mod := new(big.Int).Lsh(big.NewInt(1), uint(bits))
	v := new(big.Int).Mod(new(big.Int).Set(x), mod)
	sign := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
	if v.Cmp(sign) >= 0 {
		v.Sub(v, mod)
	}
	return v.Int64()
}

func wrapUnsigned(x *big.Int, bits int) int64 {
	mod := new(big.Int).Lsh(big.NewInt(1), uint(bits))
	v := new(big.Int).Mod(new(big.Int).Set(x), mod)
	return v.Int64()
}

func applyBitFieldWrite(old int64, op bitFieldOp) (result int64, writeValue int64, ok bool) {
	if op.encoding.signed {
		min, max := signedBounds(op.encoding.bits)
		var target *big.Int
		if op.kind == "SET" {
			target = big.NewInt(op.value)
		} else {
			target = new(big.Int).Add(big.NewInt(old), big.NewInt(op.value))
		}
		minB, maxB := big.NewInt(min), big.NewInt(max)
		overflow := target.Cmp(minB) < 0 || target.Cmp(maxB) > 0
		if overflow {
			switch op.overflow {
			case "FAIL":
				return 0, 0, false
			case "SAT":
				if target.Cmp(minB) < 0 {
					writeValue = min
				} else {
					writeValue = max
				}
			default:
				writeValue = wrapSigned(target, op.encoding.bits)
			}
		} else {
			writeValue = target.Int64()
		}
		if op.kind == "SET" {
			result = old
		} else {
			result = writeValue
		}
		return result, writeValue, true
	}

	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(op.encoding.bits)), big.NewInt(1))
	var target *big.Int
	if op.kind == "SET" {
		// Redis converts the signed input to uint64 before unsigned overflow checks.
		target = new(big.Int).SetUint64(uint64(op.value))
	} else {
		target = new(big.Int).Add(big.NewInt(old), big.NewInt(op.value))
	}
	overflowLow := target.Sign() < 0
	overflowHigh := target.Cmp(max) > 0
	if overflowLow || overflowHigh {
		switch op.overflow {
		case "FAIL":
			return 0, 0, false
		case "SAT":
			if overflowLow {
				writeValue = 0
			} else {
				writeValue = max.Int64()
			}
		default:
			writeValue = wrapUnsigned(target, op.encoding.bits)
		}
	} else {
		writeValue = target.Int64()
	}
	if op.kind == "SET" {
		result = old
	} else {
		result = writeValue
	}
	return result, writeValue, true
}

func (s *Server) executeBitField(args [][]byte) ([]byte, error) {
	readonly := strings.EqualFold(string(args[0]), "BITFIELD_RO")
	ops, err := parseBitFieldOps(args, readonly)
	if err != nil {
		return nil, err
	}
	key := string(args[1])
	replies := make([][]byte, 0, len(ops))
	for _, op := range ops {
		old, err := s.readBitField(key, op.encoding, op.offset)
		if err != nil {
			return nil, err
		}
		if op.kind == "GET" {
			replies = append(replies, integer(old))
			continue
		}
		result, writeValue, ok := applyBitFieldWrite(old, op)
		if !ok {
			replies = append(replies, nullBulk())
			continue
		}
		if err := s.writeBitField(key, op.encoding, op.offset, writeValue); err != nil {
			return nil, err
		}
		replies = append(replies, integer(result))
	}
	return array(replies...), nil
}
