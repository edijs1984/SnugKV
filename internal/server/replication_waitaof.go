package server

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

func isWaitAOFCommand(args [][]byte) bool {
	return len(args) > 0 && strings.EqualFold(string(args[0]), "WAITAOF")
}

func parseWaitAOFArguments(args [][]byte) (int, int, time.Duration, error) {
	if len(args) != 4 {
		return 0, 0, 0, errors.New("ERR wrong number of arguments for 'waitaof' command")
	}

	numLocal64, err := strconv.ParseInt(string(args[1]), 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("ERR value is not an integer or out of range")
	}
	if numLocal64 < 0 || numLocal64 > 1 {
		return 0, 0, 0, errors.New("ERR value is out of range, value must between 0 and 1")
	}

	numReplicas64, err := strconv.ParseInt(string(args[2]), 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("ERR value is not an integer or out of range")
	}
	if numReplicas64 < 0 {
		return 0, 0, 0, errors.New("ERR value is out of range, must be positive")
	}
	if strconv.IntSize == 32 && numReplicas64 > int64(^uint32(0)>>1) {
		return 0, 0, 0, errors.New("ERR value is out of range, must be positive")
	}

	timeoutMS, err := strconv.ParseInt(string(args[3]), 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("ERR timeout is not an integer or out of range")
	}
	if timeoutMS < 0 {
		return 0, 0, 0, errors.New("ERR timeout is negative")
	}
	if timeoutMS > int64((1<<63-1)/int64(time.Millisecond)) {
		return 0, 0, 0, errors.New("ERR timeout is out of range")
	}

	return int(numLocal64), int(numReplicas64), time.Duration(timeoutMS) * time.Millisecond, nil
}

func (s *Server) localAOFAcknowledged(targetSequence uint64) (int, <-chan struct{}) {
	journal, ok := s.journal.(durabilityJournal)
	if !ok {
		return 0, nil
	}
	_, synced, changed := journal.DurabilitySnapshot()
	if targetSequence == 0 || synced >= targetSequence {
		return 1, changed
	}
	return 0, changed
}

func waitAOFReply(local, replicas int) []byte {
	return array(integer(int64(local)), integer(int64(replicas)))
}

func (s *Server) executeWaitAOF(
	args [][]byte,
	targetOffset int64,
	targetSequence uint64,
	cancel <-chan struct{},
	allowBlock bool,
) ([]byte, error) {
	numLocal, numReplicas, timeout, err := parseWaitAOFArguments(args)
	if err != nil {
		return nil, err
	}
	if s.replication.isReadOnlyReplica() {
		return nil, errors.New("ERR WAITAOF cannot be used with replica instances. Please also note that writes to replicas are just local and are not propagated.")
	}
	if numLocal > 0 {
		if _, ok := s.journal.(durabilityJournal); !ok {
			return nil, errors.New("ERR WAITAOF cannot be used when numlocal is set but appendonly is disabled.")
		}
	}

	timer, timeoutC := blockingTimer(timeout)
	if timer != nil {
		defer timer.Stop()
	}

	requestedACK := false
	for {
		local, localChanged := s.localAOFAcknowledged(targetSequence)
		replicas, replicaChanged := s.replication.waitAOFSnapshot(targetOffset)
		if (local >= numLocal && replicas >= numReplicas) || !allowBlock {
			return waitAOFReply(local, replicas), nil
		}
		if numReplicas > replicas && !requestedACK {
			requestedACK = true
			s.replication.requestReplicaACKs()
		}

		select {
		case <-localChanged:
		case <-replicaChanged:
		case <-timeoutC:
			local, _ = s.localAOFAcknowledged(targetSequence)
			replicas, _ = s.replication.waitAOFSnapshot(targetOffset)
			return waitAOFReply(local, replicas), nil
		case <-cancel:
			return nil, errBlockingClientGone
		}
	}
}
