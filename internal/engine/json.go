package engine

import (
	"errors"
	"math"
	"sort"
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
