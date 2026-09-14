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
and recovery, denial-of-service behavior, admin-listener isolation, unsafe
configuration defaults, and dependency vulnerabilities.

## Alpha status

SnugKV `v0.1` is intended for evaluation, development, benchmarking, and
non-critical workloads. It has not received an independent security audit.

Do not expose the public or admin listeners directly to an untrusted network
without appropriate network controls. Keep the admin listener bound to a trusted
interface; the default loopback binding is recommended.
