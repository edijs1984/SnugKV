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

## Licensing of contributions

By submitting a contribution to SnugKV, you agree that your contribution
may be distributed under the project's GNU Affero General Public License
v3.0.

The project may introduce a Contributor License Agreement (CLA) in the
future for contributors whose changes are incorporated into commercially
licensed versions of SnugKV.

