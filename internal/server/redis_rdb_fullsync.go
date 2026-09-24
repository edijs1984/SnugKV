package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

const (
	redisRDBTypeList           = byte(1)
	redisRDBTypeSet            = byte(2)
	redisRDBTypeZSet           = byte(3)
	redisRDBTypeHash           = byte(4)
	redisRDBTypeZSet2          = byte(5)
	redisRDBTypeHashZipmap     = byte(9)
	redisRDBTypeListZiplist    = byte(10)
	redisRDBTypeZSetZiplist    = byte(12)
	redisRDBTypeHashZiplist    = byte(13)
	redisRDBTypeListQuicklist  = byte(14)
	redisRDBOpcodeIdle         = byte(248)
	redisRDBOpcodeFreq         = byte(249)
	redisRDBOpcodeAux          = byte(250)
	redisRDBOpcodeResizeDB     = byte(251)
	redisRDBOpcodeExpireTimeMS = byte(252)
	redisRDBOpcodeExpireTime   = byte(253)
	redisRDBOpcodeSelectDB     = byte(254)
	redisRDBOpcodeEOF          = byte(255)

	redisRDBMaxSupportedVersion = 16
)

func decodeRedisRDBTextDouble(data []byte, pos *int) (float64, error) {
	if *pos >= len(data) {
		return 0, errors.New("truncated Redis RDB zset score")
	}
	length := data[*pos]
	*pos++

	switch length {
	case 255:
		return math.Inf(-1), nil
	case 254:
		return math.Inf(1), nil
	case 253:
		return 0, errors.New("invalid Redis RDB zset score")
	}

	if int(length) > len(data)-*pos {
		return 0, errors.New("truncated Redis RDB zset score")
	}
	raw := string(data[*pos : *pos+int(length)])
	*pos += int(length)
	score, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(score) {
		return 0, errors.New("invalid Redis RDB zset score")
	}
	return score, nil
}

