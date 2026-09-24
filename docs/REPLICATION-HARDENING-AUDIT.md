# Replication Hardening Audit

This audit extends the replication compatibility work after WAIT and WAITAOF were merged.

## Scope

The hardening pass covered failure modes and topology transitions that are easy to miss in normal full-sync / partial-sync tests:

- blocked WAIT / WAITAOF during replica disconnect and reconnect
- replica AOF persistence failure and FACK safety
- replication backlog boundary offsets
- interrupted FULLRESYNC
- interrupted partial resync
- chained replication
- direct-only WAIT / WAITAOF accounting across chains
- topology loss while WAIT / WAITAOF are blocked

## Findings and fixes

### 1. WAIT / WAITAOF reconnect ACK solicitation

Previously a blocked WAIT or WAITAOF issued GETACK only once. If that replica disconnected and a replacement replica connected while the client remained blocked, the new replica might never be asked for an ACK/FACK.

Fix:
- re-issue GETACK after replica ACK/topology state changes while the requested acknowledgement count remains unsatisfied

Regression coverage:
- TestReplicationWaitSurvivesReplicaReconnect
- TestWaitAOFSurvivesReplicaReconnect

### 2. Replica AOF failure could fabricate FACK

A failed replica AOF append left durability sequence zero. The durability tracker could still map a replication offset to sequence zero, which was then considered already synced.

Fix:
- never record replica AOF offset mappings after durability failure
- never record zero append-sequence mappings
- never expose a replica fsynced offset while durability is failed

Regression coverage:
- TestReplicaAOFPersistenceFailureDoesNotAdvanceFACK
- TestReplicaAOFPersistenceFailurePreventsLaterReplicationApply

### 3. Backlog boundary semantics

Audited:
- exact repl_backlog_first_byte_offset is accepted
- one byte before retained backlog forces full resync
- master_repl_offset + 1 is accepted with no replay payload
- offsets beyond master+1 are rejected
- backlog first-byte offset advances exactly after eviction
- replay from inside a retained backlog entry replays the containing unit and everything after it

Regression coverage:
- TestReplicationBacklogBoundaryOffsets
- TestReplicationPartialResyncReplaysFromContainingBacklogEntry
- TestReplicationBacklogFirstOffsetTracksEvictionExactly

### 4. Interrupted FULLRESYNC continuation state

Previously FULLRESYNC committed the new master run ID and offset before the snapshot had been completely read, decoded, applied and persisted.

A connection loss during snapshot transfer could therefore leave a continuation tuple for a dataset baseline that had never been installed.

Fix:
- stage FULLRESYNC run ID / offset / stream mode
- read and decode the entire snapshot
- apply and persist the snapshot
- persist the matching replication checkpoint
- only then commit the new in-memory continuation state

Regression coverage:
- TestInterruptedFullResyncDoesNotAdvanceContinuationState

### 5. Interrupted partial resync progress

Verified both Snug logical-frame and Redis command-stream modes.

Invariant:
- complete replay unit => apply and advance offset
- incomplete trailing frame / command => no mutation and no extra offset advancement

Regression coverage:
- TestInterruptedPartialResyncDoesNotAdvancePastIncompleteSnugFrame
- TestInterruptedPartialResyncDoesNotAdvancePastIncompleteRedisCommand

### 6. Chained Snug replication

A middle replica could consume upstream Snug frames but did not forward them to downstream replicas.

Fix:
- forward the exact received Snug frame into the middle node's own replication backlog
- fan that frame out to downstream replicas
- advance the middle offset exactly once
- preserve byte-for-byte Snug PSYNC offset semantics across hops

Validated topology:

    primary -> middle replica -> leaf replica

Regression coverage:
- TestChainedReplicationPrimaryReplicaReplica
- TestChainedReplicationLeafReconnectsThroughMiddle

### 7. Direct-only acknowledgement accounting

WAIT and WAITAOF must count only directly connected replicas, not transitive replicas deeper in a chain.

Validated:
- primary counts only middle
- middle counts only leaf
- leaf does not inflate the primary's WAIT / WAITAOF count

Regression coverage:
- TestChainedReplicationWaitCountsOnlyDirectReplicas
- TestChainedReplicationWAITAOFCountsOnlyDirectFACKs

### 8. Replica topology loss while blocked

Replica removal wakes WAIT / WAITAOF so they can re-evaluate topology, but does not cause early success.

If the requested count remains unsatisfied, the command continues blocking until:
- another replica reconnects and acknowledges, or
- the timeout expires

Regression coverage:
- TestReplicationWaitReplicaTopologyLossKeepsWaitingUntilTimeout
- TestWaitAOFReplicaTopologyLossKeepsWaitingUntilTimeout

## Validation status

Focused hardening tests passed during development, including race runs for the affected WAIT / WAITAOF and replication paths.

Final branch gate:

- go test ./...
- go test -race ./internal/server
- go vet ./...

must remain green before merge.
