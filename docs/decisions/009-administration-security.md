# ADR-009: Loopback-only administration

Status: Accepted

Version 1 isolates administrative RESP commands and Prometheus metrics on separate
listeners that accept only literal loopback IP addresses. The admin listener
allows diagnostics, compaction, and AOF rewrite plus connection diagnostics; it
rejects data mutations. Public clients cannot use `MORPH.*` while the admin
listener is active. Metrics and logs omit keys and values.

Native TLS and password authentication are deferred. Remote administration must
use an authenticated TLS proxy on the host while these listeners remain loopback
bound. Adding native authentication requires a new ADR, constant-time comparison,
and credential lifecycle documentation.
