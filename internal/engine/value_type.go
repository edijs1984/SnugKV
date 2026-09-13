package engine

import (
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
	if !ok || e.expired(s.now()) {
		return TypeString, false
	}

	return e.valueType, true
}
