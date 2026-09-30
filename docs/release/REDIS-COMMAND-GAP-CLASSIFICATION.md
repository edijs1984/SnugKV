# Redis 8.2 Command Gap Classification

> Classification of the 189 Redis-only inventory entries discovered by the A2 live-oracle comparison.
> This is an A2 compatibility classification, not the final Phase A4 implementation priority.

## Summary

- Already implemented, metadata-only gaps: **49**
- Intentionally unsupported for first-release architecture: **10**
- Candidate gaps requiring client/workflow evidence: **58**
- Redis internal/debug/module-specific and irrelevant: **72**

## Classification

| Command | A2 classification | Reason |
|---|---|---|
| `ACL|CAT` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|DELUSER` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|DRYRUN` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|GENPASS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|GETUSER` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|HELP` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|LIST` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|LOAD` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|LOG` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|SAVE` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|SETUSER` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|USERS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `ACL|WHOAMI` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `BF.DEBUG` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `CF.COMPACT` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CF.DEBUG` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `CLIENT|NO-EVICT` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLIENT|NO-TOUCH` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLIENT|PAUSE` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLIENT|REPLY` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLIENT|TRACKINGINFO` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLIENT|UNPAUSE` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|ADDSLOTS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|ADDSLOTSRANGE` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|BUMPEPOCH` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|COUNT-FAILURE-REPORTS` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|COUNTKEYSINSLOT` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|DELSLOTS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|DELSLOTSRANGE` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|FAILOVER` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|FLUSHSLOTS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|FORGET` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|GETKEYSINSLOT` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|HELP` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|INFO` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|KEYSLOT` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|LINKS` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|MEET` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|MYID` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|MYSHARDID` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|NODES` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|REPLICAS` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|REPLICATE` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|RESET` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|SAVECONFIG` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|SET-CONFIG-EPOCH` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|SETSLOT` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|SHARDS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `CLUSTER|SLAVES` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|SLOT-STATS` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `CLUSTER|SLOTS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `COMMAND|HELP` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `COMMAND|LIST` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `DEBUG` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `FAILOVER` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `FT.ADD` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `FT.DEL` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `FT.DROP` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `FT.GET` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `FT.MGET` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `FT.SYNADD` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `FT._ALIASADDIFNX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `FT._ALIASDELIFX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `FT._ALTERIFNX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `FT._CREATEIFNX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `FT._DROPIFX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `FT._DROPINDEXIFX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `GEORADIUSBYMEMBER_RO` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `GEORADIUS_RO` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `JSON.NUMPOWBY` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `LATENCY` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `LATENCY|DOCTOR` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `LATENCY|GRAPH` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `LATENCY|HELP` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `LATENCY|HISTOGRAM` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `LATENCY|HISTORY` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `LATENCY|LATEST` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `LATENCY|RESET` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `LOLWUT` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `MEMORY|DOCTOR` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `MEMORY|HELP` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `MEMORY|MALLOC-STATS` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `MEMORY|PURGE` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `MEMORY|STATS` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `MEMORY|USAGE` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `MODULE` | **INTENTIONALLY_UNSUPPORTED** | SnugKV implements extended features natively and does not load Redis modules. |
| `MODULE|HELP` | **INTENTIONALLY_UNSUPPORTED** | SnugKV implements extended features natively and does not load Redis modules. |
| `MODULE|LIST` | **INTENTIONALLY_UNSUPPORTED** | SnugKV implements extended features natively and does not load Redis modules. |
| `MODULE|LOAD` | **INTENTIONALLY_UNSUPPORTED** | SnugKV implements extended features natively and does not load Redis modules. |
| `MODULE|LOADEX` | **INTENTIONALLY_UNSUPPORTED** | SnugKV implements extended features natively and does not load Redis modules. |
| `MODULE|UNLOAD` | **INTENTIONALLY_UNSUPPORTED** | SnugKV implements extended features natively and does not load Redis modules. |
| `MOVE` | **INTENTIONALLY_UNSUPPORTED** | First release intentionally supports DB 0 only. |
| `OBJECT|ENCODING` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `OBJECT|FREQ` | **INTENTIONALLY_UNSUPPORTED** | Requires Redis-style object access metadata that SnugKV intentionally does not currently track. |
| `OBJECT|HELP` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `OBJECT|IDLETIME` | **INTENTIONALLY_UNSUPPORTED** | Requires Redis-style object access metadata that SnugKV intentionally does not currently track. |
| `OBJECT|REFCOUNT` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `PFDEBUG` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `PFSELFTEST` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `PUBSUB|CHANNELS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `PUBSUB|HELP` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `PUBSUB|NUMPAT` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `PUBSUB|NUMSUB` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `PUBSUB|SHARDCHANNELS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `PUBSUB|SHARDNUMSUB` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `READONLY` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `READWRITE` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `REPLCONF` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `SCRIPT|HELP` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `SEARCH.CLUSTERINFO` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `SEARCH.CLUSTERREFRESH` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `SEARCH.CLUSTERSET` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `SHUTDOWN` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `SLAVEOF` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `SLOWLOG|GET` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `SLOWLOG|HELP` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `SLOWLOG|LEN` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `SLOWLOG|RESET` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `SUBSTR` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `SWAPDB` | **INTENTIONALLY_UNSUPPORTED** | First release intentionally supports DB 0 only. |
| `SYNC` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `TIMESERIES.CLUSTERSET` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `TIMESERIES.REFRESHCLUSTER` | **IRRELEVANT** | Debug/internal/module-coordination surface does not define a first-release SnugKV workflow. |
| `XGROUP|CREATE` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `XGROUP|CREATECONSUMER` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `XGROUP|DELCONSUMER` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `XGROUP|DESTROY` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `XGROUP|HELP` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `XGROUP|SETID` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `XINFO|CONSUMERS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `XINFO|GROUPS` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `XINFO|HELP` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `XINFO|STREAM` | **IMPLEMENTED** | Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage. |
| `XSETID` | **CANDIDATE_GAP** | Real Redis surface not yet proven necessary; validate against first-release workflows and client traces. |
| `_FT.CONFIG` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|BG_SCAN_CONTROLLER` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|CLEAR_PENDING_TOPOLOGY` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|COORD_THREADS` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DELETE_LOCAL_CURSORS` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DOCIDTOID` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DOCINFO` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_GEOMIDX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_HNSW` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_INVIDX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_NUMIDX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_NUMIDXTREE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_PHONETIC_HASH` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_PREFIX_TRIE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_SUFFIX_TRIE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_TAGIDX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|DUMP_TERMS` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|FT.AGGREGATE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|FT.PROFILE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|FT.SEARCH` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|GC_CLEAN_NUMERIC` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|GC_CONTINUE_SCHEDULE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|GC_FORCEBGINVOKE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|GC_FORCEINVOKE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|GC_STOP_SCHEDULE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|GC_WAIT_FOR_JOBS` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|GET_HIDE_USER_DATA_FROM_LOGS` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|GIT_SHA` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|HELP` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|IDTODOCID` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|INDEXER_SLEEP_BEFORE_YIELD_MICROS` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|INDEXES` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|INFO` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|INFO_TAGIDX` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|INVIDX_SUMMARY` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|NUMIDX_SUMMARY` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|PAUSE_TOPOLOGY_UPDATER` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|QUERY_CONTROLLER` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|RESUME_TOPOLOGY_UPDATER` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|SET_MAX_INDEXES` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|SET_MONITOR_EXPIRATION` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|SHARD_CONNECTION_STATES` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|SPEC_INVIDXES_INFO` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|TTL` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|TTL_EXPIRE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|TTL_PAUSE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|VECSIM_INFO` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|VECSIM_MOCK_TIMEOUT` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|WORKERS` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|YIELDS_COUNTER` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|_FT.AGGREGATE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|_FT.PROFILE` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.DEBUG|_FT.SEARCH` | **IRRELEVANT** | Redis Search internal/private command surface. |
| `_FT.SAFEADD` | **IRRELEVANT** | Redis Search internal/private command surface. |

## Interpretation

- IMPLEMENTED entries are not feature work; they expose structured COMMAND metadata completeness gaps.
- INTENTIONALLY_UNSUPPORTED entries conflict with a deliberate first-release architecture boundary.
- CANDIDATE_GAP entries feed Phase A3 client/workflow tracing and are not automatically release requirements.
- IRRELEVANT entries are Redis diagnostics, private Search commands, or module-coordination surfaces without a SnugKV first-release use case.

Phase A4 converts only evidence-backed candidate gaps into REQUIRED / USEFUL / DEFER implementation decisions.
