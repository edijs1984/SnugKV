package engine

import (
	"errors"
	"math"
	"reflect"
	"sort"

	"snugkv/internal/jsonvalue"
)

// JSONUpdateFunc is applied to one value matched by a JSONPath query. It returns
// the replacement (when changed), and the result reported for that match; a nil
// result is reported as null (the value had the wrong type).
type JSONUpdateFunc func(old any) (updated any, changed bool, result any, err error)

// JSONUpdateMatches applies fn to every value a JSONPath query matches and
// returns one result per match, in query order. The document is written back
// once, and only if some match changed; if fn fails nothing is written.
// found is false when the key does not exist.
func (s *Store) JSONUpdateMatches(key, path string, fn JSONUpdateFunc) (results []any, found bool, err error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, exists := sh.get(key)
	if !exists {
		return nil, false, nil
	}
	if sh.expired(key, e, now) {
		s.remove(sh, key)
		return nil, false, nil
	}
	if e.valueType != TypeJSON {
		return nil, false, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return nil, false, errors.New("WRONGTYPE value is not valid JSON")
	}
	locations, err := jsonvalue.Locate(root, path)
	if err != nil {
		return nil, false, err
	}

	results = make([]any, 0, len(locations))
	mutated := false
	for _, loc := range locations {
		old, ok := jsonvalue.GetAt(root, loc)
		if !ok {
			results = append(results, nil)
			continue
		}
		updated, changed, result, err := fn(old)
		if err != nil {
			return nil, false, err
		}
		if changed {
			next, ok := jsonvalue.SetAt(root, loc, updated)
			if !ok {
				results = append(results, nil)
				continue
			}
			root = next
			mutated = true
		}
		results = append(results, result)
	}

	if mutated {
		if err := s.publishJSONMutationLocked(sh, key, e, root); err != nil {
			return nil, false, err
		}
	}
	return results, true, nil
}

func jsonNumberOp(apply func(float64) float64) JSONUpdateFunc {
	return func(old any) (any, bool, any, error) {
		number, ok := old.(float64)
		if !ok {
			return nil, false, nil, nil
		}
		result := apply(number)
		if math.IsNaN(result) || math.IsInf(result, 0) {
			return nil, false, nil, errors.New("ERR result is not a number")
		}
		return result, true, result, nil
	}
}

// JSONNumIncrByMatches adds increment to every number a JSONPath query matches.
func (s *Store) JSONNumIncrByMatches(key, path string, increment float64) ([]any, bool, error) {
	return s.JSONUpdateMatches(key, path, jsonNumberOp(func(n float64) float64 { return n + increment }))
}

// JSONNumMultByMatches multiplies every number a JSONPath query matches.
func (s *Store) JSONNumMultByMatches(key, path string, multiplier float64) ([]any, bool, error) {
	return s.JSONUpdateMatches(key, path, jsonNumberOp(func(n float64) float64 { return n * multiplier }))
}

// JSONToggleMatches flips every boolean a JSONPath query matches; each result is the new value.
func (s *Store) JSONToggleMatches(key, path string) ([]any, bool, error) {
	return s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		value, ok := old.(bool)
		if !ok {
			return nil, false, nil, nil
		}
		return !value, true, !value, nil
	})
}

// JSONStrAppendMatches appends a JSON string to every string matched; each result is the new length.
func (s *Store) JSONStrAppendMatches(key, path string, raw []byte) ([]any, bool, error) {
	appendValue, err := jsonvalue.Parse(raw)
	if err != nil {
		return nil, false, err
	}
	suffix, ok := appendValue.(string)
	if !ok {
		return nil, false, errors.New("ERR wrong type of value - expected string")
	}
	return s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		text, ok := old.(string)
		if !ok {
			return nil, false, nil, nil
		}
		text += suffix
		return text, true, int64(len([]rune(text))), nil
	})
}

func parseJSONValues(raws [][]byte) ([]any, error) {
	values := make([]any, 0, len(raws))
	for _, raw := range raws {
		value, err := jsonvalue.Parse(raw)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

// JSONArrAppendMatches appends to every array matched; each result is the new length.
func (s *Store) JSONArrAppendMatches(key, path string, rawValues [][]byte) ([]any, bool, error) {
	values, err := parseJSONValues(rawValues)
	if err != nil {
		return nil, false, err
	}
	return s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		array, ok := old.([]any)
		if !ok {
			return nil, false, nil, nil
		}
		array = append(array[:len(array):len(array)], values...)
		return array, true, int64(len(array)), nil
	})
}

