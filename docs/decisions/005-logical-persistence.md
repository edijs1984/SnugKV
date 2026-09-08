# ADR-005: Logical persistence framing

Status: Accepted

Persist exact logical key/value bytes and absolute expiration milliseconds using
base64 JSON fields inside length-prefixed CRC32 frames. The file magic MCLOG001
versions the format. An AOF frame contains the entire affected command batch, so
MSET recovery is atomic. Codec choices are recomputed at recovery; internal
representation changes never generate logical journal entries.

Durable server commands are serialized. A command's prior logical records are
captured, the command executes, and its resulting logical state is appended before
acknowledgment. Append failure restores prior records and disables subsequent
writes until restart. This transaction boundary applies to this server; concurrent
embedded calls bypass it and are not supported for a journal-backed instance.

A truncated final AOF frame is ignored and truncated before new appends. Bad
checksums, unknown versions and invalid JSON fail recovery. Fsync policies are
always, everysec and no; always synchronizes before acknowledgment. Background
sync failure poisons the writer. Snapshots use a temporary file, fsync, rename and
directory fsync. Snapshot recovery precedes AOF recovery.

Full AOF replay is logically idempotent even over a newer snapshot because frames
contain final values/deletions, not increments. Both files must belong to the same
unpruned AOF history. AOF rewrite needs an explicit sequence/checkpoint protocol
before log truncation or replacement can be safely supported with snapshots.
