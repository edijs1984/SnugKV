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

SnugKV uses a dual-licensing model:

- GNU Affero General Public License v3.0 (AGPL-3.0) for the public project;
- separate commercial licenses offered by the project owner.

External contributions are accepted only after the contributor agrees to the
SnugKV Contributor License Agreement (CLA). The CLA grants the project owner
the rights necessary to distribute contributed code under both the AGPL-3.0
and separate commercial licenses.

Until the CLA process is published, please open an issue before submitting a
substantial code contribution.

By submitting a contribution after agreeing to the CLA, you confirm that you
have the legal right to submit the contribution and grant the rights described
by the CLA.

