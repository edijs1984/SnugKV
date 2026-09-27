package server

import (
	"errors"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"snugkv/internal/persistence"
	"sync"
)

const replicationPersistenceVersion = 1
const failoverPersistenceVersion = 1

type failoverPersistenceState struct {
	Version  int    `json:"version"`
	Term     uint64 `json:"term"`
	VotedFor string `json:"voted_for,omitempty"`
}

const failoverMembershipPersistenceVersion = 1

type failoverMembershipPersistenceState struct {
	Version       int      `json:"version"`
	GroupID       string   `json:"group_id"`
	ConfigEpoch   uint64   `json:"config_epoch"`
	Peers         []string `json:"peers"`
	Quorum        int      `json:"quorum"`
	JointActive   bool     `json:"joint_active"`
	PendingEpoch  uint64   `json:"pending_epoch,omitempty"`
	PendingPeers  []string `json:"pending_peers,omitempty"`
	PendingQuorum int      `json:"pending_quorum,omitempty"`
	CommitPending bool     `json:"commit_pending,omitempty"`
	CommitOldEpoch uint64  `json:"commit_old_epoch,omitempty"`
	CommitEpoch uint64     `json:"commit_epoch,omitempty"`
	CommitMembers []string `json:"commit_members,omitempty"`
	CommitQuorum int       `json:"commit_quorum,omitempty"`
	CommitTargets []string `json:"commit_targets,omitempty"`
	Retired bool `json:"retired,omitempty"`
	RetiredAtEpoch uint64 `json:"retired_at_epoch,omitempty"`
	RetirePending bool `json:"retire_pending,omitempty"`
	RetirePendingEpoch uint64 `json:"retire_pending_epoch,omitempty"`
	CommitRetireTargets []string `json:"commit_retire_targets,omitempty"`
}

type replicationPersistenceState struct {
	Version     int    `json:"version"`
	MasterHost  string `json:"master_host"`
	MasterPort  int    `json:"master_port"`
	MasterRunID string `json:"master_run_id"`
	Offset      int64  `json:"offset"`
	RedisStream bool   `json:"redis_stream"`
}

var replicationPersistencePaths sync.Map // map[*Server]string



func failoverMembershipPersistencePath(replicationPath string) string {
	if replicationPath == "" {
		return ""
	}
	return replicationPath + ".membership"
}

func (s *Server) persistFailoverMembershipState() error {
	value, ok := replicationPersistencePaths.Load(s)
	if !ok {
		return errors.New("failover membership persistence is unavailable")
	}
	path := failoverMembershipPersistencePath(value.(string))

	s.failoverMembershipMu.RLock()
	state := failoverMembershipPersistenceState{
		Version:       failoverMembershipPersistenceVersion,
		GroupID:       s.failoverGroupID,
		ConfigEpoch:   s.failoverConfigEpoch,
		Peers:         append([]string(nil), s.failoverPeers...),
		Quorum:        s.failoverQuorum,
		JointActive:   s.failoverJointActive,
		PendingEpoch:  s.failoverPendingEpoch,
		PendingPeers:  append([]string(nil), s.failoverPendingPeers...),
		PendingQuorum: s.failoverPendingQuorum,
		CommitPending: s.failoverCommitPending,
		CommitOldEpoch: s.failoverCommitOldEpoch,
		CommitEpoch: s.failoverCommitEpoch,
		CommitMembers: append([]string(nil), s.failoverCommitMembers...),
		CommitQuorum: s.failoverCommitQuorum,
		CommitTargets: append([]string(nil), s.failoverCommitTargets...),
		Retired: s.failoverRetired,
		RetiredAtEpoch: s.failoverRetiredAtEpoch,
		RetirePending: s.failoverRetirePending,
		RetirePendingEpoch: s.failoverRetirePendingEpoch,
		CommitRetireTargets: append([]string(nil), s.failoverCommitRetireTargets...),
	}
	s.failoverMembershipMu.RUnlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeSidecarStateAtomic(path, payload)
}

