package server

import (
	"errors"
	"strings"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

type replicationRecordingJournal struct {
	frames [][]persistence.Record
	fail   bool
}

func (j *replicationRecordingJournal) Append(records []persistence.Record) error {
	if j.fail {
		return errors.New("write failed")
	}
	frame := make([]persistence.Record, len(records))
	for i, record := range records {
		frame[i] = record
		frame[i].Key = append([]byte(nil), record.Key...)
		frame[i].Value = append([]byte(nil), record.Value...)
		if record.Replication != nil {
			checkpoint := *record.Replication
			frame[i].Replication = &checkpoint
		}
	}
	j.frames = append(j.frames, frame)
	return nil
}

func setReplicaCheckpointContext(s *Server, runID string, offset int64) {
	s.replication.mu.Lock()
	s.replication.role = replicationReplica
	s.replication.masterHost = "127.0.0.1"
	s.replication.masterPort = 6398
	s.replication.masterRunID = runID
	s.replication.offset = offset
	s.replication.masterRedisStream = true
	s.replication.mu.Unlock()
}

func TestRedisReplicationBatchPersistsMutationAndCheckpointTogether(t *testing.T) {
	s := New(engine.New())
	journal := &replicationRecordingJournal{}
	s.SetJournal(journal)
	runID := strings.Repeat("a", 40)
	setReplicaCheckpointContext(s, runID, 100)

	if err := s.applyRedisReplicationBatch([][][]byte{
		{[]byte("SET"), []byte("crash:key"), []byte("value")},
	}, 137); err != nil {
		t.Fatal(err)
	}
	if len(journal.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(journal.frames))
	}
	frame := journal.frames[0]
	if len(frame) != 2 {
		t.Fatalf("records = %d, want data + checkpoint", len(frame))
	}
	checkpoint := frame[len(frame)-1].Replication
	if checkpoint == nil ||
		checkpoint.MasterRunID != runID ||
		checkpoint.Offset != 137 ||
		!checkpoint.RedisStream {
		t.Fatalf("unexpected checkpoint: %+v", checkpoint)
	}

	recovered := engine.New()
	var recoveredCheckpoint *persistence.ReplicationCheckpoint
	recoveredCheckpoint = persistence.RecoverReplicationCheckpoint(recoveredCheckpoint, frame)
	if err := recovered.Restore(frame, false); err != nil {
		t.Fatal(err)
	}
	value, found, wrongType := recovered.GetString("crash:key")
	if wrongType || !found || string(value) != "value" {
		t.Fatalf("recovered value=%q found=%v wrongType=%v", value, found, wrongType)
	}
	if recoveredCheckpoint == nil || recoveredCheckpoint.Offset != 137 {
		t.Fatalf("recovered checkpoint: %+v", recoveredCheckpoint)
	}
}

func TestRedisReplicationBatchPersistenceFailureRollsBack(t *testing.T) {
	s := New(engine.New())
	journal := &replicationRecordingJournal{fail: true}
	s.SetJournal(journal)
	setReplicaCheckpointContext(s, strings.Repeat("b", 40), 200)

	err := s.applyRedisReplicationBatch([][][]byte{
		{[]byte("SET"), []byte("rollback:key"), []byte("value")},
	}, 240)
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	_, found, _ := s.store.GetString("rollback:key")
	if found {
		t.Fatal("replicated mutation survived failed persistence append")
	}
}

func TestReplicationCheckpointClearTombstoneWinsOnReplay(t *testing.T) {
	runID := strings.Repeat("c", 40)
	var recovered *persistence.ReplicationCheckpoint
	recovered = persistence.RecoverReplicationCheckpoint(recovered, []persistence.Record{{
		Replication: &persistence.ReplicationCheckpoint{
			MasterHost: "127.0.0.1", MasterPort: 6398,
			MasterRunID: runID, Offset: 55, RedisStream: true,
		},
	}})
	if recovered == nil || recovered.Offset != 55 {
		t.Fatalf("initial checkpoint: %+v", recovered)
	}

	recovered = persistence.RecoverReplicationCheckpoint(recovered, []persistence.Record{{
		Replication: &persistence.ReplicationCheckpoint{Clear: true},
	}})
	if recovered == nil || !recovered.Clear {
		t.Fatalf("clear tombstone lost: %+v", recovered)
	}

	recovered = persistence.RecoverReplicationCheckpoint(recovered, []persistence.Record{{
		Replication: &persistence.ReplicationCheckpoint{
			MasterHost: "127.0.0.1", MasterPort: 6398,
			MasterRunID: runID, Offset: 88, RedisStream: true,
		},
	}})
	if recovered == nil || recovered.Clear || recovered.Offset != 88 {
		t.Fatalf("new checkpoint did not supersede clear: %+v", recovered)
	}
}

func TestPersistRedisFullSyncWritesClearBeforeCheckpoint(t *testing.T) {
	s := New(engine.New())
	journal := &replicationRecordingJournal{}
	s.SetJournal(journal)
	setReplicaCheckpointContext(s, strings.Repeat("d", 40), 300)
	if err := s.store.SetPlain("full:key", []byte("full-value")); err != nil {
		t.Fatal(err)
	}

	s.durableMu.Lock()
	err := s.persistRedisFullSyncLocked(333)
	s.durableMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.frames) < 3 {
		t.Fatalf("frames = %d, want clear/reset, data, checkpoint", len(journal.frames))
	}
	first := journal.frames[0]
	if len(first) != 2 ||
		first[0].Replication == nil ||
		!first[0].Replication.Clear ||
		!first[1].Reset {
		t.Fatalf("unexpected first full-sync frame: %+v", first)
	}
	last := journal.frames[len(journal.frames)-1]
	if len(last) != 1 ||
		last[0].Replication == nil ||
		last[0].Replication.Offset != 333 {
		t.Fatalf("unexpected final checkpoint: %+v", last)
	}
}
