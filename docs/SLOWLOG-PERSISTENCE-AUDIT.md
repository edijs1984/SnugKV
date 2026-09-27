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
- Active AOF rewrites now use a rewrite-delta buffer, so ordinary durable writes continue during the background disk phase and only append admission is briefly serialized for the final delta flush and atomic file swap.
- Automatic periodic snapshots are now implemented as an opt-in lifecycle feature. Transaction-time persistence scheduling, SAVE/BGSAVE mutual exclusion, shutdown/handoff safety, and periodic background snapshots are covered by lifecycle tests.
- Fast TCP GET/SET paths, MULTI/EXEC executed-command visibility, outer EVAL/EVALSHA/EVAL_RO/EVALSHA_RO, FCALL/FCALL_RO, and Redis-style SLOWLOG truncation/redaction are now covered by focused race-enabled regressions.
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

Exact Redis argument truncation/redaction behavior is now covered for the audited supported command surface. Remaining SLOWLOG work is limited to future command-specific edge cases as new sensitive command/config surfaces are added.


## Persistence shutdown lifecycle hardening — 2026-09-27

This follow-up closes the audited shutdown gap for background persistence jobs.

Changes:

- Background BGSAVE and BGREWRITEAOF work is registered in a dedicated persistence-job wait group.
- TCP server shutdown waits for all tracked persistence jobs before returning.
- Rewrite -> scheduled BGSAVE handoffs are registered before the parent job completes.
- BGSAVE -> scheduled BGREWRITEAOF handoffs remain inside the same lifecycle wait.
- Regression coverage verifies that shutdown waiting does not return while an active rewrite is paused and does not return before queued successor persistence work completes.

A real race was exposed during the audit: the rewrite -> scheduled BGSAVE handoff originally launched the save with a plain goroutine, allowing the persistence wait to return early. The regression reproduced the consequence by letting the test temp directory be removed while the untracked save was still running. The handoff now uses the tracked persistence-job launcher.

Operator-reported passing gates:

- `go test -race ./internal/server -run '^(TestPersistence|TestBGSave|TestBGRewriteAOF)' -count=1 -v`
- `go test -race ./...`
- `go vet ./...`
- `go test ./internal/resp -run=^$ -fuzz=FuzzReadCommand -fuzztime=20s`

Remaining persistence lifecycle work: transaction-time scheduling, SAVE/BGSAVE interaction parity, and automatic periodic snapshot scheduling.


## SAVE/BGSAVE lifecycle hardening — 2026-09-27

This follow-up closes the audited synchronous SAVE versus BGSAVE interaction gap.

Changes:

- Synchronous `SAVE` rejects while a background save is active with `ERR Background save already in progress`.
- `BGSAVE` and `BGSAVE SCHEDULE` reject while synchronous `SAVE` is active.
- Synchronous SAVE state is tracked under the persistence job mutex so the two save modes cannot overlap.
- `BGSAVE SCHEDULE` does not queue behind synchronous SAVE; it follows the same active-save rejection semantics.

Operator-reported passing gates:

- `go test -race ./internal/server -run '^(TestPersistence|TestSave|TestBGSave|TestBGRewriteAOF)' -count=1 -v`
- `go test -race ./...`
- `go vet ./...`
- `go test ./internal/resp -run=^$ -fuzz=FuzzReadCommand -fuzztime=20s`

Remaining persistence lifecycle work: transaction-time persistence scheduling and automatic periodic snapshot scheduling.


## Transaction-time persistence scheduling hardening — 2026-09-27

This follow-up closes the audited MULTI/EXEC persistence scheduling gap.

Changes:

- Background save record capture now happens synchronously at the BGSAVE command's execution point, while snapshot file writing remains asynchronous.
- A transaction such as `SET before; BGSAVE; SET after; EXEC` produces a snapshot containing the pre-BGSAVE state and excluding writes that occur later in the same transaction.
- Scheduled BGSAVE handoff after BGREWRITEAOF also captures the record set before launching the asynchronous save worker.
- Active-AOF BGREWRITEAOF inside EXEC is regression-tested for deadlock freedom and preservation of transaction writes that occur later in the same EXEC. The rewrite worker runs after EXEC releases `durableMu`, so the rewritten active AOF reflects the final transaction state.

Operator-reported passing gates:

- `go test -race ./internal/server -run '^(TestPersistence|TestSave|TestBGSave|TestBGRewriteAOF|TestTransactionBGSAVE|TestTransactionBGRewriteAOF)' -count=1 -v`
- `go test -race ./...`
- `go vet ./...`
- `go test ./internal/resp -run=^$ -fuzz=FuzzReadCommand -fuzztime=20s`

Remaining persistence lifecycle work: automatic periodic snapshot scheduling.


