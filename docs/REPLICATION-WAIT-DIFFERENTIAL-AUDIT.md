# WAIT Differential Audit

Date: 2026-09-24

## Scope

This audit covers Redis-compatible `WAIT <numreplicas> <timeout>` semantics in SnugKV.

## Implementation

SnugKV tracks the replication offset of the most recent successful write for each client connection.

`WAIT` uses that client-local write offset rather than the primary's latest global offset.

Normal `WAIT`:

1. counts connected replicas whose ACK offset is at or beyond the client's target offset
2. if the requested count is not already satisfied, sends `REPLCONF GETACK *` to connected replicas
3. waits for ACK progress, timeout, client disconnect, shutdown, or CLIENT UNBLOCK
4. returns the number of replicas known to have acknowledged the target offset

`WAIT` does not hold `durableMu` while blocked.

## MULTI / EXEC

Redis does not block for `WAIT` executed from inside `MULTI/EXEC`.

SnugKV matches this behavior:

- the queued `WAIT` uses the client's write offset from before `EXEC`
- it does not send a fresh GETACK request
- it returns the replica ACK state already known when it executes

The transaction's own writes update the connection's last replication offset only after the transaction finishes replication.

## Argument behavior

Live Redis 8.10.x oracle and SnugKV matched for:

```
WAIT 0 0
WAIT -1 100
WAIT 1 -1
WAIT foo 100
WAIT 1 foo
```

Observed behavior:

```
WAIT 0 0
(integer) 1

WAIT -1 100
(integer) 1

WAIT 1 -1
(error) ERR timeout is negative

WAIT foo 100
(error) ERR value is not an integer or out of range

WAIT 1 foo
(error) ERR timeout is not an integer or out of range
```

Negative `numreplicas` is accepted by Redis and SnugKV. A negative timeout is rejected.

## Live Snug -> Snug validation

Topology:

- Snug primary: 127.0.0.1:6391
- Snug replica: 127.0.0.1:6392

Successful acknowledgement:

```
SET wait:key one
WAIT 1 1000
```

Result:

```
OK
(integer) 1
```

Replica contained:

```
wait:key = one
```

Insufficient replica count:

```
SET wait:timeout test
WAIT 2 200
```

Result:

```
OK
(integer) 1
```

Wall time was approximately 216 ms.

## Live Redis oracle

Topology:

- Redis primary: 127.0.0.1:6398
- SnugKV replica: 127.0.0.1:6395

Redis:

```
SET wait:redis:a one
WAIT 1 1000
```

returned:

```
OK
(integer) 1
```

The SnugKV replica received the write.

For:

```
SET wait:redis:timeout test
WAIT 2 200
```

Redis returned one acknowledged replica after approximately 221 ms.

## MULTI differential

Redis 8.10.x:

```
SET wait:redis:before before
MULTI
WAIT 1 10000
SET wait:redis:inside inside
EXEC
```

returned:

```
OK
OK
QUEUED
QUEUED
1) (integer) 0
2) OK
```

SnugKV produced the same result:

```
OK
OK
QUEUED
QUEUED
1) (integer) 0
2) OK
```

The transaction did not block for 10 seconds.

## Tests

Focused tests cover:

- immediate acknowledged return
- timeout return count
- wake on ACK
- cancellation
- Redis-compatible argument validation
- non-blocking WAIT inside EXEC
- command metadata
- ACK monotonicity
- Redis stream-mode recovery regression

