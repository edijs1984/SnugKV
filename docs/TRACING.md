# Flow tracing (`snug:traces`)

SnugKV carries flow traces for the services that use it: one trace per
activity (an HTTP request, a webhook, a scheduled job), one span per step,
across every service the activity reaches. The server needs nothing special;
traces are ordinary stream entries. Two companion tools use them:

- **[snugkv-nest](https://github.com/edijs1984/snugkv-nest)** (0.4+) writes
  spans with `tracing: true` on `SnugKvModule`, and carries the trace through
  job queues (`snugq`) and stream messages so the next service continues it.
- **[snugkv-stream-ui](https://github.com/edijs1984/snugkv-stream-ui)** shows
  them on its Traces page: a searchable list of flows and a waterfall of the
  steps, optionally kept in MongoDB for a retention period.

This page describes the format both sides agree on, so other clients and
tools can write or read traces too.

## The stream

Spans are appended to the stream `snug:traces` (configurable on both sides),
capped with `XADD … MAXLEN ~ 100000 *`. Writers batch spans and send them in a
pipeline. One entry is one finished span:

| Field | Meaning |
| --- | --- |
| `traceId` | 32 hex characters, shared by every span of the flow |
| `spanId` | 16 hex characters |
| `parentId` | `spanId` of the step that started this one; empty for the first step |
| `service` | name of the app that recorded it (`api`, `ledger`, …) |
| `name` | step name (`POST /api/tx/:id/send`, `chain-events:deposit.detected`, `mongo find wallets`) |
| `status` | `ok` or `error` |
| `start` | start time, ms since the epoch |
| `durationMs` | duration in ms |
| `attrs` | JSON object of attributes (bounded; secret-looking keys redacted) |
| `error` | error message (failed steps only) |
| `stack` | stack trace (failed steps only) |

A span is written when it ends, so a parent usually arrives after its
children. Readers group by `traceId` and order by `start`.

## Carrying a trace between services

- **Job queues (`snugq_add`).** When the job data is a JSON object, the trace
  travels in a reserved field: `"__trace": {"traceId": "…", "spanId": "…"}`.
  snugkv-nest removes it before the processor sees `job.data`. Other data
  types are not traced across the hop.
- **Stream messages.** The message `headers` JSON gets
  `"x-snug-trace": "<traceId>-<spanId>"` (same envelope as in
  [WEBHOOKS.md](WEBHOOKS.md)). Consumers continue that trace.
- **HTTP.** snugkv-nest returns the trace id of every traced request in the
  `x-trace-id` response header.

## Reading traces

- Without a database, read the newest entries with `XREVRANGE snug:traces + -
  COUNT n` and group them.
- For longer retention, read through a consumer group (snugkv-stream-ui uses
  `snugkv-stream-ui`, starting from `0`), store the spans elsewhere and `XACK`
  them. The stream cap means unread spans are eventually trimmed.

## Privacy

Writers must not put credentials or personal data into spans. snugkv-nest
redacts attribute keys that look like secrets (`token`, `password`,
`signature`, `privateKey`, `seed`, `apiKey`, …), drops query strings from
outgoing URLs, masks long key-like path segments, and never records request
bodies or headers. Treat the stream as operational data with the same access
rules as the rest of the server (ACLs can restrict `snug:traces`).

## Clearing

```bash
redis-cli -p 6380 DEL snug:traces
```

The writers recreate the stream on the next span; a consumer group reading it
must be recreated (snugkv-stream-ui does that by itself).
