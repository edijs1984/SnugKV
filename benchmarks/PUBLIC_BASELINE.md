# Public Redis-vs-SnugKV Baseline

This benchmark is the retained, reproducible starting point for public
Redis-vs-SnugKV performance comparisons.

It intentionally separates measurement from marketing claims.

## Scope

The first public baseline compares Redis 8.2 with optimized SnugKV using isolated
fresh server/container processes, deterministic datasets, and three runs per
workload by default.

Default profiles:

- counter — canonical 10-byte integers
- uuid — canonical UUID strings
- cache-json — 1 KiB structured application-cache objects
- text — 256-byte recurring application text
- random — 256-byte incompressible control

Default workloads:

- LOAD / SET
- pipelined random GET
- mixed 90% GET / 10% SET
- SET PX TTL mutation

## Default command

    bash scripts/bench/public-baseline.sh

Default matrix:

    keys        = 1,000,000
    get ops     = 2,000,000
    mixed ops   = 1,000,000
    ttl ops     = 1,000,000
    workers     = 8
    pipeline    = 256
    runs        = 3
    servers     = Redis 8.2, SnugKV optimized
    profiles    = counter, uuid, cache-json, text, random

## Smaller validation run

Before committing machine time to the full matrix:

    KEYS=100000 \
    GET_OPS=200000 \
    MIXED_OPS=100000 \
    TTL_OPS=100000 \
    RUNS=1 \
    PROFILES="counter cache-json random" \
    bash scripts/bench/public-baseline.sh

The small run validates the harness only. Do not use it as a public benchmark.

## Output

Each run creates a timestamped benchmark-results/public-baseline-* directory with:

- environment.json
- server-versions.txt
- public-summary.json
- PUBLIC_BASELINE_RESULTS.md
- raw per-profile/per-run JSON
- per-profile matrix-summary.json

The environment record captures exact Git commit/branch, clean worktree state,
CPU model/count, OS/architecture, total physical memory, Go version, Docker
version, benchmark parameters, Redis image identity, and SnugKV image identity.

## Interpretation rules

1. Quote medians, not best-of-N values.
2. Preserve workload/profile/worker/pipeline context.
3. Pipelined LOAD/GET percentiles are amortized per-operation batch times.
4. SnugKV used_memory is engine-accounted memory and is not process RSS.
5. Redis and SnugKV memory values do not use identical accounting models.
6. Container memory should be reported separately when process footprint matters.
7. Do not generalize compressible-profile results to random/incompressible data.
8. Do not mix persistence-off results with AOF/fsync results.
9. Keep standalone, persistence, and cluster baselines as separate benchmark slices.
10. Public README claims should link to retained artifacts or committed summaries.

## Follow-up slices

After this standalone baseline is retained, add separate controlled suites for:

- AOF off / everysec / always
- replication overhead
- Cluster direct vs MOVED/redirect/cache routing
- reshard throughput
- failover recovery latency
- worker/pipeline scaling
- dedicated-hardware reruns