func decodeRedisRDBObjectAt(data []byte, pos *int, objectType byte) (decodedKeyObject, error) {
	switch objectType {
	case redisRDBTypeList:
		count, encoded, err := readRDBLen(data, pos)
		if err != nil || encoded || count == 0 || count > uint64(len(data)) {
			return decodedKeyObject{}, errors.New("invalid Redis RDB list")
		}
		values := make([][]byte, 0, count)
		for i := uint64(0); i < count; i++ {
			value, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB list element")
			}
			values = append(values, value)
		}
		return decodedKeyObject{valueType: engine.TypeList, list: values}, nil

	case redisRDBTypeSet:
		count, encoded, err := readRDBLen(data, pos)
		if err != nil || encoded || count == 0 || count > uint64(len(data)) {
			return decodedKeyObject{}, errors.New("invalid Redis RDB set")
		}
		members := make([][]byte, 0, count)
		for i := uint64(0); i < count; i++ {
			member, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB set member")
			}
			members = append(members, member)
		}
		return decodedKeyObject{valueType: engine.TypeSet, set: members}, nil

	case redisRDBTypeHash:
		count, encoded, err := readRDBLen(data, pos)
		if err != nil || encoded || count == 0 || count > uint64(len(data)) {
			return decodedKeyObject{}, errors.New("invalid Redis RDB hash")
		}
		pairs := make([]engine.HashPair, 0, count)
		for i := uint64(0); i < count; i++ {
			field, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB hash field")
			}
			value, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB hash value")
			}
			pairs = append(pairs, engine.HashPair{Field: field, Value: value})
		}
		return decodedKeyObject{valueType: engine.TypeHash, hash: pairs}, nil

	case redisRDBTypeZSet:
		count, encoded, err := readRDBLen(data, pos)
		if err != nil || encoded || count == 0 || count > uint64(len(data)) {
			return decodedKeyObject{}, errors.New("invalid Redis RDB zset")
		}
		items := make([]engine.ZSetItem, 0, count)
		for i := uint64(0); i < count; i++ {
			member, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB zset member")
			}
			score, err := decodeRedisRDBTextDouble(data, pos)
			if err != nil {
				return decodedKeyObject{}, err
			}
			items = append(items, engine.ZSetItem{Member: member, Score: score})
		}
		return decodedKeyObject{valueType: engine.TypeZSet, zset: items}, nil

	case redisRDBTypeZSet2:
		count, encoded, err := readRDBLen(data, pos)
		if err != nil || encoded || count == 0 || count > uint64(len(data)) {
			return decodedKeyObject{}, errors.New("invalid Redis RDB zset")
		}
		items := make([]engine.ZSetItem, 0, count)
		for i := uint64(0); i < count; i++ {
			member, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB zset member")
			}
			if len(data)-*pos < 8 {
				return decodedKeyObject{}, errors.New("truncated Redis RDB zset score")
			}
			score := math.Float64frombits(binary.LittleEndian.Uint64(data[*pos : *pos+8]))
			*pos += 8
			if math.IsNaN(score) {
				return decodedKeyObject{}, errors.New("invalid Redis RDB zset score")
			}
			items = append(items, engine.ZSetItem{Member: member, Score: score})
		}
		return decodedKeyObject{valueType: engine.TypeZSet, zset: items}, nil

	case keyRDBTypeString:
		value, err := decodeRDBString(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB string")
		}
		return decodedKeyObject{valueType: engine.TypeString, scalar: value}, nil

	case redisRDBTypeHashMetadataPreGA, redisRDBTypeHashMetadata:
		var minExpire int64
		if objectType == redisRDBTypeHashMetadata {
			if len(data)-*pos < 8 {
				return decodedKeyObject{}, errors.New("truncated Redis RDB hash min expiry")
			}
			minExpire = int64(binary.LittleEndian.Uint64(data[*pos : *pos+8]))
			*pos += 8
			if minExpire < 0 {
				return decodedKeyObject{}, errors.New("invalid Redis RDB hash min expiry")
			}
		}

		count, encoded, err := readRDBLen(data, pos)
		if err != nil || encoded || count == 0 || count > uint64(len(data)) {
			return decodedKeyObject{}, errors.New("invalid Redis RDB hash metadata")
		}
		pairs := make([]engine.HashPair, 0, count)
		for i := uint64(0); i < count; i++ {
			ttl, ttlEncoded, err := readRDBLen(data, pos)
			if err != nil || ttlEncoded {
				return decodedKeyObject{}, errors.New("invalid Redis RDB hash field TTL")
			}

			var expiresAtMS int64
			if ttl != 0 {
				if objectType == redisRDBTypeHashMetadataPreGA {
					if ttl > uint64(math.MaxInt64) {
						return decodedKeyObject{}, errors.New("Redis RDB hash field TTL out of range")
					}
					expiresAtMS = int64(ttl)
				} else {
					if minExpire == 0 || ttl-1 > uint64(math.MaxInt64-minExpire) {
						return decodedKeyObject{}, errors.New("Redis RDB hash field TTL out of range")
					}
					expiresAtMS = minExpire + int64(ttl) - 1
				}
			}

			field, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB hash field")
			}
			value, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB hash value")
			}
			pairs = append(pairs, engine.HashPair{
				Field: field, Value: value, ExpiresAtMS: expiresAtMS,
			})
		}
		return decodedKeyObject{valueType: engine.TypeHash, hash: pairs}, nil

	case redisRDBTypeHashZipmap:
		raw, err := decodeRDBString(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB hash zipmap")
		}
		pairs, err := decodeRedisZipmap(raw)
		if err != nil || len(pairs) == 0 {
			return decodedKeyObject{}, errors.New("invalid Redis RDB hash zipmap")
		}
		return decodedKeyObject{valueType: engine.TypeHash, hash: pairs}, nil

	case redisRDBTypeListZiplist:
		raw, err := decodeRDBString(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB list ziplist")
		}
		values, err := decodeRedisZiplist(raw)
		if err != nil || len(values) == 0 {
			return decodedKeyObject{}, errors.New("invalid Redis RDB list ziplist")
		}
		return decodedKeyObject{valueType: engine.TypeList, list: values}, nil

	case redisRDBTypeHashZiplist:
		raw, err := decodeRDBString(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB hash ziplist")
		}
		values, err := decodeRedisZiplist(raw)
		if err != nil || len(values) == 0 || len(values)%2 != 0 {
			return decodedKeyObject{}, errors.New("invalid Redis RDB hash ziplist")
		}
		pairs := make([]engine.HashPair, 0, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			pairs = append(pairs, engine.HashPair{Field: values[i], Value: values[i+1]})
		}
		return decodedKeyObject{valueType: engine.TypeHash, hash: pairs}, nil

	case redisRDBTypeZSetZiplist:
		raw, err := decodeRDBString(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB zset ziplist")
		}
		values, err := decodeRedisZiplist(raw)
		if err != nil || len(values) == 0 || len(values)%2 != 0 {
			return decodedKeyObject{}, errors.New("invalid Redis RDB zset ziplist")
		}
		items := make([]engine.ZSetItem, 0, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			score, err := parseZSetScore(values[i+1])
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB zset ziplist score")
			}
			items = append(items, engine.ZSetItem{Member: values[i], Score: score})
		}
		return decodedKeyObject{valueType: engine.TypeZSet, zset: items}, nil

	case redisRDBTypeListQuicklist:
		nodes, encoded, err := readRDBLen(data, pos)
		if err != nil || encoded || nodes == 0 || nodes > uint64(len(data)) {
			return decodedKeyObject{}, errors.New("invalid Redis RDB legacy quicklist")
		}
		values := make([][]byte, 0)
		for i := uint64(0); i < nodes; i++ {
			raw, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB legacy quicklist node")
			}
			node, err := decodeRedisZiplist(raw)
			if err != nil || len(node) == 0 {
				return decodedKeyObject{}, errors.New("invalid Redis RDB legacy quicklist ziplist")
			}
			values = append(values, node...)
		}
		if len(values) == 0 {
			return decodedKeyObject{}, errors.New("empty Redis RDB legacy quicklist")
		}
		return decodedKeyObject{valueType: engine.TypeList, list: values}, nil

	case keyRDBTypeHashListpack:
		raw, err := decodeRDBString(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB hash")
		}
		values, err := decodeRedisListpack(raw)
		if err != nil || len(values) == 0 || len(values)%2 != 0 {
			return decodedKeyObject{}, errors.New("invalid Redis RDB hash listpack")
		}
		pairs := make([]engine.HashPair, 0, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			pairs = append(pairs, engine.HashPair{Field: values[i], Value: values[i+1]})
		}
		return decodedKeyObject{valueType: engine.TypeHash, hash: pairs}, nil

	case keyRDBTypeSetIntset:
		raw, err := decodeRDBString(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB intset")
		}
		members, err := decodeRedisIntset(raw)
		if err != nil || len(members) == 0 {
			return decodedKeyObject{}, errors.New("invalid Redis RDB intset")
		}
		return decodedKeyObject{valueType: engine.TypeSet, set: members}, nil

	case keyRDBTypeSetListpack:
		raw, err := decodeRDBString(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB set")
		}
		members, err := decodeRedisListpack(raw)
		if err != nil || len(members) == 0 {
			return decodedKeyObject{}, errors.New("invalid Redis RDB set listpack")
		}
		return decodedKeyObject{valueType: engine.TypeSet, set: members}, nil

	case keyRDBTypeListQuicklist2:
		nodes, encoded, err := readRDBLen(data, pos)
		if err != nil || encoded || nodes == 0 || nodes > uint64(len(data)) {
			return decodedKeyObject{}, errors.New("invalid Redis RDB quicklist")
		}
		elements := make([][]byte, 0)
		for i := uint64(0); i < nodes; i++ {
			container, encoded, err := readRDBLen(data, pos)
			if err != nil || encoded {
				return decodedKeyObject{}, errors.New("invalid Redis RDB quicklist container")
			}
			raw, err := decodeRDBString(data, pos)
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB quicklist payload")
			}
			switch container {
			case 1: // QUICKLIST_NODE_CONTAINER_PLAIN
				elements = append(elements, raw)
			case 2: // QUICKLIST_NODE_CONTAINER_PACKED
				node, err := decodeRedisListpack(raw)
				if err != nil || len(node) == 0 {
					return decodedKeyObject{}, errors.New("invalid Redis RDB quicklist listpack")
				}
				elements = append(elements, node...)
			default:
				return decodedKeyObject{}, errors.New("unsupported Redis RDB quicklist container")
			}
		}
		if len(elements) == 0 {
			return decodedKeyObject{}, errors.New("empty Redis RDB list")
		}
		return decodedKeyObject{valueType: engine.TypeList, list: elements}, nil

	case keyRDBTypeZSetListpack:
		raw, err := decodeRDBString(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB zset")
		}
		values, err := decodeRedisListpack(raw)
		if err != nil || len(values) == 0 || len(values)%2 != 0 {
			return decodedKeyObject{}, errors.New("invalid Redis RDB zset listpack")
		}
		items := make([]engine.ZSetItem, 0, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			score, err := parseZSetScore(values[i+1])
			if err != nil {
				return decodedKeyObject{}, errors.New("invalid Redis RDB zset score")
			}
			items = append(items, engine.ZSetItem{Member: values[i], Score: score})
		}
		return decodedKeyObject{valueType: engine.TypeZSet, zset: items}, nil

	case keyRDBTypeStreamListpacks3:
		snapshot, err := decodeStreamDump(data, pos)
		if err != nil {
			return decodedKeyObject{}, errors.New("invalid Redis RDB stream")
		}
		return decodedKeyObject{valueType: engine.TypeStream, stream: &snapshot}, nil
	default:
		return decodedKeyObject{}, fmt.Errorf("unsupported Redis RDB object type %d", objectType)
	}
}