func (s *Server) loadFailoverMembershipState(replicationPath string) error {
	path := failoverMembershipPersistencePath(replicationPath)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state failoverMembershipPersistenceState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Version != failoverMembershipPersistenceVersion {
		return fmt.Errorf("unsupported failover membership state version %d", state.Version)
	}
	if state.JointActive && (state.PendingEpoch <= state.ConfigEpoch || len(state.PendingPeers) == 0 || state.PendingQuorum <= 0) {
		return errors.New("invalid persisted failover membership transition")
	}
	if state.CommitPending && (state.CommitEpoch == 0 || state.CommitEpoch <= state.CommitOldEpoch || len(state.CommitMembers) == 0 || state.CommitQuorum <= 0) {
		return errors.New("invalid persisted failover membership commit recovery")
	}
	if state.RetirePending && state.RetirePendingEpoch <= state.ConfigEpoch {
		return errors.New("invalid persisted failover retirement prepare")
	}

	s.failoverMembershipMu.Lock()
	s.failoverGroupID = state.GroupID
	s.failoverConfigEpoch = state.ConfigEpoch
	s.failoverPeers = append([]string(nil), state.Peers...)
	s.failoverQuorum = state.Quorum
	s.failoverJointActive = state.JointActive
	s.failoverPendingEpoch = state.PendingEpoch
	s.failoverPendingPeers = append([]string(nil), state.PendingPeers...)
	s.failoverPendingQuorum = state.PendingQuorum
	s.failoverCommitPending = state.CommitPending
	s.failoverCommitOldEpoch = state.CommitOldEpoch
	s.failoverCommitEpoch = state.CommitEpoch
	s.failoverCommitMembers = append([]string(nil), state.CommitMembers...)
	s.failoverCommitQuorum = state.CommitQuorum
	s.failoverCommitTargets = append([]string(nil), state.CommitTargets...)
	s.failoverRetired = state.Retired
	s.failoverRetiredAtEpoch = state.RetiredAtEpoch
	s.failoverRetirePending = state.RetirePending
	s.failoverRetirePendingEpoch = state.RetirePendingEpoch
	s.failoverCommitRetireTargets = append([]string(nil), state.CommitRetireTargets...)
	s.failoverMembershipMu.Unlock()
	return nil
}

func failoverPersistencePath(replicationPath string) string {
	if replicationPath == "" {
		return ""
	}
	return replicationPath + ".failover"
}

func (s *Server) persistFailoverVoteState(term uint64, votedFor string) error {
	value, ok := replicationPersistencePaths.Load(s)
	if !ok {
		return nil
	}
	path := failoverPersistencePath(value.(string))
	state := failoverPersistenceState{
		Version:  failoverPersistenceVersion,
		Term:     term,
		VotedFor: votedFor,
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeSidecarStateAtomic(path, payload)
}

func (s *Server) loadFailoverVoteState(replicationPath string) error {
	path := failoverPersistencePath(replicationPath)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state failoverPersistenceState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Version != failoverPersistenceVersion {
		return fmt.Errorf("unsupported failover state version %d", state.Version)
	}
	s.failoverVoteMu.Lock()
	s.failoverTerm = state.Term
	s.failoverVotedFor = state.VotedFor
	s.failoverVoteMu.Unlock()
	return nil
}

func replicationPersistencePath(aofPath, snapshotPath string) string {
	if aofPath != "" {
		return aofPath + ".replication"
	}
	if snapshotPath != "" {
		return snapshotPath + ".replication"
	}
	return ""
}

// ConfigureReplicationPersistence restores a previously checkpointed upstream
// PSYNC continuation tuple and resumes following that upstream. The checkpoint
// is written only after the keyspace has been made durable during graceful
// shutdown, so its offset never intentionally advances beyond recoverable data.
func (s *TCPServer) ConfigureReplicationPersistence(aofPath, snapshotPath string) error {
	return s.ConfigureReplicationPersistenceRecovered(aofPath, snapshotPath, nil)
}

// ConfigureReplicationPersistenceRecovered prefers a crash-safe checkpoint
// recovered from the logical persistence stream. The graceful-shutdown sidecar
// remains the fallback for compacted AOF/snapshot shutdown checkpoints.
func (s *TCPServer) ConfigureReplicationPersistenceRecovered(
	aofPath, snapshotPath string,
	recovered *persistence.ReplicationCheckpoint,
) error {
	path := replicationPersistencePath(aofPath, snapshotPath)
	if path == "" {
		return nil
	}
	replicationPersistencePaths.Store(s.server, path)
	if err := s.server.loadFailoverVoteState(path); err != nil {
		return fmt.Errorf("failover recovery: %w", err)
	}
	if err := s.server.loadFailoverMembershipState(path); err != nil {
		return fmt.Errorf("failover membership recovery: %w", err)
	}

	var state replicationPersistenceState
	if recovered != nil && recovered.Clear {
		if err := s.server.clearReplicationPersistence(); err != nil {
			return fmt.Errorf("replication recovery: %w", err)
		}
		if recovered.MasterHost != "" {
			if recovered.MasterPort <= 0 || recovered.MasterPort > 65535 {
				return fmt.Errorf("replication recovery: invalid upstream")
			}
			s.server.startReplicaFollow(recovered.MasterHost, recovered.MasterPort)
		}
		return nil
	}
	if recovered != nil {
		state = replicationPersistenceState{
			Version:     replicationPersistenceVersion,
			MasterHost:  recovered.MasterHost,
			MasterPort:  recovered.MasterPort,
			MasterRunID: recovered.MasterRunID,
			Offset:      recovered.Offset,
			RedisStream: recovered.RedisStream,
		}
	} else {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("replication recovery: %w", err)
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("replication recovery: %w", err)
		}
	}

	if state.Version != replicationPersistenceVersion {
		return fmt.Errorf("replication recovery: unsupported state version %d", state.Version)
	}
	if state.MasterHost == "" || state.MasterPort <= 0 || state.MasterPort > 65535 {
		return fmt.Errorf("replication recovery: invalid upstream")
	}
	if state.Offset < 0 || len(state.MasterRunID) != 40 {
		return fmt.Errorf("replication recovery: invalid PSYNC state")
	}
	if _, err := hex.DecodeString(state.MasterRunID); err != nil {
		return fmt.Errorf("replication recovery: invalid master run ID")
	}

	s.server.replication.mu.Lock()
	s.server.replication.masterHost = state.MasterHost
	s.server.replication.masterPort = state.MasterPort
	s.server.replication.masterRunID = state.MasterRunID
	s.server.replication.offset = state.Offset
	s.server.replication.masterRedisStream = state.RedisStream
	s.server.replication.mu.Unlock()

	s.server.startReplicaFollow(state.MasterHost, state.MasterPort)
	return nil
}

