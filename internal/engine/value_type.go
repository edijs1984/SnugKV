package engine

import (
	"math"
	"strconv"
	"unicode/utf8"
)

// ValueType is compact semantic metadata for a stored value.
//
// It is intentionally independent of codec.ID:
//
//	ValueType = what the value logically represents
//	codec.ID   = how the bytes are physically encoded
//
// Redis-compatible commands continue to expose the exact original byte
// representation through decode().
type ValueType uint8

const (
	TypeString ValueType = iota
	TypeInt64
	TypeUint64
	TypeFloat64
	TypeBool
	TypeBytes
	TypeJSON
	TypeHash
	TypeSet
	TypeList
	TypeZSet
	TypeStream
)

func (t ValueType) String() string {
	switch t {
	case TypeString:
		return "STRING"
	case TypeInt64:
		return "INT64"
	case TypeUint64:
		return "UINT64"
	case TypeFloat64:
		return "FLOAT64"
	case TypeBool:
		return "BOOL"
	case TypeBytes:
		return "BYTES"
	case TypeJSON:
		return "JSON"
	case TypeHash:
		return "HASH"
	case TypeSet:
		return "SET"
	case TypeList:
		return "LIST"
	case TypeZSet:
		return "ZSET"
	case TypeStream:
		return "STREAM"
	default:
		return "UNKNOWN"
	}
}

// classifyValue performs conservative automatic type inference.
//
// Important rule: classification must NEVER change Redis-visible bytes.
// The codec layer remains responsible for lossless physical encoding.
func classifyValue(value []byte) ValueType {
	// Canonical signed integer.
	//
	// strconv.FormatInt equality deliberately rejects:
	//   00123
	//   +123
	//   -0
	//   00
	//
	// because converting those to integer semantics could lose the
	// original representation.
	if len(value) > 0 {
		if n, err := strconv.ParseInt(string(value), 10, 64); err == nil {
			if strconv.FormatInt(n, 10) == string(value) {
				return TypeInt64
			}
		}
	}

	// Canonical UINT64 values above MaxInt64.
	//
	// Values <= MaxInt64 are intentionally classified as INT64 first.
	if len(value) > 0 {
		if n, err := strconv.ParseUint(string(value), 10, 64); err == nil {
			if strconv.FormatUint(n, 10) == string(value) &&
				n > uint64(^uint64(0)>>1) {
				return TypeUint64
			}
	}

	// Canonical FLOAT64.
	//
	// Only infer FLOAT64 when IEEE-754 -> text reconstruction produces the
	// exact same bytes. This deliberately leaves alternate representations
	// such as "1.00" and "1e3" as STRING.
	if len(value) > 0 {
		text := string(value)

		if n, err := strconv.ParseFloat(text, 64); err == nil &&
			!math.IsNaN(n) &&
			!math.IsInf(n, 0) &&
			strconv.FormatFloat(n, 'g', -1, 64) == text {
			return TypeFloat64
		}
	}

	// Canonical BOOL.
	//
	// Only exact lowercase Redis-visible representations are inferred.
	// Case variants remain STRING so classification never normalizes bytes.
	if string(value) == "true" || string(value) == "false" {
		return TypeBool
	}

	// Redis strings are binary-safe. Invalid UTF-8 is therefore treated
	// explicitly as BYTES rather than STRING.
	if !utf8.Valid(value) {
		return TypeBytes
	}

	return TypeString
}

// ValueTypeOf returns SnugKV's semantic type tag for a live key.
func (s *Store) ValueTypeOf(key string) (ValueType, bool) {
	sh := s.shardFor(key)

	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return TypeString, false
	}

	return e.valueType, true
}
