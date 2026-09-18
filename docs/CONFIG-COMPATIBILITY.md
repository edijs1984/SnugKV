# CONFIG compatibility

SnugKV implements the common Redis CONFIG surface needed by tooling while keeping the advertised configuration set tied to real SnugKV behavior.

## Implemented commands

- `CONFIG GET <pattern> [pattern ...]`
- `CONFIG SET <parameter> <value> [parameter value ...]`
- `CONFIG RESETSTAT`
- `CONFIG REWRITE`
- `CONFIG HELP`

## Runtime-mutable settings

The following settings mutate the component that actually enforces them:

- `maxmemory` -> engine memory admission limit
- `maxmemory-policy` -> SnugKV eviction policy
- `maxclients` -> TCP accept-loop connection limit
- `appendfsync` -> live persistence.Log fsync policy

Multi-parameter CONFIG SET validates the complete request before applying updates.

## CONFIG GET boundary

SnugKV returns only parameters it genuinely implements instead of fabricating Redis configuration options.

Current exposed settings include:

- `maxmemory`
- `maxmemory-policy`
- `maxclients`
- `appendfsync`
- `appendonly`
- `databases`

`databases` intentionally reports `1` because SnugKV is a single-logical-database server.

The supported eviction policies remain the policies SnugKV actually implements:

- `noeviction`
- `allkeys-lru`
- `volatile-lru`

## RESETSTAT

`CONFIG RESETSTAT` clears SnugKV's total command counter and per-command metrics. The next INFO request is itself counted, matching the observable Redis behavior tested during the differential audit.

## REWRITE

SnugKV uses strict JSON configuration.

When started with `--config <path>`, `CONFIG REWRITE` atomically rewrites that source JSON file with the effective runtime configuration and fsyncs both the file and containing directory.

When no source config file exists, SnugKV returns:

`ERR The server is running without a config file`

End-to-end restart testing confirmed rewritten `maxmemory`, `maxmemory-policy`, `maxclients`, and `appendfsync` values survive restart.

## Runtime profile config

The repository `snugkv.json` captures the normal development/runtime profile, including:

- `encoding: true`
- `compression: true`
- `json_shape: true`
- `go_memory_limit: 134217728` (128 MiB)

This replaces the common environment-heavy launch with:

```bash
go run ./cmd/snugkv --config ./snugkv.json
```

If `go_memory_limit` is zero or omitted, SnugKV does not call `debug.SetMemoryLimit`, preserving the Go runtime's existing `GOMEMLIMIT` behavior.

## COMMAND metadata

Redis-shaped parent/subcommand metadata and documentary output are implemented for:

- `CONFIG`
- `CONFIG|GET`
- `CONFIG|SET`
- `CONFIG|RESETSTAT`
- `CONFIG|REWRITE`
- `CONFIG|HELP`

The captured Redis 8.2 arities, flags, ACL categories, tips, histories, argument trees, and supported subcommand ordering are represented for this implemented surface.

## Intentional compatibility boundaries

SnugKV does not expose hundreds of Redis-specific settings that have no corresponding runtime feature.

The goal is tooling compatibility without pretending unsupported Redis topology, persistence, allocator, TLS, cluster, replica, or module configuration exists.

## Validation

The milestone was exercised with focused CONFIG and persistence tests plus the normal project gates:

```text
go test -race -count=1 ./...
go vet ./...
go test ./internal/resp -run '^$' -fuzz FuzzReadCommand -fuzztime=10s
go build ./cmd/snugkv
```

Live Redis 8.2 differential testing covered CONFIG HELP, GET exact/pattern/multiple, SET valid/invalid values, arity, RESETSTAT, REWRITE, unknown subcommands, COMMAND INFO, and COMMAND DOCS.
