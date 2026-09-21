package engine

import (
	"errors"
	"snugkv/internal/jsonvalue"
	"sort"
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

	values, err := jsonvalue.GetAll(root, path)
	if err != nil {
		return nil, false, err
	}

	if jsonvalue.IsJSONPath(path) {
		encoded, err := jsonvalue.Encode(values)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	}

	if len(values) == 0 {
		return nil, false, nil
	}

	encoded, err := jsonvalue.Encode(values[0])
	if err != nil {
		return nil, false, err
	}

	return encoded, true, nil
}

func (s *Store) JSONTypes(key, path string) ([]string, bool, error) {
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

	values, err := jsonvalue.GetAll(root, path)
	if err != nil {
		return nil, false, err
	}

	types := make([]string, len(values))
	for i := range values {
		types[i] = jsonvalue.TypeOf(values[i])
	}

	return types, true, nil
}

func (s *Store) JSONType(key, path string) (string, bool, error) {
	types, exists, err := s.JSONTypes(key, path)
	if err != nil || !exists || len(types) == 0 {
		return "", exists && len(types) > 0, err
	}
	return types[0], true, nil
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


type JSONLengthResult struct {
	Value int64
	Valid bool
}

type JSONKeysResult struct {
	Values []string
	Valid  bool
}

func (s *Store) jsonValues(key, path string) ([]any, bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, exists := sh.get(key)
	if !exists {
		return nil, false, nil
	}
	if sh.expired(key, e, s.now()) {
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
	values, err := jsonvalue.GetAll(root, path)
	if err != nil {
		return nil, false, err
	}
	return values, true, nil
}

func (s *Store) JSONStrLen(key, path string) ([]JSONLengthResult, bool, error) {
	values, exists, err := s.jsonValues(key, path)
	if err != nil || !exists {
		return nil, exists, err
	}

	out := make([]JSONLengthResult, len(values))
	for i, value := range values {
		if text, ok := value.(string); ok {
			out[i] = JSONLengthResult{Value: int64(len([]rune(text))), Valid: true}
		}
	}
	return out, true, nil
}

func (s *Store) JSONObjLen(key, path string) ([]JSONLengthResult, bool, error) {
	values, exists, err := s.jsonValues(key, path)
	if err != nil || !exists {
		return nil, exists, err
	}

	out := make([]JSONLengthResult, len(values))
	for i, value := range values {
		if object, ok := value.(map[string]any); ok {
			out[i] = JSONLengthResult{Value: int64(len(object)), Valid: true}
		}
	}
	return out, true, nil
}

func (s *Store) JSONArrLen(key, path string) ([]JSONLengthResult, bool, error) {
	values, exists, err := s.jsonValues(key, path)
	if err != nil || !exists {
		return nil, exists, err
	}

	out := make([]JSONLengthResult, len(values))
	for i, value := range values {
		if array, ok := value.([]any); ok {
			out[i] = JSONLengthResult{Value: int64(len(array)), Valid: true}
		}
	}
	return out, true, nil
}

func (s *Store) JSONObjKeys(key, path string) ([]JSONKeysResult, bool, error) {
	values, exists, err := s.jsonValues(key, path)
	if err != nil || !exists {
		return nil, exists, err
	}

	out := make([]JSONKeysResult, len(values))
	for i, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out[i] = JSONKeysResult{Values: keys, Valid: true}
	}
	return out, true, nil
}
