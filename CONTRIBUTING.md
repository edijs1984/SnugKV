# Contributing

SnugKV is a Go project. Use Go 1.27 or newer.

Before opening a change, run:

```sh
make check
```

Behavior changes should include focused tests and documentation updates. Changes
to codecs, persistence, concurrency, or memory accounting should also include
the relevant benchmark, race, recovery, or property coverage. Record major
design decisions as ADRs in `docs/decisions/` and keep `PROGRESS.md` current.

The implementation rules and acceptance criteria are in
`SNUGKV_AGENT_SPEC.md`.
