package server

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

const clusterTopologyPersistenceVersion = 1

type clusterTopologyPersistenceState struct {
	Version   int                       `json:"version"`
	Enabled   bool                      `json:"enabled"`
	NodeAddr  string                    `json:"node_addr"`
	Epoch     uint64                    `json:"epoch"`
	Owners    [clusterSlotCount]string  `json:"owners"`
	Migrating [clusterSlotCount]string  `json:"migrating"`
	Importing [clusterSlotCount]string  `json:"importing"`
}

var clusterTopologyPersistencePaths sync.Map // map[*Server]string

func clusterTopologyPersistencePath(aofPath, snapshotPath string) string {
	if aofPath != "" {
		return aofPath + ".cluster"
	}
	if snapshotPath != "" {
		return snapshotPath + ".cluster"
	}
	return ""
}

func clusterTopologyStateFromSnapshot(state clusterStateSnapshot) clusterTopologyPersistenceState {
	return clusterTopologyPersistenceState{
		Version:   clusterTopologyPersistenceVersion,
		Enabled:   state.enabled,
		NodeAddr:  state.nodeAddr,
		Epoch:     state.epoch,
		Owners:    state.owners,
		Migrating: state.migrating,
		Importing: state.importing,
	}
}

func (s *Server) persistClusterTopologySnapshot(state clusterStateSnapshot) error {
	value, ok := clusterTopologyPersistencePaths.Load(s)
	if !ok {
		return nil
	}
	payload, err := json.Marshal(clusterTopologyStateFromSnapshot(state))
	if err != nil {
		return err
	}
	return writeSidecarStateAtomic(value.(string), payload)
}

func (s *TCPServer) ConfigureClusterPersistence(aofPath, snapshotPath string) error {
	path := clusterTopologyPersistencePath(aofPath, snapshotPath)
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		clusterTopologyPersistencePaths.Store(s.server, path)
		return nil
	}
	if err != nil {
		return fmt.Errorf("cluster topology recovery: %w", err)
	}

	var state clusterTopologyPersistenceState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("cluster topology recovery: %w", err)
	}
	if state.Version != clusterTopologyPersistenceVersion {
		return fmt.Errorf("cluster topology recovery: unsupported state version %d", state.Version)
	}
	if state.Enabled && state.NodeAddr == "" {
		return fmt.Errorf("cluster topology recovery: enabled topology has empty node address")
	}

	s.server.clusterMu.Lock()
	s.server.clusterEnabled = state.Enabled
	s.server.clusterNodeAddr = state.NodeAddr
	s.server.clusterTopologyEpoch = state.Epoch
	s.server.clusterSlotOwners = state.Owners
	s.server.clusterSlotMigrating = state.Migrating
	s.server.clusterSlotImporting = state.Importing
	s.server.clusterMu.Unlock()

	clusterTopologyPersistencePaths.Store(s.server, path)
	return nil
}

func (s *Server) mutateClusterTopologyLocked(mutator func() error) error {
	before := clusterStateSnapshot{
		enabled:   s.clusterEnabled,
		nodeAddr:  s.clusterNodeAddr,
		epoch:     s.clusterTopologyEpoch,
		owners:    s.clusterSlotOwners,
		migrating: s.clusterSlotMigrating,
		importing: s.clusterSlotImporting,
	}

	if err := mutator(); err != nil {
		return err
	}
	if s.clusterEnabled == before.enabled &&
		s.clusterNodeAddr == before.nodeAddr &&
		s.clusterSlotOwners == before.owners &&
		s.clusterSlotMigrating == before.migrating &&
		s.clusterSlotImporting == before.importing {
		return nil
	}

	s.clusterTopologyEpoch++
	after := clusterStateSnapshot{
		enabled:   s.clusterEnabled,
		nodeAddr:  s.clusterNodeAddr,
		epoch:     s.clusterTopologyEpoch,
		owners:    s.clusterSlotOwners,
		migrating: s.clusterSlotMigrating,
		importing: s.clusterSlotImporting,
	}
	if err := s.persistClusterTopologySnapshot(after); err != nil {
		s.clusterEnabled = before.enabled
		s.clusterNodeAddr = before.nodeAddr
		s.clusterTopologyEpoch = before.epoch
		s.clusterSlotOwners = before.owners
		s.clusterSlotMigrating = before.migrating
		s.clusterSlotImporting = before.importing
		return fmt.Errorf("ERR cluster topology persistence failed: %w", err)
	}
	return nil
}
