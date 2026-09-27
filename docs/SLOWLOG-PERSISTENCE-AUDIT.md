# SLOWLOG and persistence controls audit — 2026-09-27

## Scope and evidence

Feature branch: feat/redis82-slowlog-persistence-controls-main. Latest tested implementation: 88f5907589a1733e408ebfafbe04b7acab3c6c8b.

The operator ran the live probes and Go checks on their Linux workstation and reported success. The assistant did not run Go locally (Go was unavailable). The live reference was Redis **8.10.2**, on port 6391, not Redis 8.2. SnugKV ran on port 6392. Redis 8.2.0 source was separately inspected for persistence scheduling and six-field SLOWLOG replies.

## Implemented and observed

- SLOWLOG GET/LEN/RESET/HELP and slowlog CONFIG settings. Invalid counts, overflow, excess arguments, and HELP text covered by regressions from live outputs.
- At threshold zero, RESET records itself; LEN returned 2 after RESET/PING, GET returned LEN/PING/RESET newest first, and LEN after a second RESET returned 1. IDs start at zero and survive RESET.
- TCP entries capture the connection address and client name at recording time. Live PING output included both the remote endpoint and slowlog-audit.
- SAVE, LASTSAVE, BGSAVE, BGREWRITEAOF with the observed argument errors; live SAVE succeeded and BGSAVE acknowledged starting.
- Explicit aof_rewrite_path / -aof-rewrite supports one-off AOF export without enabling journaling. Live CONFIG GET appendonly stayed no; restarting with the export as the active AOF recovered rewrite-test=value.
- Active-AOF export and replacement hold durableMu together. A regression checks lock ownership and replay of writes before/after rewrite.
- INFO persistence exposes running flags, last save time, last save/rewrite ok/err, AOF enabled state, and queued rewrite state. Failure/retry and disabled-journal cases are covered by tests.
- BGSAVE SCHEDULE queues behind a rewrite but errors during another save. BGREWRITEAOF queues behind a save. Regression coverage exercises snapshot/export replay, repeated scheduling, and rewrite after failed save.

## Validation

Operator reported all passing on the latest implementation:
- go test -race -count=1 ./...
- go vet ./...
- go test ./internal/resp -run '^$' -fuzz FuzzReadCommand -fuzztime=10s

Targeted race suites and the live checks above also passed. GitHub CI is a separate merge gate; this document does not claim it has passed.

## Limits and remaining review

- This is an audited subset, not full Redis parity or completion of Phase F.
- SnugKV retains Redis 8.2's six-field SLOWLOG entry. Redis 8.10.2 returns an additional original-argument-count field.
- With no active AOF, SnugKV requires an explicit export destination; Redis retains a default destination even with appendonly=no.
- Active rewrites block commands participating in durableMu, including INFO and scheduling requests arriving during that interval. Scheduling tests exercise the state machine directly; fully responsive Redis-style concurrent rewrites need buffering.
- Transaction-time persistence scheduling, shutdown while a job is pending/running, simultaneous synchronous SAVE and BGSAVE, and automatic periodic snapshots need further lifecycle review.
- Fast TCP GET/SET paths, MULTI/EXEC executed-command visibility, outer EVAL/EVALSHA/EVAL_RO/EVALSHA_RO, and FCALL/FCALL_RO are now covered by focused race-enabled regressions. Argument truncation/redaction remains a separate hardening area.
- Persistence INFO is a subset; native SnugKV snapshot/AOF files are not Redis RDB/AOF file-format exports.

MONITOR is complete on main. This follow-up SLOWLOG hardening slice closes the previously documented fast-path / transaction / scripting / Function visibility gap. See aof-one-off-rewrite.md for the export option and locking trade-off.


## Follow-up hardening — 2026-09-27

The post-MONITOR audit closed a SLOWLOG coverage gap caused by execution paths that bypass `Server.execute()`.

New coverage and fixes:

- TCP fast-path `GET` and `SET` record SLOWLOG entries with the correct client identity without mutating shared execution context.
- Transaction control commands are recorded when executed.
- Commands queued in `MULTI` are recorded when they actually execute under `EXEC`, not when merely queued.
- Killable Lua entry points record outer `EVAL`, `EVALSHA`, `EVAL_RO`, and `EVALSHA_RO` invocations.
- Killable Function entry points record outer `FCALL` and `FCALL_RO` invocations.
- Client identity capture for concurrent fast paths uses an explicit client parameter, avoiding races through `executionClient`.

Operator-reported passing gates on the hardened branch:

- `go test -race ./internal/server -run '^TestSlowlog' -count=1 -v`
- `go test -race ./...`
- `go vet ./...`
- `go test ./internal/resp -run=^$ -fuzz=FuzzReadCommand -fuzztime=20s`

Remaining SLOWLOG hardening is primarily exact Redis argument truncation/redaction behavior and any future command-specific edge cases.
