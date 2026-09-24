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
	keyRDBTypeString         = byte(0)
	keyRDBTypeSetIntset      = byte(11)
	keyRDBTypeHashListpack   = byte(16)
	keyRDBTypeZSetListpack   = byte(17)
	keyRDBTypeListQuicklist2 = byte(18)
	keyRDBTypeSetListpack    = byte(20)
	keyRDBTypeStreamListpacks3 = byte(21)
	keyRDBVersion            = functionRDBVersion
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
	case n >= -128 && n <= 127:
		return append(dst, 0xC0|rdbEncInt8, byte(uint8(int8(n)))), true
	case n >= -32768 && n <= 32767:
		dst = append(dst, 0xC0|rdbEncInt16)
		var buf [2]byte
		binary.LittleEndian.PutUint16(buf[:], uint16(int16(n)))
		return append(dst, buf[:]...), true
	case n >= -2147483648 && n <= 2147483647:
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

func appendKeyDumpTrailer(out []byte) ([]byte, error) {
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

func encodeKeyStringDump(value []byte) ([]byte, error) {
	out := []byte{keyRDBTypeString}
	out = appendRDBKeyString(out, value)
	return appendKeyDumpTrailer(out)
}

func verifyKeyDumpPayload(data []byte) ([]byte, error) {
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
	return data[:trailer], nil
}

func decodeKeyStringDump(data []byte) ([]byte, error) {
	body, err := verifyKeyDumpPayload(data)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || body[0] != keyRDBTypeString {
		return nil, errors.New("ERR Bad data format")
	}
	pos := 1
	value, err := decodeRDBString(body, &pos)
	if err != nil || pos != len(body) {
		return nil, errors.New("ERR Bad data format")
	}
	return value, nil
}

func encodeHashDump(pairs []engine.HashPair) ([]byte, error) {
	values := make([][]byte, 0, len(pairs)*2)
	for _, pair := range pairs {
		values = append(values, pair.Field, pair.Value)
	}
	lp, err := encodeRedisListpack(values)
	if err != nil {
		return nil, err
	}
	out := []byte{keyRDBTypeHashListpack}
	out = appendRDBRawString(out, lp)
	return appendKeyDumpTrailer(out)
}

func encodeSetDump(members [][]byte) ([]byte, error) {
	if len(members) == 0 {
		return nil, errors.New("ERR empty SET cannot be dumped")
	}
	if len(members) <= 512 {
		if intset, ok := encodeRedisIntset(members); ok {
			out := []byte{keyRDBTypeSetIntset}
			out = appendRDBRawString(out, intset)
			return appendKeyDumpTrailer(out)
		}
	}
	lp, err := encodeRedisListpack(members)
	if err != nil {
		return nil, err
	}
	out := []byte{keyRDBTypeSetListpack}
	out = appendRDBRawString(out, lp)
	return appendKeyDumpTrailer(out)
}

func encodeListDump(elements [][]byte) ([]byte, error) {
	if len(elements) == 0 {
		return nil, errors.New("ERR empty LIST cannot be dumped")
	}
	lp, err := encodeRedisListpack(elements)
	if err != nil {
		return nil, err
	}
	out := []byte{keyRDBTypeListQuicklist2}
	out = appendRDBLen(out, 1) // one quicklist node
	out = appendRDBLen(out, 2) // QUICKLIST_NODE_CONTAINER_PACKED
	out = appendRDBRawString(out, lp)
	return appendKeyDumpTrailer(out)
}

func encodeZSetDump(items []engine.ZSetItem) ([]byte, error) {
	if len(items) == 0 {
		return nil, errors.New("ERR empty ZSET cannot be dumped")
	}
	values := make([][]byte, 0, len(items)*2)
	for _, item := range items {
		values = append(values, item.Member, formatZSetScore(item.Score))
	}
	lp, err := encodeRedisListpack(values)
	if err != nil {
		return nil, err
	}
	out := []byte{keyRDBTypeZSetListpack}
	out = appendRDBRawString(out, lp)
	return appendKeyDumpTrailer(out)
}

type decodedKeyObject struct {
	valueType engine.ValueType
	scalar    []byte
	hash      []engine.HashPair
	set       [][]byte
	list      [][]byte
	zset      []engine.ZSetItem
	stream    *engine.StreamSnapshot
}

func decodeSingleRDBString(body []byte, pos *int) ([]byte, error) {
	value, err := decodeRDBString(body, pos)
	if err != nil {
		return nil, errors.New("ERR Bad data format")
	}
	return value, nil
}

func decodeKeyDumpObject(data []byte) (decodedKeyObject, error) {
	body, err := verifyKeyDumpPayload(data)
	if err != nil {
		return decodedKeyObject{}, err
	}
	if len(body) == 0 {
		return decodedKeyObject{}, errors.New("ERR Bad data format")
	}
	pos := 1

	switch body[0] {
	case keyRDBTypeString:
		value, err := decodeSingleRDBString(body, &pos)
		if err != nil || pos != len(body) {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		return decodedKeyObject{valueType: engine.TypeString, scalar: value}, nil

	case keyRDBTypeHashListpack:
		raw, err := decodeSingleRDBString(body, &pos)
		if err != nil || pos != len(body) {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		values, err := decodeRedisListpack(raw)
		if err != nil || len(values) == 0 || len(values)%2 != 0 {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		pairs := make([]engine.HashPair, 0, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			pairs = append(pairs, engine.HashPair{Field: values[i], Value: values[i+1]})
		}
		return decodedKeyObject{valueType: engine.TypeHash, hash: pairs}, nil

	case keyRDBTypeSetListpack:
		raw, err := decodeSingleRDBString(body, &pos)
		if err != nil || pos != len(body) {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		members, err := decodeRedisListpack(raw)
		if err != nil || len(members) == 0 {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		return decodedKeyObject{valueType: engine.TypeSet, set: members}, nil

	case keyRDBTypeSetIntset:
		raw, err := decodeSingleRDBString(body, &pos)
		if err != nil || pos != len(body) {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		members, err := decodeRedisIntset(raw)
		if err != nil {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		return decodedKeyObject{valueType: engine.TypeSet, set: members}, nil

	case keyRDBTypeListQuicklist2:
		nodes, encoded, err := readRDBLen(body, &pos)
		if err != nil || encoded || nodes == 0 || nodes > uint64(len(body)) {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		elements := make([][]byte, 0)
		for i := uint64(0); i < nodes; i++ {
			container, encoded, err := readRDBLen(body, &pos)
			if err != nil || encoded || container != 2 {
				return decodedKeyObject{}, errors.New("ERR Bad data format")
			}
			raw, err := decodeSingleRDBString(body, &pos)
			if err != nil {
				return decodedKeyObject{}, err
			}
			node, err := decodeRedisListpack(raw)
			if err != nil || len(node) == 0 {
				return decodedKeyObject{}, errors.New("ERR Bad data format")
			}
			elements = append(elements, node...)
		}
		if pos != len(body) || len(elements) == 0 {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		return decodedKeyObject{valueType: engine.TypeList, list: elements}, nil

	case keyRDBTypeZSetListpack:
		raw, err := decodeSingleRDBString(body, &pos)
		if err != nil || pos != len(body) {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		values, err := decodeRedisListpack(raw)
		if err != nil || len(values) == 0 || len(values)%2 != 0 {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		items := make([]engine.ZSetItem, 0, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			score, err := parseZSetScore(values[i+1])
			if err != nil || math.IsNaN(score) {
				return decodedKeyObject{}, errors.New("ERR Bad data format")
			}
			items = append(items, engine.ZSetItem{Member: values[i], Score: score})
		}
		return decodedKeyObject{valueType: engine.TypeZSet, zset: items}, nil

	case keyRDBTypeStreamListpacks3:
		snapshot, err := decodeStreamDump(body, &pos)
		if err != nil || pos != len(body) {
			return decodedKeyObject{}, errors.New("ERR Bad data format")
		}
		return decodedKeyObject{valueType: engine.TypeStream, stream: &snapshot}, nil

	default:
		return decodedKeyObject{}, errors.New("ERR Bad data format")
	}
}

func buildRestoreRecord(key string, object decodedKeyObject, expiresAtMS int64) (persistence.Record, error) {
	tmp, err := engine.NewWithShards(1)
	if err != nil {
		return persistence.Record{}, err
	}
	const tempKey = "__restore__"

	switch object.valueType {
	case engine.TypeString:
		if _, err := tmp.SetConditional(tempKey, object.scalar, engine.SetOptions{}); err != nil {
			return persistence.Record{}, err
		}
	case engine.TypeHash:
		fields := make([][]byte, 0, len(object.hash))
		values := make([][]byte, 0, len(object.hash))
		for _, pair := range object.hash {
			fields = append(fields, pair.Field)
			values = append(values, pair.Value)
		}
		if _, err := tmp.HashSet(tempKey, fields, values); err != nil {
			return persistence.Record{}, err
		}
		for _, pair := range object.hash {
			if pair.ExpiresAtMS == 0 {
				continue
			}
			if _, err := tmp.HashFieldExpireAt(tempKey, [][]byte{pair.Field}, pair.ExpiresAtMS); err != nil {
				return persistence.Record{}, err
			}
		}
	case engine.TypeSet:
		if _, err := tmp.SetAdd(tempKey, object.set); err != nil {
			return persistence.Record{}, err
		}
	case engine.TypeList:
		if _, err := tmp.ListPushRight(tempKey, object.list); err != nil {
			return persistence.Record{}, err
		}
	case engine.TypeZSet:
		if _, _, _, err := tmp.ZSetAdd(tempKey, object.zset, engine.ZSetAddOptions{}); err != nil {
			return persistence.Record{}, err
		}
	case engine.TypeStream:
		if object.stream == nil {
			return persistence.Record{}, errors.New("ERR Bad data format")
		}
		value, err := engine.StreamSnapshotRecordValue(*object.stream)
		if err != nil {
			return persistence.Record{}, err
		}
		return persistence.Record{
			Key: []byte(key), Value: value, ValueType: uint8(engine.TypeStream), ExpiresAtMS: expiresAtMS,
		}, nil
	default:
		return persistence.Record{}, errors.New("ERR Bad data format")
	}

	records := tmp.Export([]string{tempKey})
	if len(records) != 1 || records[0].Deleted {
		return persistence.Record{}, errors.New("ERR Bad data format")
	}
	record := records[0]
	record.Key = []byte(key)
	record.ExpiresAtMS = expiresAtMS
	return record, nil
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

func (s *Server) dumpKey(key string, valueType engine.ValueType) ([]byte, error) {
	switch valueType {
	case engine.TypeHash:
		pairs, err := s.store.HashGetAll(key)
		if err != nil {
			return nil, err
		}
		return encodeHashDump(pairs)
	case engine.TypeSet:
		members, err := s.store.SetMembers(key)
		if err != nil {
			return nil, err
		}
		return encodeSetDump(members)
	case engine.TypeList:
		elements, err := s.store.ListRange(key, 0, -1)
		if err != nil {
			return nil, err
		}
		return encodeListDump(elements)
	case engine.TypeZSet:
		items, err := s.store.ZSetRange(key, 0, -1, false)
		if err != nil {
			return nil, err
		}
		return encodeZSetDump(items)
	case engine.TypeStream:
		snapshot, found, err := s.store.StreamSnapshot(key)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, nil
		}
		return encodeStreamDump(snapshot)
	default:
		if !keyDumpScalarTypeSupported(valueType) {
			return nil, errors.New("ERR DUMP object type is not supported yet")
		}
		value, found := s.store.Get(key)
		if !found {
			return nil, nil
		}
		return encodeKeyStringDump(value)
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
		valueType, found := s.store.ValueTypeOf(key)
		if !found {
			return nullBulk(), nil
		}
		payload, err := s.dumpKey(key, valueType)
		if err != nil {
			return nil, err
		}
		if payload == nil {
			return nullBulk(), nil
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

		object, err := decodeKeyDumpObject(args[3])
		if err != nil {
			return nil, err
		}

		var expiresAtMS int64
		if ttl > 0 {
			if options.absttl {
				expiresAtMS = ttl
			} else {
				nowMS := time.Now().UnixMilli()
				if ttl > int64(^uint64(0)>>1)-nowMS {
					return nil, errors.New("ERR value is not an integer or out of range")
				}
				expiresAtMS = nowMS + ttl
			}
		}

		record, err := buildRestoreRecord(key, object, expiresAtMS)
		if err != nil {
			return nil, err
		}
		if err := s.store.Restore([]persistence.Record{record}, false); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil
	}

	return nil, errors.New("ERR command unavailable")
}
