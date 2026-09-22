package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"snugkv/internal/engine"
)

const searchPersistenceVersion = 1

type searchPersistenceState struct {
	Version int                       `json:"version"`
	Indexes []engine.SearchDefinition `json:"indexes"`
}

var searchPersistencePaths sync.Map // map[*Server]string

func searchPersistencePath(aofPath, snapshotPath string) string {
	if aofPath != "" {
		return aofPath + ".search"
	}
	if snapshotPath != "" {
		return snapshotPath + ".search"
	}
	return ""
}

// ConfigureSearchPersistence restores search definitions from a durable
// sidecar after primary data recovery. Postings remain derived: restoring a
// definition rebuilds its index from the recovered JSON keyspace.
func (s *TCPServer) ConfigureSearchPersistence(aofPath, snapshotPath string) error {
	path := searchPersistencePath(aofPath, snapshotPath)
	if path == "" {
		return nil
	}

	if data, err := os.ReadFile(path); err == nil {
		var state searchPersistenceState
		if decodeErr := json.Unmarshal(data, &state); decodeErr != nil {
			return fmt.Errorf("search recovery: %w", decodeErr)
		}
		if state.Version != searchPersistenceVersion {
			return fmt.Errorf("search recovery: unsupported state version %d", state.Version)
		}
		if restoreErr := s.server.store.RestoreSearchDefinitions(state.Indexes); restoreErr != nil {
			return fmt.Errorf("search recovery: %w", restoreErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("search recovery: %w", err)
	}

	searchPersistencePaths.Store(s.server, path)
	return nil
}

func (s *Server) persistSearchDefinitions() error {
	value, ok := searchPersistencePaths.Load(s)
	if !ok {
		return nil
	}

	payload, err := json.Marshal(searchPersistenceState{
		Version: searchPersistenceVersion,
		Indexes: s.store.SearchDefinitions(),
	})
	if err != nil {
		return err
	}
	return writeSidecarStateAtomic(value.(string), payload)
}

func (s *Server) executeSearchDefinitionMutation(
	args [][]byte,
	apply func() ([]byte, error),
) ([]byte, error) {
	before := s.store.SearchDefinitions()

	response, err := apply()
	if err != nil {
		return response, err
	}

	if err = s.persistSearchDefinitions(); err == nil {
		return response, nil
	}

	if rollbackErr := s.store.RestoreSearchDefinitions(before); rollbackErr != nil {
		return nil, errors.New("ERR search persistence and rollback failed")
	}
	return nil, errors.New("ERR search persistence write failed")
}
