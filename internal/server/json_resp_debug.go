package server

import (
	"math"
	"sort"
	"strconv"
)

func jsonRESPValue(value any) []byte {
	switch v := value.(type) {
	case nil:
		return nullBulk()
	case bool:
		if v {
			return []byte("+true\r\n")
		}
		return []byte("+false\r\n")
	case string:
		return formatBulkString([]byte(v))
	case float64:
		if math.Trunc(v) == v && v >= math.MinInt64 && v <= math.MaxInt64 {
			return integer(int64(v))
		}
		return formatBulkString([]byte(strconv.FormatFloat(v, 'g', 17, 64)))
	case []any:
		items := make([][]byte, 0, len(v)+1)
		items = append(items, []byte("+[\r\n"))
		for _, item := range v {
			items = append(items, jsonRESPValue(item))
		}
		return array(items...)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		items := make([][]byte, 0, 1+2*len(keys))
		items = append(items, []byte("+{\r\n"))
		for _, key := range keys {
			items = append(items, formatBulkString([]byte(key)))
			items = append(items, jsonRESPValue(v[key]))
		}
		return array(items...)
	default:
		return nullBulk()
	}
}

func jsonDebugMemory(value any) int64 {
	switch v := value.(type) {
	case nil, bool, float64:
		return 8
	case string:
		return 8 + int64(len(v))
	case []any:
		total := int64(24)
		for _, item := range v {
			total += 8 + jsonDebugMemory(item)
		}
		return total
	case map[string]any:
		total := int64(24)
		for key, item := range v {
			total += 16 + int64(len(key))
			total += 8 + jsonDebugMemory(item)
		}
		return total
	default:
		return 8
	}
}
