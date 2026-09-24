package server

import (
	"encoding/binary"
	"errors"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

const (
	redisZipmapBigLen = byte(254)
	redisZipmapEnd    = byte(255)
)

func decodeRedisZipmapLength(raw []byte, pos *int) (int, error) {
	if *pos >= len(raw) {
		return 0, errors.New("truncated Redis zipmap length")
	}
	first := raw[*pos]
	*pos++
	if first < redisZipmapBigLen {
		return int(first), nil
	}
	if first == redisZipmapEnd || len(raw)-*pos < 4 {
		return 0, errors.New("invalid Redis zipmap length")
	}
	n := binary.LittleEndian.Uint32(raw[*pos : *pos+4])
	*pos += 4
	if uint64(n) > uint64(persistence.MaxFrameBytes) {
		return 0, errors.New("Redis zipmap length exceeds limit")
	}
	return int(n), nil
}

func decodeRedisZipmap(raw []byte) ([]engine.HashPair, error) {
	if len(raw) < 2 || len(raw) > persistence.MaxFrameBytes || raw[len(raw)-1] != redisZipmapEnd {
		return nil, errors.New("invalid Redis zipmap")
	}

	declared := raw[0]
	pos := 1
	pairs := make([]engine.HashPair, 0)

	for pos < len(raw)-1 {
		keyLen, err := decodeRedisZipmapLength(raw, &pos)
		if err != nil || keyLen > len(raw)-pos {
			return nil, errors.New("invalid Redis zipmap key")
		}
		key := append([]byte(nil), raw[pos:pos+keyLen]...)
		pos += keyLen

		valueLen, err := decodeRedisZipmapLength(raw, &pos)
		if err != nil || pos >= len(raw) {
			return nil, errors.New("invalid Redis zipmap value")
		}
		free := int(raw[pos])
		pos++
		if valueLen > len(raw)-pos || free > len(raw)-pos-valueLen {
			return nil, errors.New("invalid Redis zipmap value")
		}
		value := append([]byte(nil), raw[pos:pos+valueLen]...)
		pos += valueLen + free

		pairs = append(pairs, engine.HashPair{Field: key, Value: value})
		if len(pairs) > persistence.MaxFrameBytes {
			return nil, errors.New("Redis zipmap entry count exceeds limit")
		}
	}

	if pos != len(raw)-1 {
		return nil, errors.New("invalid Redis zipmap terminator")
	}
	if declared != redisZipmapBigLen && int(declared) != len(pairs) {
		return nil, errors.New("invalid Redis zipmap entry count")
	}
	return pairs, nil
}
