# Security Policy

## Supported versions

SnugKV is currently in alpha. Security fixes target the latest code on `main`.

| Version | Supported |
|---|---|
| `main` / latest alpha | Yes |
| Older snapshots | No |

## Reporting a vulnerability

Do not open a public GitHub issue for a suspected security vulnerability.

Report privately to the project maintainer and include:
- affected version or commit;
- deployment environment;
- reproduction steps or minimal proof of concept;
- expected and observed behavior;
- potential impact;
- suggested mitigation, if known.

Do not include production credentials, customer data, secrets, or other sensitive material.

## Scope

Security-sensitive areas include RESP parsing, connection handling, persistence
and recovery, denial-of-service behavior, admin-listener isolation, ACL/auth
enforcement, replication TLS/authentication, cluster write fencing, internal
control-plane authentication, unsafe configuration defaults, and dependency
vulnerabilities.

## Distributed security

Configured cluster mode requires a dedicated `cluster_control_auth` secret.
Private peer coordination establishes a connection-scoped
`SNUG.INTERNAL AUTH` identity; normal ACL/admin authorization alone must not
grant access to peer-only cluster/failover mutation RPCs. `RESET`, `AUTH`, and
`HELLO` revoke that identity.

Use TLS for replication/internal migration on untrusted networks, keep
`cluster_control_auth`, `masterauth`, ACL secrets, certificates, and private
keys out of logs/source control, and restrict cluster/replication endpoints with
network policy as an additional layer.

The distributed system has not yet completed the full multi-process chaos,
partition, disk-failure, and long-running soak matrix required for a
production-complete claim. See `docs/PROJECT-STATE.md`.

## Alpha status

SnugKV `v0.1` is intended for evaluation, development, benchmarking, and
non-critical workloads. It has not received an independent security audit.

Do not expose the public or admin listeners directly to an untrusted network
without appropriate network controls. Keep the admin listener bound to a trusted
interface; the default loopback binding is recommended.
