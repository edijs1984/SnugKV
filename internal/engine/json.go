package engine

import (
	"errors"
	"snugkv/internal/jsonvalue"
)

func (s *Store) JSONSet(key, path string, raw []byte) error {
	newValue, err := jsonvalue.Parse(raw)
	if err != nil {
		return err
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	e, exists := sh.data.Get(key)

	if exists && e.expired(now) {
		s.remove(sh, key)
		exists = false
	}

	// New keys can only be created at the JSON root.
	if !exists {
		if path != "$" {
			return errors.New("ERR new objects must be created at the root")
		}

		encoded, err := jsonvalue.Encode(newValue)
		if err != nil {
			return err
		}

		return s.publish(sh, key, s.makeEntry(encoded))
	}

	currentRaw := s.decode(e)

	root, err := jsonvalue.Parse(currentRaw)
	if err != nil {
		return errors.New("WRONGTYPE value is not valid JSON")
	}

	updatedRoot, err := jsonvalue.Set(root, path, newValue)
	if err != nil {
		return err
	}

	encoded, err := jsonvalue.Encode(updatedRoot)
	if err != nil {
		return err
	}

	updated := s.makeEntry(encoded)

	// JSON.SET must not silently destroy an existing TTL.
	updated.expiresAt = e.expiresAt

	return s.publish(sh, key, updated)
}

func (s *Store) JSONGet(key, path string) ([]byte, bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()

	e, exists := sh.data.Get(key)

	if !exists {
		return nil, false, nil
	}

	if e.expired(now) {
		s.remove(sh, key)
		return nil, false, nil
	}

	root, err := jsonvalue.Parse(s.decode(e))
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