## Automatic periodic snapshot scheduling — 2026-09-27

This follow-up closes the remaining audited persistence lifecycle gap.

Changes:

- Added opt-in `snapshot_interval_ms` configuration. The default is `0`, which disables automatic snapshots.
- Added `SNUGKV_SNAPSHOT_INTERVAL_MS` and `-snapshot-interval-ms` configuration surfaces.
- Enabling the interval requires a configured `snapshot_path`.
- The process scheduler triggers the existing background-save lifecycle rather than implementing a separate persistence worker.
- Periodic ticks skip cleanly while conflicting persistence work is already active, preserving the existing BGSAVE/AOF rewrite state machine.
- Shutdown waiting, persistence status tracking, failure logging, and record capture semantics remain centralized in the existing background-save implementation.

Operator-reported passing gates:

- `go test -race ./internal/config ./internal/server ./cmd/snugkv -count=1`
- `go test -race ./...`
- `go vet ./...`
- `go test ./internal/resp -run=^$ -fuzz=FuzzReadCommand -fuzztime=20s`

With this slice complete, the persistence lifecycle items identified by this audit are closed. Remaining Section F work, if any, is compatibility hardening outside this lifecycle list, such as exact SLOWLOG argument truncation/redaction and the larger architectural question of fully concurrent Redis-style AOF rewrite buffering.


## SLOWLOG argument truncation and redaction — 2026-09-27

This follow-up closes the audited SLOWLOG argv retention/privacy gap against Redis 8.2 behavior.

Changes:

- SLOWLOG entries retain at most 32 arguments.
- When the original command exceeds that limit, the final retained slot is replaced with `... (N more arguments)`.
- Individual arguments are capped at 128 bytes and append `... (N more bytes)` when truncated.
- Supported ACL-sensitive commands redact user/rule payloads in SLOWLOG:
  - `ACL SETUSER`
  - `ACL GETUSER`
  - `ACL DELUSER`
- Supported MIGRATE authentication options redact credentials:
  - `AUTH`
  - `AUTH2`
- TCP ACL commands are now recorded in SLOWLOG, closing a connection-layer visibility gap discovered by the end-to-end regression.
- End-to-end `SLOWLOG GET` coverage verifies that sensitive values do not leak and truncation markers survive serialization.

Redis 8.2 reference constants and behavior were taken from upstream `src/slowlog.c`, `src/slowlog.h`, and `tests/unit/slowlog.tcl`.

Operator-reported passing gates:

- `go test -race ./internal/server -run '^TestSlowlog' -count=1 -v`
- `go test -race ./...`
- `go vet ./...`
- `go test ./internal/resp -run=^$ -fuzz=FuzzReadCommand -fuzztime=20s`

With this slice complete, the audited SLOWLOG coverage gaps identified in this document are closed for SnugKV's currently supported command surface.


## Concurrent AOF rewrite buffering — 2026-09-27

This follow-up closes the final persistence concurrency gap identified by the Section F audit.

Changes:

- Active AOF rewrites establish a rewrite boundary before capturing the base logical image.
- Writes accepted after that boundary continue to the live AOF and are also copied into an in-memory rewrite delta buffer.
- The base image is written in the background without holding `durableMu`.
- Append admission is only serialized briefly during final delta flush, fsync, directory sync, and atomic file replacement.
- Existing synchronous `Rewrite()` callers continue to use the same buffered lifecycle through `BeginRewrite()` / `FinishRewrite()`.
- BGREWRITEAOF preserves transaction-position correctness: writes that occur after the rewrite command in the same EXEC are retained through the rewrite delta.
- The previous blocking regression was inverted: durable writes must now complete while the background rewrite is paused.
- Persistence scheduling, shutdown waiting, status reporting, fsync policy handling, replay/recovery, and one-off rewrite behavior remain covered.

Operator-reported passing gates:

- `go test -race ./internal/persistence -run '^TestBufferedRewrite' -count=1 -v`
- `go test -race ./internal/server -run '^TestTransactionBGRewriteAOFIncludesLaterTransactionWrites$' -count=1 -v`
- `go test -race ./internal/server -run '^TestBGRewriteAOFAllowsWritesDuringBackgroundRewrite$' -count=1 -v`
- `go test -race ./internal/persistence -count=1 -v`
- `go test -race ./internal/server -run '^(TestPersistence|TestSave|TestBGSave|TestBGRewriteAOF|TestTransactionBGRewriteAOF|TestBuffered|TestAOF)' -count=1 -v`
- `go test -race -count=1 ./...`
- `go vet ./...`
- `go test ./internal/resp -run=^$ -fuzz=FuzzReadCommand -fuzztime=20s`

With this slice complete, the persistence/SLOWLOG lifecycle and concurrency items tracked by Section F are closed for the current SnugKV architecture.
