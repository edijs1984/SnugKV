# ADR-002: Atomic multi-key commands

Status: Accepted

Multi-key reads and mutations lock shards in ascending order and release them in
reverse order. The baseline locks all shards, providing atomic MSET, MGET, DEL,
and EXISTS semantics even across shards. This favors a simple auditable contract
over maximum concurrency. A later change may lock only touched shards without
weakening atomicity. Readers copy values while locks are held; no locks are held
during network writes. Concurrent MSET/MGET tests validate atomic visibility.
