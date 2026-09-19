package server

import (
	"encoding/binary"
	"errors"
	"math"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
	"strconv"
	"strings"
	"time"
)

const (
	keyRDBTypeString = byte(0)
	keyRDBVersion    = functionRDBVersion
)

var keyDumpRestoreCommands = map[string]commandInfo{
	"DUMP":    {2, 2, 1, 1, 1, false},
	"RESTORE": {4, 0, 1, 1, 1, true},
}

func init() {
	for name, info := range keyDumpRestoreCommands {
		commandTable[name] = info
	}
}

func isKeyDumpRestoreCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := keyDumpRestoreCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func appendRDBIntegerString(dst []byte, value []byte) ([]byte, bool) {
	if len(value) == 0 || len(value) > 11 {
		return dst, false
	}
	n, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != string(value) {
		return dst, false
	}
	switch {
	case n >= math.MinInt8 && n <= math.MaxInt8:
		return append(dst, 0xC0|rdbEncInt8, byte(int8(n))), true
	case n >= math.MinInt16 && n <= math.MaxInt16:
		dst = append(dst, 0xC0|rdbEncInt16)
		var buf [2]byte
		binary.LittleEndian.PutUint16(buf[:], uint16(int16(n)))
		return append(dst, buf[:]...), true
	case n >= math.MinInt32 && n <= math.MaxInt32:
		dst = append(dst, 0xC0|rdbEncInt32)
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], uint32(int32(n)))
		return append(dst, buf[:]...), true
	default:
		return dst, false
	}
}

func appendRDBKeyString(dst []byte, value []byte) []byte {
	if encoded, ok := appendRDBIntegerString(dst, value); ok {
		return encoded
	}
	return appendRDBRawString(dst, value)
}

func encodeKeyStringDump(value []byte) ([]byte, error) {
	out := make([]byte, 0, len(value)+16)
	out = append(out, keyRDBTypeString)
	out = appendRDBKeyString(out, value)

	var version [2]byte
	binary.LittleEndian.PutUint16(version[:], keyRDBVersion)
	out = append(out, version[:]...)

	var checksum [8]byte
	binary.LittleEndian.PutUint64(checksum[:], redisCRC64(out))
	out = append(out, checksum[:]...)

	if len(out) > persistence.MaxFrameBytes {
		return nil, errors.New("ERR DUMP payload exceeds limit")
	}
	return out, nil
}

func decodeKeyStringDump(data []byte) ([]byte, error) {
	if len(data) < 11 || len(data) > persistence.MaxFrameBytes {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}

	trailer := len(data) - 10
	version := binary.LittleEndian.Uint16(data[trailer : trailer+2])
	if version == 0 || version > keyRDBVersion {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}
	wantChecksum := binary.LittleEndian.Uint64(data[trailer+2:])
	if redisCRC64(data[:trailer+2]) != wantChecksum {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}

	pos := 0
	if data[pos] != keyRDBTypeString {
		return nil, errors.New("ERR Bad data format")
	}
	pos++

	value, err := decodeRDBString(data[:trailer], &pos)
	if err != nil || pos != trailer {
		return nil, errors.New("ERR Bad data format")
	}
	return value, nil
}

type restoreOptions struct {
	replace bool
	absttl  bool
}

func parseRestoreOptions(args [][]byte) (restoreOptions, error) {
	var options restoreOptions
	idleSeen := false
	freqSeen := false

	for i := 0; i < len(args); {
		switch strings.ToUpper(string(args[i])) {
		case "REPLACE":
			options.replace = true
			i++
		case "ABSTTL":
			options.absttl = true
			i++
		case "IDLETIME":
			if i+1 >= len(args) || freqSeen {
				return restoreOptions{}, errors.New("ERR syntax error")
			}
			n, err := parseInt64(args[i+1])
			if err != nil {
				return restoreOptions{}, err
			}
			if n < 0 {
				return restoreOptions{}, errors.New("ERR Invalid IDLETIME value, must be >= 0")
			}
			idleSeen = true
			i += 2
		case "FREQ":
			if i+1 >= len(args) || idleSeen {
				return restoreOptions{}, errors.New("ERR syntax error")
			}
			n, err := parseInt64(args[i+1])
			if err != nil {
				return restoreOptions{}, err
			}
			if n < 0 || n > 255 {
				return restoreOptions{}, errors.New("ERR Invalid FREQ value, must be >= 0 and <= 255")
			}
			freqSeen = true
			i += 2
		default:
			return restoreOptions{}, errors.New("ERR syntax error")
		}
	}
	return options, nil
}

func keyDumpScalarTypeSupported(t engine.ValueType) bool {
	switch t {
	case engine.TypeString, engine.TypeInt64, engine.TypeUint64,
		engine.TypeFloat64, engine.TypeBool, engine.TypeBytes:
		return true
	default:
		return false
	}
}

func (s *Server) executeKeyDumpRestore(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "DUMP":
		if len(args) != 2 {
			return nil, errors.New("ERR wrong number of arguments for 'dump' command")
		}
		key := string(args[1])
		t, found := s.store.ValueTypeOf(key)
		if !found {
			return nullBulk(), nil
		}
		if !keyDumpScalarTypeSupported(t) {
			return nil, errors.New("ERR DUMP object type is not supported yet")
		}
		value, found := s.store.Get(key)
		if !found {
			return nullBulk(), nil
		}
		payload, err := encodeKeyStringDump(value)
		if err != nil {
			return nil, err
		}
		return formatBulkString(payload), nil

	case "RESTORE":
		if len(args) < 4 {
			return nil, errors.New("ERR wrong number of arguments for 'restore' command")
		}

		options, err := parseRestoreOptions(args[4:])
		if err != nil {
			return nil, err
		}

		key := string(args[1])
		if !options.replace && s.store.Exists([]string{key}) != 0 {
			return nil, errors.New("BUSYKEY Target key name already exists.")
		}

		ttl, err := parseInt64(args[2])
		if err != nil {
			return nil, err
		}
		if ttl < 0 {
			return nil, errors.New("ERR Invalid TTL value, must be >= 0")
		}

		value, err := decodeKeyStringDump(args[3])
		if err != nil {
			return nil, err
		}

		setOptions := engine.SetOptions{}
		if ttl > 0 {
			if options.absttl {
				setOptions.HasExpireAt = true
				setOptions.ExpireAt = time.UnixMilli(ttl)
			} else {
				if ttl > math.MaxInt64/int64(time.Millisecond) {
					return nil, errors.New("ERR value is not an integer or out of range")
				}
				setOptions.TTL = time.Duration(ttl) * time.Millisecond
			}
		}

		if _, err := s.store.SetConditional(key, value, setOptions); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil
	}

	return nil, errors.New("ERR command unavailable")
}
