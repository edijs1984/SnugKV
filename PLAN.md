# SnugKV Delivery Plan

This plan is the implementation roadmap for taking the current prototype to a completion-ready single-node in-memory store aligned with the product specification in SNUGKV_AGENT_SPEC.md.

## Phase 0: Foundations and working rules

### Completed

- Created the Go module and local toolchain usage path.
- Added initial spec-driven engine tests for exact round-trip, TTL expiry, and counter increment behavior.
- Implemented a minimal in-memory engine with exact byte preservation and TTL expiration.
- Implemented a minimal command dispatcher for `PING`, `ECHO`, `SET`, `GET`, `DEL`, `INCR`, and `TTL`.
- Implemented a RESP parser and a basic TCP server loop.
- Added configuration defaults and power-of-two shard validation.
- Added the `cmd/snugkv` entry point.

### Remaining

- Review and formalize the current design against the spec, especially around concurrency and server lifecycle.
- Add a clear ADR for the chosen config format and runtime lifecycle.
- Maintain `PROGRESS.md` as the living status file for completed and current work.

---

## Phase 1: Correctness baseline for single-node engine

### Goal

Produce a correct, stable engine that passes the spec’s invariants before optimization or codec work begins.

### Must do

1. Replace the single `map[string]entry` with a shard-aware engine model.
2. Ensure `SET`/`GET` are exact byte round-trips.
3. Ensure `INCR` and `DECR` match Redis-style integer semantics and overflow behavior.
4. Implement lazy expiration and active cleanup.
5. Add tests for overwrite, delete, expiration boundary, and stale read safety.
6. Add race tests for concurrent reads/writes.
7. Add a clear memory-accounting model for logical vs physical bytes.

### Expected deliverables

- `internal/engine/shard.go`
- `internal/engine/engine.go` refactored to shard-based operations
- tests for concurrency, overwrite, and TTL transitions

---

## Phase 2: Full command compliance for the supported subset

### Goal

Match the minimum supported command set from the specification.

### Must do

1. Implement all commands in Section 7.2 for the supported subset.
2. Implement correct RESP2 error semantics.
3. Add command arity validation and integer parsing errors.
4. Add `SELECT 0`, `HELLO 2`, `INFO`, `DBSIZE`, `COMMAND`, `EXISTS`, `MGET`, `SETNX`, `MSET`, `STRLEN`, `EXPIRE`, `PEXPIRE`, `PERSIST`, and the administrative `SNUG.*` commands.
5. Add tests for supported commands and unsupported-command behavior.

### Expected deliverables

- command dispatcher coverage for the supported Redis-compatible subset
- RESP error/response validation tests

---

## Phase 3: Storage and value representation

### Goal

Add the internal value model and codec infrastructure promised by the spec.

### Must do

1. Define the internal encoded record format and metadata envelope.
2. Implement a codec registry with a raw fallback.
3. Add canonical integer codec support.
4. Add UUID, timestamp, and JSON-shape codec stubs with exact round-trip verification.
5. Add dictionary and schema admission rules.
6. Document memory format and codec IDs in `docs/memory-format.md` and ADRs under `docs/decisions/`.

### Expected deliverables

- `internal/codec/*` package structure
- codec `ID` registry and common interfaces
- docs and ADRs for the chosen format decisions

---

## Phase 4: Policy engine and background optimizer

### Goal

Implement the adaptive policy logic behind workload-aware encoding without breaking correctness.

### Must do

1. Define value heat classes and score model.
2. Implement optimizer sampling and safe rewrite protocol.
3. Add stale rewrite rejection logic using version checks.
4. Add budget enforcement for CPU, scratch memory, and bytes encoded per second.
5. Add anti-thrashing and hysteresis for codec switching.

### Expected deliverables

- optimizer package and policy scoring logic
- tests covering stale rewrite protection and rewrite rejection

---

## Phase 5: Persistence and crash safety

### Goal

Support AOF and snapshots only after the in-memory engine is stable.

### Must do

1. Add AOF writer for logical mutations.
2. Implement snapshot format with versioning and checksums.
3. Add corruption and truncation recovery tests.
4. Document recovery order and failure modes.

### Expected deliverables

- `internal/persistence/*`
- AOF/snapshot golden tests and recovery validation

---

## Phase 6: Observability, metrics, and operational readiness

### Goal

Make SnugKV operable and diagnosable under load.

### Must do

1. Add metrics for commands, latency, memory, codec usage, queue depth, and expiration/eviction counters.
2. Implement `INFO memory` and `SNUG.*` admin commands.
3. Add structured logging with safe redaction.
4. Ensure metrics and admin interfaces are isolated and secure.

### Expected deliverables

- `internal/stats/*` and observability hooks
- admin command output and docs

---

## Phase 7: Benchmarking and product validation

### Goal

Demonstrate the product hypothesis with reproducible data and measured memory gains.

### Must do

1. Add benchmark datasets for session JSON, API responses, telemetry, random values, and hot counters.
2. Run benchmarks against raw baseline and Redis-compatible subset.
3. Measure p50/p95/p99 latency and memory footprint.
4. Publish results with machine details and exact config.
5. Only document memory or performance improvements with reproducible benchmark numbers.

### Expected deliverables

- `benchmarks/` and benchmark scripts
- benchmark results file or markdown summary

---

## Phase 8: Final production hardening

### Goal

Prepare the project for final review and handoff.

### Must do

1. Run required verification commands:
   - `gofmt ./...`
   - `go vet ./...`
   - `go test ./...`
   - `go test -race ./...`
2. Review all docs and ADRs.
3. Verify concurrency safety, expiration correctness, and AOF/snapshot failures.
4. Confirm the user-facing product behavior matches the spec’s compatibility promise.
5. Ensure no unsupported claim is documented without benchmark evidence.

### Final exit criteria

- code compiles cleanly
- all targeted tests pass
- race tests pass for concurrency changes
- public behavior is documented
- memory ownership is clear
- failure paths are covered
- benchmarks exist for performance-sensitive work
- no unverified claims are published

---

## Implementation status

Phases 0 through 8 are implemented for the repository's single-node scope. The
remaining work is release validation: million-record comparative benchmarks,
multi-run variance, third-party client matrices, and a 24-hour soak. See
`PROGRESS.md` for current verification evidence and environment constraints.