// JSONArrInsertMatches inserts into every array matched; each result is the new length.
func (s *Store) JSONArrInsertMatches(key, path string, index int, rawValues [][]byte) ([]any, bool, error) {
	values, err := parseJSONValues(rawValues)
	if err != nil {
		return nil, false, err
	}
	return s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		array, ok := old.([]any)
		if !ok {
			return nil, false, nil, nil
		}
		at := index
		if at < 0 {
			at = len(array) + at
			if at < 0 {
				at = 0
			}
		}
		if at > len(array) {
			return nil, false, nil, errors.New("ERR index out of bounds")
		}
		out := make([]any, 0, len(array)+len(values))
		out = append(out, array[:at]...)
		out = append(out, values...)
		out = append(out, array[at:]...)
		return out, true, int64(len(out)), nil
	})
}

// JSONArrPopMatches pops from every array matched; each result is the encoded
// element, or nil for an empty array or a value that is not an array.
func (s *Store) JSONArrPopMatches(key, path string, index int) ([]any, bool, error) {
	return s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		array, ok := old.([]any)
		if !ok || len(array) == 0 {
			return nil, false, nil, nil
		}
		at := index
		if at < 0 {
			at = len(array) + at
		}
		if at < 0 {
			at = 0
		}
		if at >= len(array) {
			at = len(array) - 1
		}
		popped := array[at]
		encoded, err := jsonvalue.Encode(popped)
		if err != nil {
			return nil, false, nil, err
		}
		rest := make([]any, 0, len(array)-1)
		rest = append(rest, array[:at]...)
		rest = append(rest, array[at+1:]...)
		return rest, true, encoded, nil
	})
}

// JSONArrTrimMatches trims every array matched; each result is the new length.
func (s *Store) JSONArrTrimMatches(key, path string, start, stop int) ([]any, bool, error) {
	return s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		array, ok := old.([]any)
		if !ok {
			return nil, false, nil, nil
		}
		n := len(array)
		from, to := start, stop
		if from < 0 {
			from = n + from
		}
		if to < 0 {
			to = n + to
		}
		if from < 0 {
			from = 0
		}
		if to >= n {
			to = n - 1
		}
		trimmed := []any{}
		if n > 0 && from < n && from <= to && to >= 0 {
			trimmed = append(trimmed, array[from:to+1]...)
		}
		return trimmed, true, int64(len(trimmed)), nil
	})
}

// JSONClearMatches empties arrays and objects and zeroes numbers; the result is the number cleared.
func (s *Store) JSONClearMatches(key, path string) (int64, error) {
	results, _, err := s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		switch value := old.(type) {
		case []any:
			if len(value) == 0 {
				return nil, false, nil, nil
			}
			return []any{}, true, int64(1), nil
		case map[string]any:
			if len(value) == 0 {
				return nil, false, nil, nil
			}
			return map[string]any{}, true, int64(1), nil
		case float64:
			if value == 0 {
				return nil, false, nil, nil
			}
			return float64(0), true, int64(1), nil
		}
		return nil, false, nil, nil
	})
	if err != nil {
		return 0, err
	}
	var cleared int64
	for _, r := range results {
		if r != nil {
			cleared++
		}
	}
	return cleared, nil
}

// JSONLenMatches reports the length of every string, array or object matched
// (kind is "string", "array" or "object"); other values report nil.
func (s *Store) JSONLenMatches(key, path, kind string) ([]any, bool, error) {
	return s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		switch kind {
		case "string":
			if text, ok := old.(string); ok {
				return nil, false, int64(len([]rune(text))), nil
			}
		case "array":
			if array, ok := old.([]any); ok {
				return nil, false, int64(len(array)), nil
			}
		case "object":
			if object, ok := old.(map[string]any); ok {
				return nil, false, int64(len(object)), nil
			}
		}
		return nil, false, nil, nil
	})
}

// JSONObjKeysMatches lists the keys of every object matched ([]string), nil for other values.
func (s *Store) JSONObjKeysMatches(key, path string) ([]any, bool, error) {
	return s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		object, ok := old.(map[string]any)
		if !ok {
			return nil, false, nil, nil
		}
		keys := make([]string, 0, len(object))
		for name := range object {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		return nil, false, keys, nil
	})
}

// JSONArrIndexMatches finds a value in every array matched. stop is exclusive
// and 0 (or omitted) means to the end of the array.
func (s *Store) JSONArrIndexMatches(key, path string, rawScalar []byte, start, stop *int) ([]any, bool, error) {
	target, err := jsonvalue.Parse(rawScalar)
	if err != nil {
		return nil, false, err
	}
	return s.JSONUpdateMatches(key, path, func(old any) (any, bool, any, error) {
		array, ok := old.([]any)
		if !ok {
			return nil, false, nil, nil
		}
		from, to := 0, len(array)
		if start != nil {
			from = *start
			if from < 0 {
				from += len(array)
			}
		}
		if stop != nil && *stop != 0 {
			to = *stop
			if to < 0 {
				to += len(array)
			}
		}
		if from < 0 {
			from = 0
		}
		if to > len(array) {
			to = len(array)
		}
		for i := from; i < to; i++ {
			if reflect.DeepEqual(array[i], target) {
				return nil, false, int64(i), nil
			}
		}
		return nil, false, int64(-1), nil
	})
}
