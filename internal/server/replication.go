package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type replicationRole uint8

const (
	replicationMaster replicationRole = iota
	replicationReplica
)

type replicationState struct {
	mu sync.RWMutex

	role replicationRole

	masterHost string
	masterPort int
	masterLinkStatus string
	masterSyncInProgress bool

	connectedReplicas int
	runID string
	offset int64
}

type replicationSnapshot struct {
	role replicationRole
	masterHost string
	masterPort int
	masterLinkStatus string
	masterSyncInProgress bool
	connectedReplicas int
	runID string
	offset int64
}

func (r *replicationState) snapshot() replicationSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return replicationSnapshot{
		role: r.role,
		masterHost: r.masterHost,
		masterPort: r.masterPort,
		masterLinkStatus: r.masterLinkStatus,
		masterSyncInProgress: r.masterSyncInProgress,
		connectedReplicas: r.connectedReplicas,
		runID: r.runID,
		offset: r.offset,
	}
}

func (r *replicationState) setReplica(host string, port int) {
	r.mu.Lock()
	r.role = replicationReplica
	r.masterHost = host
	r.masterPort = port
	r.masterLinkStatus = "down"
	r.masterSyncInProgress = true
	r.mu.Unlock()
}

func (r *replicationState) setReplicaConnected() {
	r.mu.Lock()
	if r.role == replicationReplica {
		r.masterLinkStatus = "up"
		r.masterSyncInProgress = false
	}
	r.mu.Unlock()
}

func (r *replicationState) setReplicaDisconnected() {
	r.mu.Lock()
	if r.role == replicationReplica {
		r.masterLinkStatus = "down"
		r.masterSyncInProgress = false
	}
	r.mu.Unlock()
}

func (r *replicationState) promote() {
	r.mu.Lock()
	r.role = replicationMaster
	r.masterHost = ""
	r.masterPort = 0
	r.masterLinkStatus = ""
	r.masterSyncInProgress = false
	r.mu.Unlock()
}

func (r *replicationState) setConnectedReplicas(n int) {
	r.mu.Lock()
	r.connectedReplicas = n
	r.mu.Unlock()
}

func (r *replicationState) isReadOnlyReplica() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.role == replicationReplica
}

func (s *Server) replicationInfo() string {
	state := s.replication.snapshot()
	if state.role == replicationReplica {
		return fmt.Sprintf(
			"# Replication\r\n"+
				"role:slave\r\n"+
				"master_host:%s\r\n"+
				"master_port:%d\r\n"+
				"master_link_status:%s\r\n"+
				"master_sync_in_progress:%d\r\n"+
				"slave_read_only:1\r\n",
			state.masterHost,
			state.masterPort,
			state.masterLinkStatus,
			boolInt(state.masterSyncInProgress),
		)
	}
	return fmt.Sprintf(
		"# Replication\r\n"+
			"role:master\r\n"+
			"connected_slaves:%d\r\n"+
			"master_replid:%s\r\n"+
			"master_repl_offset:%d\r\n",
		state.connectedReplicas,
		state.runID,
		state.offset,
	)
}

func boolInt(v bool) int {
	if v { return 1 }
	return 0
}

func (s *Server) replicationRoleReply() []byte {
	state := s.replication.snapshot()
	if state.role == replicationReplica {
		status := "connect"
		if state.masterLinkStatus == "up" {
			status = "connected"
		}
		return array(
			formatBulkString([]byte("slave")),
			formatBulkString([]byte(state.masterHost)),
			integer(int64(state.masterPort)),
			formatBulkString([]byte(status)),
			integer(state.offset),
		)
	}

	replicas := make([][]byte, 0)
	return array(
		formatBulkString([]byte("master")),
		integer(state.offset),
		array(replicas...),
	)
}

func parseReplicaOf(args [][]byte) (detach bool, host string, port int, err error) {
	if len(args) != 3 {
		return false, "", 0, errors.New("ERR wrong number of arguments for 'replicaof' command")
	}
	if strings.EqualFold(string(args[1]), "NO") && strings.EqualFold(string(args[2]), "ONE") {
		return true, "", 0, nil
	}
	p, parseErr := strconv.Atoi(string(args[2]))
	if parseErr != nil || p <= 0 || p > 65535 {
		return false, "", 0, errors.New("ERR Invalid master port")
	}
	if len(args[1]) == 0 {
		return false, "", 0, errors.New("ERR Invalid master address")
	}
	return false, string(args[1]), p, nil
}
