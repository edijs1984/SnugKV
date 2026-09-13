package engine

import (
	"errors"
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

	if exists && e.expired(now) {
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
	updated.expiresAt = e.expiresAt

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

	if e.expired(now) {
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

	if e.expired(now) {
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

	if e.expired(now) {
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
	updated.expiresAt = e.expiresAt

	if err := s.publish(sh, key, updated); err != nil {
		return 0, err
	}

	return 1, nil
}
