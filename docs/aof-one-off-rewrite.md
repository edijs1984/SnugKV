# One-off AOF rewrite

When journaling is disabled (`-aof ""`), configure `-aof-rewrite /absolute/path/export.aof` to allow BGREWRITEAOF to create a one-off SnugKV AOF. The equivalent JSON option is `aof_rewrite_path`; the environment option is `SNUGKV_AOF_REWRITE_PATH`.

The parent directory must exist. The destination must differ from the configured active AOF and snapshot paths. The writer holds the persistence file lock through rewrite and close. The generated file contains a reset marker and exported records using SnugKV's native persistence format.

This operation does not install an active journal, enable appendonly, or load the export automatically on startup. Later writes are not appended. An active AOF journal, if configured, continues to use its existing rewrite path instead.

## Audit status

Redis 8.10.2 was observed to allow BGREWRITEAOF with appendonly=no and retain aof_enabled:0 after success. SnugKV requires the explicit export destination above; without either an active journal or that destination it still returns ERR AOF is disabled. This is a remaining configuration compatibility difference.

The command's started reply acknowledges scheduling, not successful completion. INFO persistence now reports aof_enabled, aof_rewrite_in_progress, aof_last_bgrewrite_status, rdb_bgsave_in_progress, rdb_last_save_time, and rdb_last_bgsave_status. Background failures are logged and last results change to err; a successful retry returns the result to ok. The flags describe job state, but INFO may wait behind an active rewrite's durability lock. This is a subset of Redis persistence diagnostics. Active-journal rewrites now hold the server durability lock across export and replacement to prevent intervening writes from being discarded. This blocks serialized commands and participating write paths during file I/O; it is a correctness-first implementation, not Redis-style concurrent background rewriting. A regression test checks the lock and replays writes made before and after replacement. Full persistence compatibility still requires validation.

Regression coverage checks export replay, disabled journaling after rewrite, unchanged file bytes after subsequent SET, invalid destinations, and configured path collisions.
