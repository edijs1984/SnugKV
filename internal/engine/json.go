package engine

import (
	"errors"
	"math"
	"sort"
	"reflect"
	"snugkv/internal/jsonvalue"
)

func (s *Store) JSONSet(
	key, path string,
	raw []byte,
	nx, xx bool,
) (bool, error) {
	if nx && xx {
		return false, errors.New("ERR syntax error")
	}

	newValue, err := jsonvalue.Parse(raw)
	if err != nil {
		return false, err
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	e, exists := sh.get(key)

	if exists && sh.expired(key, e, now) {
		s.remove(sh, key)
		exists = false
	}

	if !exists {
		if xx {
			return false, nil
		}

		if path != "$" && path != "." {
			return false, errors.New("ERR new objects must be created at the root")
		}

		encoded, err := jsonvalue.Encode(newValue)
		if err != nil {
			return false, err
		}

		entry := s.makeEntry(encoded)
		entry.valueType = TypeJSON

		if err := s.publish(sh, key, entry); err != nil {
			return false, err
		}

		s.observeJSONShapeLocked(sh, encoded)

		return true, nil
	}

	if e.valueType != TypeJSON {
		return false, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return false, errors.New("WRONGTYPE value is not valid JSON")
	}

	_, pathExists, err := jsonvalue.Get(root, path)
	if err != nil {
		return false, err
	}

	if nx && pathExists {
		return false, nil
	}

	if xx && !pathExists {
		return false, nil
	}

	updatedRoot, err := jsonvalue.Set(root, path, newValue)
	if err != nil {
		return false, err
	}

	encoded, err := jsonvalue.Encode(updatedRoot)
	if err != nil {
		return false, err
	}

	updated := s.makeEntry(encoded)
	updated.valueType = TypeJSON
	updated.expiresAt = sh.expirationAt(key, e)

	if err := s.publish(sh, key, updated); err != nil {
		return false, err
	}

	return true, nil
}

func (s *Store) JSONGet(key, path string) ([]byte, bool, error) {
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

	value, found, err := jsonvalue.Get(root, path)
	if err != nil {
		return nil, false, err
	}

	if !found {
		return nil, false, nil
	}

	encoded, err := jsonvalue.Encode(value)
	if err != nil {
		return nil, false, err
	}

	return encoded, true, nil
}

func (s *Store) JSONType(key, path string) (string, bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	e, exists := sh.get(key)

	if !exists {
		return "", false, nil
	}

	if sh.expired(key, e, now) {
		s.remove(sh, key)
		return "", false, nil
	}

	if e.valueType != TypeJSON {
		return "", false, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return "", false, errors.New("WRONGTYPE value is not valid JSON")
	}

	value, found, err := jsonvalue.Get(root, path)
	if err != nil {
		return "", false, err
	}

	if !found {
		return "", false, nil
	}

	return jsonvalue.TypeOf(value), true, nil
}

func (s *Store) JSONDel(key, path string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	e, exists := sh.get(key)

	if !exists {
		return 0, nil
	}

	if sh.expired(key, e, now) {
		s.remove(sh, key)
		return 0, nil
	}

	if e.valueType != TypeJSON {
		return 0, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	if path == "$" {
		s.remove(sh, key)
		return 1, nil
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return 0, errors.New("WRONGTYPE value is not valid JSON")
	}

	updatedRoot, deleted, err := jsonvalue.Delete(root, path)
	if err != nil {
		return 0, err
	}

	if !deleted {
		return 0, nil
	}

	encoded, err := jsonvalue.Encode(updatedRoot)
	if err != nil {
		return 0, err
	}

	updated := s.makeEntry(encoded)
	updated.valueType = TypeJSON
	updated.expiresAt = sh.expirationAt(key, e)

	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}

	return 1, nil
}


func (s *Store) JSONNumIncrBy(key, path string, increment float64) ([]byte, bool, error) {
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

	value, found, err := jsonvalue.Get(root, path)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	number, ok := value.(float64)
	if !ok {
		return nil, false, nil
	}

	result := number + increment
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return nil, false, errors.New("ERR result is not a number")
	}

	updatedRoot, err := jsonvalue.Set(root, path, result)
	if err != nil {
		return nil, false, err
	}

	encodedRoot, err := jsonvalue.Encode(updatedRoot)
	if err != nil {
		return nil, false, err
	}

	updated := s.makeEntry(encodedRoot)
	updated.valueType = TypeJSON
	updated.expiresAt = sh.expirationAt(key, e)

	if err := s.publish(sh, key, updated); err != nil {
		return nil, false, err
	}

	encodedResult, err := jsonvalue.Encode(result)
	if err != nil {
		return nil, false, err
	}
	return encodedResult, true, nil
}

