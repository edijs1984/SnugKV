# Webhooks

An optional module that connects streams to HTTP. It is off unless you start it with a listen flag, and it does not change the data protocol.

- **Inbound webhook.** `POST /hook/<id>` publishes the request into a stream, in the same message format `snugkv-nest` uses (`payload`, `event`, `mid`, `ts`, `producer`, `headers`).
- **Outbound webhook.** A consumer group reads a stream and POSTs each message to a URL, with retries, backoff and a dead-letter stream.

The module connects to the server over loopback like any client. Everything it stores (definitions, delivery logs, counters) is ordinary data, so it is journaled, snapshotted and replicated. Only the primary runs webhooks; on a replica inbound requests get `503` and no worker starts.

## Start it

```bash
snugkv -listen :6379 \
  -webhooks-listen :8090 \                # inbound: POST /hook/<id>
  -webhooks-api-listen 127.0.0.1:8091 \   # management API (token required when not loopback)
  -webhooks-key "$SNUGKV_WEBHOOKS_KEY"    # 16+ characters, encrypts stored secrets
```

| Flag | Environment | Meaning |
|---|---|---|
| `-webhooks-listen` | `SNUGKV_WEBHOOKS_LISTEN` | Address for inbound webhooks. Empty: inbound disabled. |
| `-webhooks-api-listen` | `SNUGKV_WEBHOOKS_API_LISTEN` | Address of the management API. |
| `-webhooks-api-token` | `SNUGKV_WEBHOOKS_API_TOKEN` | Bearer token for the API. Required unless the API listens on loopback. |
| `-webhooks-key` | `SNUGKV_WEBHOOKS_KEY` | Encrypts stored secrets (AES-256-GCM). Without it a webhook cannot hold secrets. Losing the key makes stored secrets unreadable. |
| `-webhooks-auth` | `SNUGKV_WEBHOOKS_AUTH` | `password` or `user:password` the module uses against this server when ACLs are on. |
| `-webhooks-public-url` | `SNUGKV_WEBHOOKS_PUBLIC_URL` | Base URL shown in the API for inbound hooks. |
| `-webhooks-allow-private` | | Let outbound webhooks reach private and loopback addresses. |

Put TLS in front of `-webhooks-listen` (a reverse proxy); the module speaks plain HTTP.

## Management API

All endpoints are JSON; add `Authorization: Bearer <token>` when a token is set.

| Method and path | |
|---|---|
| `GET /v1/status` | Role, whether secrets and inbound are available. |
| `GET /v1/webhooks` | All webhooks with counters and live status. |
| `POST /v1/webhooks` | Create. |
| `GET` / `PUT` / `DELETE /v1/webhooks/{id}` | Read, replace, remove (removing also drops the consumer group). |
| `POST /v1/webhooks/{id}/test` | Send one sample message to an outbound webhook and report the result. |
| `GET /v1/webhooks/{id}/log?limit=50` | Recent deliveries (newest first, last 200 kept). |

Secrets are never returned: only `set` and a hint (`••••` plus the last four characters). On `PUT`, a secret you leave out is kept; send a new value to replace it.

### Inbound

```json
{
  "name": "github", "kind": "inbound", "topic": "github.events", "enabled": true,
  "inbound": {
    "auth": { "mode": "hmac", "header": "X-Hub-Signature-256", "algo": "sha256", "prefix": "sha256=", "secret": { "value": "…" } },
    "eventHeader": "X-GitHub-Event", "maxPerSecond": 100, "maxLen": 100000
  }
}
```

- `auth.mode`: `none` (the unguessable id is the only protection), `token` (header equals the secret) or `hmac` (hex HMAC of the raw body, `sha256` or `sha1`).
- A JSON body becomes `payload` as is; any other body is stored as a JSON string. Bodies up to 1 MiB. Requests above `maxPerSecond` (default 100, burst 2×) get `429`.
- Request headers are kept in `headers` when they are `Content-Type`, `User-Agent` or `X-*`, and not credential-like. The auth header is never kept.
- Answers `202 {"id": "<stream id>", "mid": "…"}`.

### Outbound

```json
{
  "name": "crm", "kind": "outbound", "topic": "orders.created", "enabled": true,
  "outbound": {
    "url": "https://crm.example.com/hooks/orders",
    "headers": [ { "name": "X-Api-Key", "value": "…", "secret": true } ],
    "signingSecret": { "value": "…" },
    "from": "$", "maxAttempts": 5, "timeoutMs": 10000, "body": "envelope"
  }
}
```

- Delivery is at least once and in order, one message at a time per webhook. The consumer group is `webhook:<id>` unless `group` is set. `from: "$"` starts with new messages, `"0"` with the whole stream.
- Success is any `2xx`. `408`, `425`, `429`, `5xx` and network errors are retried with exponential backoff (1 s up to 60 s, jittered; `Retry-After` is honored up to 2 min). Other statuses fail at once. After `maxAttempts` the message goes to `<topic>.dlq` with `dlq_error`, `dlq_webhook` and `dlq_source_id` fields, and is acknowledged. A slow or failing endpoint holds up its own webhook only.
- A restart or an edit never loses a message: unacknowledged messages are delivered again.
- Body: `envelope` (`id`, `topic`, `mid`, `event`, `ts`, `producer`, `headers`, `payload`) or `payload` (only the payload).
- Headers sent: your custom ones, plus `X-Snugkv-Webhook-Id`, `X-Snugkv-Message-Id` (use it to de-duplicate), `X-Snugkv-Delivery-Attempt`, `X-Snugkv-Timestamp`, `X-Snugkv-Event`, and, with a signing secret, `X-Snugkv-Signature: sha256=hex(HMAC-SHA256(secret, timestamp + "." + body))`. Receivers should recompute it and reject old timestamps.

### Safety

- Outbound requests to loopback, private, link-local (cloud metadata), carrier-grade NAT and multicast addresses are refused. The check runs on the address actually connected to, so DNS tricks do not get around it. Redirects are not followed. Use `-webhooks-allow-private` for internal receivers.
- Header names that SnugKV sets itself (`Host`, `Content-Length`, `X-Snugkv-*`) cannot be overridden.

## Stored keys

`snugkv:webhook:ids` (set), `snugkv:webhook:def:<id>` (JSON, secrets encrypted), `snugkv:webhook:log:<id>` (list), `snugkv:webhook:stats:<id>` (hash). None of them is a stream, so they do not show up as topics.
