# Changelog

All notable changes to SnugKV will be documented in this file.

## [Unreleased]

### Added

- RESP2 TCP server with bounded protocol parsing.
- Sharded in-memory storage engine.
- Expiration and TTL operations.
- Memory limits and inspection commands.
- Adaptive canonical encodings for selected value types.
- Optional JSON-shape encoding and compression candidates.
- Logical persistence with checksum-protected frames.
- Separate admin listener for `SNUG.*` diagnostics and controls.
- Compatibility smoke tests for ioredis, node-redis, redis-py, and go-redis.
- TCP client soak harness.
- Engine soak workload with correctness and memory reporting.
- AGPL-3.0 licensing with separate commercial-license terms available.

### Hardened

- Large arena allocations up to the RESP bulk-size boundary.
- Connection-level panic recovery.
- Exact RESP maximum-bulk boundary behavior.
- Lazy JSON-shape store allocation.
- Persistence restart and corruption recovery tests.

### Verified

- One-hour engine soak completed with zero mismatches.
- One-hour TCP/RESP soak completed with zero client errors.
- TCP soak showed stable engine-accounted memory and stable arena allocation.

## [0.1.0-alpha] - 2026-09-14

Initial public alpha release.