func (s *Server) replicationCheckpointForOffset(offset int64, redisStream bool) *persistence.ReplicationCheckpoint {
	s.replication.mu.RLock()
	defer s.replication.mu.RUnlock()
	if s.replication.role != replicationReplica ||
		s.replication.masterHost == "" ||
		s.replication.masterRunID == "" {
		return nil
	}
	return &persistence.ReplicationCheckpoint{
		MasterHost:  s.replication.masterHost,
		MasterPort:  s.replication.masterPort,
		MasterRunID: s.replication.masterRunID,
		Offset:      offset,
		RedisStream: redisStream,
	}
}

func (s *Server) persistReplicationCheckpointClearLocked(host string, port int) error {
	if s.journal == nil {
		return nil
	}
	return s.journal.Append([]persistence.Record{{
		Replication: &persistence.ReplicationCheckpoint{
			Clear:      true,
			MasterHost: host,
			MasterPort: port,
		},
	}})
}

// HasReplicationContinuationState reports whether a graceful shutdown should
// make the current replica dataset durable before checkpointing PSYNC state.
func (s *TCPServer) HasReplicationContinuationState() bool {
	s.server.replication.mu.RLock()
	defer s.server.replication.mu.RUnlock()
	return s.server.replication.role == replicationReplica &&
		s.server.replication.masterHost != "" &&
		s.server.replication.masterRunID != ""
}

// CheckpointReplicationPersistence atomically saves the continuation tuple.
// Call this only after the current keyspace has been durably checkpointed.
func (s *TCPServer) CheckpointReplicationPersistence() error {
	value, ok := replicationPersistencePaths.Load(s.server)
	if !ok {
		return nil
	}
	path := value.(string)

	s.server.replication.mu.RLock()
	if s.server.replication.role != replicationReplica ||
		s.server.replication.masterHost == "" ||
		s.server.replication.masterRunID == "" {
		s.server.replication.mu.RUnlock()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	state := replicationPersistenceState{
		Version:     replicationPersistenceVersion,
		MasterHost:  s.server.replication.masterHost,
		MasterPort:  s.server.replication.masterPort,
		MasterRunID: s.server.replication.masterRunID,
		Offset:      s.server.replication.offset,
		RedisStream: s.server.replication.masterRedisStream,
	}
	s.server.replication.mu.RUnlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeSidecarStateAtomic(path, payload)
}

func (s *Server) clearReplicationPersistence() error {
	value, ok := replicationPersistencePaths.Load(s)
	if !ok {
		return nil
	}
	path := value.(string)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
