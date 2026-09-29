package server

import (
	"time"
)

func (s *Server) clusterConsistencyReply() []byte {
	state := s.clusterStateSnapshot()
	assigned := 0
	for _, owner := range state.owners {
		if owner != "" {
			assigned++
		}
	}
	coverageOK := assigned == clusterSlotCount
	transitioning := clusterRebalanceHasActiveTransition(state)
	fenced, fenceReason, leaseHolder, leaseUntil := s.failoverWriteFenceStatus(time.Now())

	status := "ok"
	if !coverageOK {
		status = "fail"
	} else if transitioning || fenced {
		status = "degraded"
	}

	leaseUntilMS := int64(0)
	if !leaseUntil.IsZero() {
		leaseUntilMS = leaseUntil.UnixMilli()
	}

	return array(
		formatBulkString([]byte("status")),
		formatBulkString([]byte(status)),
		formatBulkString([]byte("ownership_digest")),
		formatBulkString([]byte(clusterOwnershipDigest(state))),
		formatBulkString([]byte("local_topology_epoch")),
		integer(int64(state.epoch)),
		formatBulkString([]byte("coverage_ok")),
		integer(boolToInt64(coverageOK)),
		formatBulkString([]byte("assigned_slots")),
		integer(int64(assigned)),
		formatBulkString([]byte("transitioning")),
		integer(boolToInt64(transitioning)),
		formatBulkString([]byte("writes_fenced")),
		integer(boolToInt64(fenced)),
		formatBulkString([]byte("fence_reason")),
		formatBulkString([]byte(fenceReason)),
		formatBulkString([]byte("lease_holder")),
		formatBulkString([]byte(leaseHolder)),
		formatBulkString([]byte("lease_until_ms")),
		integer(leaseUntilMS),
	)
}
