# RESP2 protocol

TCP requests are nonempty flat RESP2 arrays of non-null bulk strings. Binary and
empty values, partial TCP frames, and pipelined commands are supported. Inline
commands, nested arrays, null arguments, simple strings, integers, and error frames
are rejected as requests.

Default limits are 64 MiB per command, 32 MiB per bulk string, 1,024 arguments,
10,000 concurrent clients, and 30 seconds per complete read or write. They are
configured before startup. Payload storage grows as bytes arrive after validating
the declared length. Responses are written synchronously, so there is no unbounded
output queue; partial writes are retried.

Malformed protocol receives `-ERR invalid RESP` and the connection closes. Command
errors preserve the connection. Error framing strips carriage returns and newlines.
`QUIT` returns `+OK` and closes the connection.

Commands run sequentially per connection. Per-key mutations are atomic. MSET,
MGET, DEL, and EXISTS lock shards in ascending order and provide an atomic
cross-shard view. Engine locks are released before network responses are written.

Administrative commands use the separate loopback admin listener when configured.
That listener rejects data mutations; the public listener rejects `SNUG.*` while
the admin listener is active.