func decodeRedisFullSyncRDB(data []byte) ([]persistence.Record, error) {
	if len(data) < 18 || len(data) > persistence.MaxFrameBytes {
		return nil, errors.New("invalid Redis RDB length")
	}
	if string(data[:5]) != "REDIS" {
		return nil, errors.New("invalid Redis RDB magic")
	}
	version, err := strconv.Atoi(string(data[5:9]))
	if err != nil || version < 1 || version > redisRDBMaxSupportedVersion {
		return nil, errors.New("unsupported Redis RDB version")
	}

	checksumPos := len(data) - 8
	storedChecksum := binary.LittleEndian.Uint64(data[checksumPos:])
	if storedChecksum != 0 && redisCRC64(data[:checksumPos]) != storedChecksum {
		return nil, errors.New("Redis RDB checksum mismatch")
	}

	pos := 9
	currentDB := uint64(0)
	var expiresAtMS int64
	records := []persistence.Record{{Reset: true}}

	for pos < checksumPos {
		opcode := data[pos]
		pos++

		switch opcode {
		case redisRDBOpcodeAux:
			if _, err := decodeRDBString(data[:checksumPos], &pos); err != nil {
				return nil, errors.New("invalid Redis RDB AUX key")
			}
			if _, err := decodeRDBString(data[:checksumPos], &pos); err != nil {
				return nil, errors.New("invalid Redis RDB AUX value")
			}
			continue

		case redisRDBOpcodeSelectDB:
			db, encoded, err := readRDBLen(data[:checksumPos], &pos)
			if err != nil || encoded {
				return nil, errors.New("invalid Redis RDB database selector")
			}
			currentDB = db
			if currentDB != 0 {
				return nil, errors.New("Redis RDB contains unsupported nonzero database")
			}
			continue

		case redisRDBOpcodeResizeDB:
			if _, encoded, err := readRDBLen(data[:checksumPos], &pos); err != nil || encoded {
				return nil, errors.New("invalid Redis RDB resize hint")
			}
			if _, encoded, err := readRDBLen(data[:checksumPos], &pos); err != nil || encoded {
				return nil, errors.New("invalid Redis RDB expire resize hint")
			}
			continue

		case redisRDBOpcodeExpireTimeMS:
			if pos+8 > checksumPos {
				return nil, errors.New("truncated Redis RDB millisecond expiry")
			}
			expiresAtMS = int64(binary.LittleEndian.Uint64(data[pos : pos+8]))
			pos += 8
			continue

		case redisRDBOpcodeExpireTime:
			if pos+4 > checksumPos {
				return nil, errors.New("truncated Redis RDB second expiry")
			}
			seconds := uint64(binary.LittleEndian.Uint32(data[pos : pos+4]))
			if seconds > uint64(^uint64(0)>>1)/1000 {
				return nil, errors.New("Redis RDB expiry out of range")
			}
			expiresAtMS = int64(seconds * 1000)
			pos += 4
			continue

		case redisRDBOpcodeIdle:
			if _, encoded, err := readRDBLen(data[:checksumPos], &pos); err != nil || encoded {
				return nil, errors.New("invalid Redis RDB idle metadata")
			}
			continue

		case redisRDBOpcodeFreq:
			if pos >= checksumPos {
				return nil, errors.New("truncated Redis RDB frequency metadata")
			}
			pos++
			continue

		case redisRDBOpcodeEOF:
			if pos != checksumPos {
				return nil, errors.New("unexpected bytes after Redis RDB EOF")
			}
			return records, nil
		}

		if currentDB != 0 {
			return nil, errors.New("Redis RDB contains unsupported nonzero database")
		}

		key, err := decodeRDBString(data[:checksumPos], &pos)
		if err != nil {
			return nil, errors.New("invalid Redis RDB key")
		}
		object, err := decodeRedisRDBObjectAt(data[:checksumPos], &pos, opcode)
		if err != nil {
			return nil, err
		}
		record, err := buildRestoreRecord(string(key), object, expiresAtMS)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
		expiresAtMS = 0
	}

	return nil, errors.New("Redis RDB missing EOF")
}

func isRedisRDBPayload(payload []byte) bool {
	return len(payload) >= 9 && strings.HasPrefix(string(payload[:9]), "REDIS")
}
