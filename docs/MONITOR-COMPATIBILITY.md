# MONITOR — initial implementation

Branch: feat/redis82-monitor, based on main after PR #202.

The captured Redis 8.10.2 oracle emitted PING, SET, GET, MULTI, queued SET/GET at EXEC time, EXEC, then DEL. Each line contains a microsecond timestamp, DB 0, peer address, and quoted arguments. Redis 8.2 source was inspected for byte escaping and administrative command exclusion.

This first slice adds ACL-authorized TCP subscription, serialized output, idle subscription lifetime, shared server subscriptions, ordinary/fast TCP GET and SET reporting, and transaction replay events. Authentication, configuration, ACL, migration and administrative commands are conservatively excluded. Binary bytes are escaped.

Backpressure is bounded to 128 queued events per subscriber; slow subscribers are disconnected. Commands with more than 64 KiB total argument bytes disconnect monitors rather than allocate an unbounded event. These limits differ from Redis configurable output-buffer policies.

## Pending audit

- Nested Lua/Function events and lua source labels.
- Exact skip-monitor/admin command filtering and invalid-command cases.
- Commands issued by monitor clients, RESET/QUIT behavior, and CLIENT LIST monitor classification.
- Complete COMMAND flags and RESP3 behavior.
- Concurrency ordering across clients, blocking command timing, and all alternate execution routes.
- Subscription teardown/backpressure stress and output-size policy.

These are remaining implementation/audit tasks, not claims of full MONITOR compatibility. Go tests were added but not run in the assistant environment; operator execution is required.