func (s *Store) JSONStrLen(key, path string) (int64, bool, error) {
	value, found, err := s.jsonValueAtPath(key, path)
	if err != nil || !found {
		return 0, found, err
	}
	str, ok := value.(string)
	if !ok {
		return 0, false, nil
	}
	return int64(len([]rune(str))), true, nil
}

func (s *Store) JSONArrLen(key, path string) (int64, bool, error) {
	value, found, err := s.jsonValueAtPath(key, path)
	if err != nil || !found {
		return 0, found, err
	}
	arr, ok := value.([]any)
	if !ok {
		return 0, false, nil
	}
	return int64(len(arr)), true, nil
}

func (s *Store) JSONObjLen(key, path string) (int64, bool, error) {
	value, found, err := s.jsonValueAtPath(key, path)
	if err != nil || !found {
		return 0, found, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return 0, false, nil
	}
	return int64(len(obj)), true, nil
}

func (s *Store) jsonValueAtPath(key, path string) (any, bool, error) {
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
	return jsonvalue.Get(root, path)
}


func (s *Store) JSONArrAppend(key, path string, rawValues [][]byte) (int64, bool, error) {
	values := make([]any, 0, len(rawValues))
	for _, raw := range rawValues {
		value, err := jsonvalue.Parse(raw)
		if err != nil {
			return 0, false, err
		}
		values = append(values, value)
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, exists := sh.get(key)
	if !exists {
		return 0, false, nil
	}
	if sh.expired(key, e, now) {
		s.remove(sh, key)
		return 0, false, nil
	}
	if e.valueType != TypeJSON {
		return 0, false, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return 0, false, errors.New("WRONGTYPE value is not valid JSON")
	}
	current, found, err := jsonvalue.Get(root, path)
	if err != nil || !found {
		return 0, found, err
	}
	arr, ok := current.([]any)
	if !ok {
		return 0, false, nil
	}

	arr = append(arr, values...)
	updatedRoot, err := jsonvalue.Set(root, path, arr)
	if err != nil {
		return 0, false, err
	}
	if err := s.publishJSONMutationLocked(sh, key, e, updatedRoot); err != nil {
		return 0, false, err
	}
	return int64(len(arr)), true, nil
}

func (s *Store) JSONStrAppend(key, path string, raw []byte) (int64, bool, error) {
	appendValue, err := jsonvalue.Parse(raw)
	if err != nil {
		return 0, false, err
	}
	suffix, ok := appendValue.(string)
	if !ok {
		return 0, false, errors.New("ERR expected a JSON string")
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, exists := sh.get(key)
	if !exists {
		return 0, false, nil
	}
	if sh.expired(key, e, now) {
		s.remove(sh, key)
		return 0, false, nil
	}
	if e.valueType != TypeJSON {
		return 0, false, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return 0, false, errors.New("WRONGTYPE value is not valid JSON")
	}
	current, found, err := jsonvalue.Get(root, path)
	if err != nil || !found {
		return 0, found, err
	}
	str, ok := current.(string)
	if !ok {
		return 0, false, nil
	}

	str += suffix
	updatedRoot, err := jsonvalue.Set(root, path, str)
	if err != nil {
		return 0, false, err
	}
	if err := s.publishJSONMutationLocked(sh, key, e, updatedRoot); err != nil {
		return 0, false, err
	}
	return int64(len([]rune(str))), true, nil
}

func (s *Store) JSONObjKeys(key, path string) ([]string, bool, error) {
	value, found, err := s.jsonValueAtPath(key, path)
	if err != nil || !found {
		return nil, found, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, false, nil
	}

	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, true, nil
}

func (s *Store) JSONToggle(key, path string) (bool, bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, exists := sh.get(key)
	if !exists {
		return false, false, nil
	}
	if sh.expired(key, e, now) {
		s.remove(sh, key)
		return false, false, nil
	}
	if e.valueType != TypeJSON {
		return false, false, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return false, false, errors.New("WRONGTYPE value is not valid JSON")
	}
	current, found, err := jsonvalue.Get(root, path)
	if err != nil || !found {
		return false, found, err
	}
	value, ok := current.(bool)
	if !ok {
		return false, false, nil
	}

	value = !value
	updatedRoot, err := jsonvalue.Set(root, path, value)
	if err != nil {
		return false, false, err
	}
	if err := s.publishJSONMutationLocked(sh, key, e, updatedRoot); err != nil {
		return false, false, err
	}
	return value, true, nil
}

func (s *Store) publishJSONMutationLocked(sh *shard, key string, previous entry, root any) error {
	encoded, err := jsonvalue.Encode(root)
	if err != nil {
		return err
	}

	updated := s.makeEntry(encoded)
	updated.valueType = TypeJSON
	updated.expiresAt = sh.expirationAt(key, previous)
	return s.publish(sh, key, updated)
}


func (s *Store) JSONArrPop(key, path string, index int) ([]byte, bool, error) {
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
	current, found, err := jsonvalue.Get(root, path)
	if err != nil || !found {
		return nil, found, err
	}
	arr, ok := current.([]any)
	if !ok {
		return nil, false, nil
	}
	if len(arr) == 0 {
		return nil, true, nil
	}

	if index < 0 {
		index = len(arr) + index
	}
	if index < 0 {
		index = 0
	}
	if index >= len(arr) {
		index = len(arr) - 1
	}

	popped := arr[index]
	arr = append(arr[:index], arr[index+1:]...)

	updatedRoot, err := jsonvalue.Set(root, path, arr)
	if err != nil {
		return nil, false, err
	}
	if err := s.publishJSONMutationLocked(sh, key, e, updatedRoot); err != nil {
		return nil, false, err
	}

	encoded, err := jsonvalue.Encode(popped)
	if err != nil {
		return nil, false, err
	}
	return encoded, true, nil
}

func (s *Store) JSONArrInsert(key, path string, index int, rawValues [][]byte) (int64, bool, error) {
	values := make([]any, 0, len(rawValues))
	for _, raw := range rawValues {
		value, err := jsonvalue.Parse(raw)
		if err != nil {
			return 0, false, err
		}
		values = append(values, value)
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, exists := sh.get(key)
	if !exists {
		return 0, false, nil
	}
	if sh.expired(key, e, now) {
		s.remove(sh, key)
		return 0, false, nil
	}
	if e.valueType != TypeJSON {
		return 0, false, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return 0, false, errors.New("WRONGTYPE value is not valid JSON")
	}
	current, found, err := jsonvalue.Get(root, path)
	if err != nil || !found {
		return 0, found, err
	}
	arr, ok := current.([]any)
	if !ok {
		return 0, false, nil
	}

	if index < 0 {
		index = len(arr) + index
		if index < 0 {
			index = 0
		}
	}
	if index > len(arr) {
		return 0, false, errors.New("ERR index out of bounds")
	}

	out := make([]any, 0, len(arr)+len(values))
	out = append(out, arr[:index]...)
	out = append(out, values...)
	out = append(out, arr[index:]...)

	updatedRoot, err := jsonvalue.Set(root, path, out)
	if err != nil {
		return 0, false, err
	}
	if err := s.publishJSONMutationLocked(sh, key, e, updatedRoot); err != nil {
		return 0, false, err
	}
	return int64(len(out)), true, nil
}

func (s *Store) JSONArrIndex(key, path string, rawScalar []byte, start, stop *int) (int64, bool, error) {
	target, err := jsonvalue.Parse(rawScalar)
	if err != nil {
		return 0, false, err
	}

	value, found, err := s.jsonValueAtPath(key, path)
	if err != nil || !found {
		return 0, found, err
	}
	arr, ok := value.([]any)
	if !ok {
		return 0, false, nil
	}

	from := 0
	to := len(arr) - 1
	if start != nil {
		from = *start
		if from < 0 {
			from = len(arr) + from
		}
	}
	if stop != nil {
		to = *stop
		if to < 0 {
			to = len(arr) + to
		}
	}
	if from < 0 {
		from = 0
	}
	if to >= len(arr) {
		to = len(arr) - 1
	}
	if from > to || from >= len(arr) {
		return -1, true, nil
	}

	for i := from; i <= to; i++ {
		if reflect.DeepEqual(arr[i], target) {
			return int64(i), true, nil
		}
	}
	return -1, true, nil
}

func (s *Store) JSONClear(key, path string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, exists := sh.get(key)
	if !exists {
		return 0, nil
	}
	if sh.expired(key, e, now) {
		s.remove(sh, key)
		return 0, nil
	}
	if e.valueType != TypeJSON {
		return 0, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return 0, errors.New("WRONGTYPE value is not valid JSON")
	}
	current, found, err := jsonvalue.Get(root, path)
	if err != nil || !found {
		return 0, err
	}

	var replacement any
	switch current.(type) {
	case []any:
		replacement = []any{}
	case map[string]any:
		replacement = map[string]any{}
	case float64:
		replacement = float64(0)
	default:
		return 0, nil
	}

	updatedRoot, err := jsonvalue.Set(root, path, replacement)
	if err != nil {
		return 0, err
	}
	if err := s.publishJSONMutationLocked(sh, key, e, updatedRoot); err != nil {
		return 0, err
	}
	return 1, nil
}


func (s *Store) JSONArrTrim(key, path string, start, stop int) (int64, bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, exists := sh.get(key)
	if !exists {
		return 0, false, nil
	}
	if sh.expired(key, e, now) {
		s.remove(sh, key)
		return 0, false, nil
	}
	if e.valueType != TypeJSON {
		return 0, false, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return 0, false, errors.New("WRONGTYPE value is not valid JSON")
	}
	current, found, err := jsonvalue.Get(root, path)
	if err != nil || !found {
		return 0, found, err
	}
	arr, ok := current.([]any)
	if !ok {
		return 0, false, nil
	}

	n := len(arr)
	if start < 0 {
		start = n + start
	}
	if stop < 0 {
		stop = n + stop
	}
	if start < 0 {
		start = 0
	}
	if stop >= n {
		stop = n - 1
	}

	var trimmed []any
	if n == 0 || start >= n || start > stop || stop < 0 {
		trimmed = []any{}
	} else {
		trimmed = append([]any(nil), arr[start:stop+1]...)
	}

	updatedRoot, err := jsonvalue.Set(root, path, trimmed)
	if err != nil {
		return 0, false, err
	}
	if err := s.publishJSONMutationLocked(sh, key, e, updatedRoot); err != nil {
		return 0, false, err
	}
	return int64(len(trimmed)), true, nil
}

func (s *Store) JSONMGet(keys []string, path string) ([][]byte, []bool) {
	values := make([][]byte, len(keys))
	found := make([]bool, len(keys))

	for i, key := range keys {
		value, ok, err := s.JSONGet(key, path)
		if err != nil || !ok {
			continue
		}
		values[i] = value
		found[i] = true
	}
	return values, found
}

func (s *Store) JSONMerge(key, path string, raw []byte) (bool, error) {
	patch, err := jsonvalue.Parse(raw)
	if err != nil {
		return false, err
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, exists := sh.get(key)
	if exists && sh.expired(key, e, now) {
		s.remove(sh, key)
		exists = false
	}

	if !exists {
		if path != "$" && path != "." {
			return false, errors.New("ERR new objects must be created at the root")
		}

		encoded, err := jsonvalue.Encode(mergeJSONPatch(nil, patch))
		if err != nil {
			return false, err
		}
		entry := s.makeEntry(encoded)
		entry.valueType = TypeJSON
		if err := s.publish(sh, key, entry); err != nil {
			return false, err
		}
		return true, nil
	}

	if e.valueType != TypeJSON {
		return false, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}

	root, err := jsonvalue.Parse(s.decode(sh, e))
	if err != nil {
		return false, errors.New("WRONGTYPE value is not valid JSON")
	}

	current, found, err := jsonvalue.Get(root, path)
	if err != nil {
		return false, err
	}

	var merged any
	if found {
		merged = mergeJSONPatch(current, patch)
	} else {
		merged = mergeJSONPatch(nil, patch)
	}

	updatedRoot, err := jsonvalue.Set(root, path, merged)
	if err != nil {
		return false, err
	}
	if err := s.publishJSONMutationLocked(sh, key, e, updatedRoot); err != nil {
		return false, err
	}
	return true, nil
}

func mergeJSONPatch(target, patch any) any {
	patchObject, ok := patch.(map[string]any)
	if !ok {
		return patch
	}

	targetObject, ok := target.(map[string]any)
	if !ok {
		targetObject = make(map[string]any)
	}

	for key, patchValue := range patchObject {
		if patchValue == nil {
			delete(targetObject, key)
			continue
		}
		targetObject[key] = mergeJSONPatch(targetObject[key], patchValue)
	}

	return targetObject
}
