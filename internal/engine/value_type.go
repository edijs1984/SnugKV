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
	TypeBloom
	TypeCuckoo
	TypeCMS
	TypeTopK
	TypeTDigest
	TypeTimeSeries
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
	case TypeBloom:
		return "BLOOM"
	case TypeCuckoo:
		return "CUCKOO"
	case TypeCMS:
		return "CMS"
	case TypeTopK:
		return "TOPK"
	case TypeTDigest:
		return "TDIGEST"
	case TypeTimeSeries:
		return "TIMESERIES"
	default:
		return "UNKNOWN"
	}
}

// classifyValue performs conservative automatic type inference.
//
// Important rule: classification must NEVER change Redis-visible bytes.
// The codec layer remains responsible for lossless physical encoding.
func classifyValue(value []byte) ValueType {
	// Canonical BOOL. Check this before numeric parsing so the common textual
	// booleans avoid three failed strconv parses.
	if len(value) == 4 &&
		value[0] == 't' &&
		value[1] == 'r' &&
		value[2] == 'u' &&
		value[3] == 'e' {
		return TypeBool
	}
	if len(value) == 5 &&
		value[0] == 'f' &&
		value[1] == 'a' &&
		value[2] == 'l' &&
		value[3] == 's' &&
		value[4] == 'e' {
		return TypeBool
	}

	// Numeric canonical forms can only begin with a digit or '-'. Reject the
	// overwhelmingly common ordinary-string case before allocating a string or
	// invoking strconv. A leading '+' can never be canonical because the
	// formatter used below never emits it.
	numericCandidate := len(value) > 0 &&
		((value[0] >= '0' && value[0] <= '9') || value[0] == '-')

	if numericCandidate {
		text := string(value)

		// Canonical signed integer.
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			if strconv.FormatInt(n, 10) == text {
				return TypeInt64
			}
		}

		// Canonical UINT64 values above MaxInt64.
		if value[0] != '-' {
			if n, err := strconv.ParseUint(text, 10, 64); err == nil {
				if strconv.FormatUint(n, 10) == text &&
					n > uint64(^uint64(0)>>1) {
					return TypeUint64
				}
			}
		}

		// Canonical FLOAT64.
		if n, err := strconv.ParseFloat(text, 64); err == nil &&
			!math.IsNaN(n) &&
			!math.IsInf(n, 0) &&
			strconv.FormatFloat(n, 'g', -1, 64) == text {
			return TypeFloat64
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
	if !ok || sh.expired(key, e, s.now()) {
		return TypeString, false
	}

	return e.valueType, true
}
