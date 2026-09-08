# ADR-003: Configuration and runtime lifecycle

Status: Accepted

Use strict JSON with standard-library parsing, avoiding a dependency for YAML or
TOML. Defaults are overridden by a configuration file, SNUGKV_ environment
variables, and flags, in that order. Unknown fields and trailing JSON are errors.
All settings require restart and validation occurs before listening.

The executable owns periodic expiration cleanup and signal handling. SIGINT and
SIGTERM stop accepting clients, close active connections to unblock I/O, and wait
for all server goroutines. A connection cap bounds concurrent handlers. Commands
are sequential within a connection and deadlines apply to each complete request
and response. Existing constructors keep defaults for embedded tests/callers.

Full-shard cleanup scans are the initial implementation; a bounded scheduler is
still required before claiming latency bounds on very large datasets.
