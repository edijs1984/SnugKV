# First Release Gap Classification

> Authoritative Phase A4 release queue derived from the Redis 8.2 oracle audit and real-client A3 evidence.

## Executive result

- Redis-only A2 candidate commands: **58**
- REQUIRED candidate commands: **0**
- USEFUL candidate commands: **9**
- DEFER candidate commands: **49**
- Already-implemented subcommands with incomplete structured `COMMAND` metadata: **49**

The supported first-release client/workflow matrix did not require any of the 58
Redis-only candidate commands. First-release scope therefore remains evidence-driven
rather than chasing broad Redis command-count parity.

## Required release work outside the 58 command candidates

| Gap | Classification | Evidence | Implementation slice |
|---|---|---|---|
| Structured `COMMAND` metadata for already-implemented subcommands | **REQUIRED** | A2 found 49 Redis subcommands whose behavior exists in SnugKV but is not represented in structured command metadata. This is an introspection/compatibility defect, not missing behavior. | Phase B1 |
| `CLUSTER SLOTS` replica advertisement | **REQUIRED — COMPLETE IN A3** | Persistent Cluster-client failover exposed that owner-only `CLUSTER SLOTS` replies could leave a client without a surviving topology endpoint. A3 added replica endpoints and regression coverage. | A3 / PR #254 |

## Candidate-command decisions

| Command | A4 classification | Evidence / rationale |
|---|---|---|
| `CF.COMPACT` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLIENT|NO-EVICT` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLIENT|NO-TOUCH` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLIENT|PAUSE` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLIENT|REPLY` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLIENT|TRACKINGINFO` | **USEFUL** | Complements the already-supported CLIENT TRACKING/CACHING feature with low-risk observability. |
| `CLIENT|UNPAUSE` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|ADDSLOTSRANGE` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|BUMPEPOCH` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|COUNT-FAILURE-REPORTS` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|DELSLOTSRANGE` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|FAILOVER` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|FORGET` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|HELP` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|LINKS` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|MEET` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|MYSHARDID` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|REPLICAS` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|REPLICATE` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|RESET` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|SAVECONFIG` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|SET-CONFIG-EPOCH` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|SLAVES` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `CLUSTER|SLOT-STATS` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `COMMAND|HELP` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `FAILOVER` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `FT.ADD` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `FT.DEL` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `FT.DROP` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `FT.GET` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `FT.MGET` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `FT.SYNADD` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `GEORADIUSBYMEMBER_RO` | **USEFUL** | Low-risk read-only alias that improves drop-in compatibility for GEO users. |
| `GEORADIUS_RO` | **USEFUL** | Low-risk read-only alias that improves drop-in compatibility for GEO users. |
| `JSON.NUMPOWBY` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `LATENCY` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `LATENCY|DOCTOR` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `LATENCY|GRAPH` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `LATENCY|HELP` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `LATENCY|HISTOGRAM` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `LATENCY|HISTORY` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `LATENCY|LATEST` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `LATENCY|RESET` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `MEMORY|DOCTOR` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `MEMORY|HELP` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `MEMORY|MALLOC-STATS` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `MEMORY|PURGE` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `MEMORY|STATS` | **USEFUL** | Materially improves runtime/memory operability without changing data semantics. |
| `READONLY` | **USEFUL** | Useful for explicit replica-read Cluster workflows; not required by the tested default clients. |
| `READWRITE` | **USEFUL** | Companion mode reset for READONLY; useful if replica-read mode is supported. |
| `REPLCONF` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `SCRIPT|HELP` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `SHUTDOWN` | **USEFUL** | Operationally useful graceful-stop compatibility; not exercised automatically by clients. |
| `SLAVEOF` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `SUBSTR` | **USEFUL** | Trivial legacy alias for GETRANGE; low-risk compatibility win. |
| `SYNC` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `XGROUP|HELP` | **DEFER** | Not observed by the supported A3 client/workflow matrix, with no first-release blocker demonstrated. |
| `XSETID` | **USEFUL** | Useful stream-recovery/admin primitive for advanced Streams users. |

## Phase B policy

Only the required metadata-completion slice blocks the release.

The USEFUL commands above are not release blockers. They may be accepted before feature
freeze only if each implementation is demonstrably small, low-risk, and does not delay
the release-hardening phases. Otherwise they move to the post-release backlog unchanged.

All DEFER entries stay out of the first-release implementation queue unless new evidence
appears from a supported client, documented first-release workflow, or a concrete operator
requirement.
