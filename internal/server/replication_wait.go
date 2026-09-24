package server

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

func isReplicationWaitCommand(args [][]byte) bool {
	return len(args) > 0 && strings.EqualFold(string(args[0]), "WAIT")
}

func parseWaitArguments(args [][]byte) (int, time.Duration, error) {
	if len(args) != 3 {
		return 0, 0, errors.New("ERR wrong number of arguments for 'wait' command")
	}

	replicas64, err := strconv.ParseInt(string(args[1]), 10, 64)
	if err != nil {
		return 0, 0, errors.New("ERR value is not an integer or out of range")
	}
	if strconv.IntSize == 32 && (replicas64 > int64(^uint32(0)>>1) || replicas64 < -int64(^uint32(0)>>1)-1) {
		return 0, 0, errors.New("ERR value is not an integer or out of range")
	}

	timeoutMS, err := strconv.ParseInt(string(args[2]), 10, 64)
	if err != nil {
		return 0, 0, errors.New("ERR timeout is not an integer or out of range")
	}
	if timeoutMS < 0 {
		return 0, 0, errors.New("ERR timeout is negative")
	}
	if timeoutMS > int64((1<<63-1)/int64(time.Millisecond)) {
		return 0, 0, errors.New("ERR timeout is out of range")
	}

	timeout := time.Duration(timeoutMS) * time.Millisecond
	return int(replicas64), timeout, nil
}

func (s *Server) executeReplicationWait(
	args [][]byte,
	targetOffset int64,
	cancel <-chan struct{},
	allowBlock bool,
) ([]byte, error) {
	numReplicas, timeout, err := parseWaitArguments(args)
	if err != nil {
		return nil, err
	}

	timer, timeoutC := blockingTimer(timeout)
	if timer != nil {
		defer timer.Stop()
	}

	for {
		count, changed := s.replication.waitSnapshot(targetOffset)
		if count >= numReplicas || !allowBlock {
			return integer(int64(count)), nil
		}

		// Topology and ACK progress share the same change notification. Ask the
		// currently connected replica set again after every such change so a
		// replacement connection can satisfy an already-blocked WAIT.
		s.replication.requestReplicaACKs()

		select {
		case <-changed:
			continue
		case <-timeoutC:
			count, _ = s.replication.waitSnapshot(targetOffset)
			return integer(int64(count)), nil
		case <-cancel:
			return nil, errBlockingClientGone
		}
	}
}
