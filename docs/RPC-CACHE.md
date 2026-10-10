# rpccache: a caching JSON-RPC proxy for Solana and EVM

`rpccache` sits between your application and an RPC provider (Helius, Quicknode, Alchemy, your own node). It answers repeated reads from a Redis-protocol cache and forwards everything else unchanged. The cache can be SnugKV or Redis; SnugKV holds the same entries in less memory.

```
app  ->  rpccache  ->  upstream RPC node
            |
        SnugKV / Redis
```

## Run it

```
snugkv -listen 127.0.0.1:6379 -encoding -compression -json-shape \
       -max-memory 2147483648 -eviction-policy allkeys-lru

rpccache -listen 127.0.0.1:8899 \
         -upstream https://mainnet.example.com/KEY \
         -cache 127.0.0.1:6379 -chain solana
```

Point the application at `http://127.0.0.1:8899` instead of the provider. Use `-chain evm` for Ethereum-style nodes. The upstream API key is usually in the URL; it is not logged (the startup line prints only the host). Extra headers: `-upstream-header "x-api-key: ..."`.

Endpoints: `POST /` (JSON-RPC, single or batch), `GET /healthz`, `GET /metrics`.

Every reply carries `X-Cache: HIT | MISS | COALESCED | BYPASS` (batches: `BATCH hit=1 miss=2 bypass=0`).

## What is cached, and for how long

| Class | Default TTL | Solana | EVM |
|---|---|---|---|
| immutable | 24 h | `getTransaction`, `getBlock` at `finalized` (the default) | |
| static | 5 min | `getGenesisHash`, `getEpochSchedule`, `getMinimumBalanceForRentExemption`, `getVersion` | `eth_chainId`, `net_version` |
| recent | 15 s | | results for a block number, block hash or `finalized`; receipts and transactions by hash |
| state | 1 s | `getAccountInfo`, `getBalance`, `getMultipleAccounts`, token and program account reads, `getSignaturesForAddress`, `getSignatureStatuses` | reads at `latest` |
| tip | 200 ms | `getSlot`, `getBlockHeight`, `getLatestBlockhash`, `getEpochInfo` | `eth_blockNumber`, `eth_gasPrice` |

Not cached, forwarded as they came: `sendTransaction`, `simulateTransaction`, `requestAirdrop`, `eth_sendRawTransaction`, `eth_estimateGas`, `pending` state, any request with `minContextSlot`, any method the table does not list, and anything the proxy cannot parse. Errors, `null` results and non-200 replies are never cached.

TTLs are flags: `-ttl-immutable`, `-ttl-static`, `-ttl-recent`, `-ttl-state`, `-ttl-tip`.

## What you give up

A cached answer can be older than the chain head by up to its TTL. With the defaults a Solana account read is at most 1 second (about 2 or 3 slots) old, and its `context.slot` says which slot it describes. If a caller needs fresher data it should pass `minContextSlot`, which always goes to the node. Set `-ttl-state` lower (or 0 to disable that class) if 1 second is too stale for your application.

## How it behaves under load

- **Coalescing.** Identical requests that arrive while the first is still at the node wait for it and share one upstream call.
- **Batches.** A JSON-RPC batch is answered element by element: cached elements locally, the rest as individual upstream calls (so a batch can cost more upstream calls than one batched request, never fewer than the misses).
- **A broken cache never breaks the proxy.** If the cache server is down or slow, requests are served from the node and the cache is skipped for a second at a time. `rpccache_cache_errors_total` counts it.
- **Size limit.** Results above `-max-entry-bytes` (1 MiB) are served but not stored; `getProgramAccounts` on large programs is the usual case.
- Cache keys are 17 bytes: a tag plus 128 bits of a SHA-256 over the chain, method and whitespace-normalized params.

Not supported: WebSocket subscriptions and acting as a node. Client authentication and limits are optional, see below.

## API keys, plans and metering (`-auth`)

With `-auth`, every JSON-RPC request needs an API key, checked against records kept in SnugKV. Without the flag nothing changes.

```
snugkv  -listen 127.0.0.1:6379 ...                    # holds keys, plans, limits, usage
rpccache -auth -cache 127.0.0.1:6379 -upstream ... -chain solana

rpckeys plan set free -rps 10 -burst 20 -daily 100000
rpckeys key create -label acme -plan free             # prints the secret once
rpckeys usage <id> -days 7
rpckeys key revoke <id>
```

A client sends the key as `Authorization: Bearer <key>`, `X-API-Key: <key>` or `?api-key=<key>`. `/healthz` and `/metrics` stay open.

- **Plans** hold `rps` (sustained calls per second), `burst` (bucket size, and the largest batch) and `daily` (calls per UTC day, 0 for unlimited). A batch of n calls costs n. Rate limiting uses SnugKV's `snug_rate_limit`, so the limit is shared by every `rpccache` pointing at the same store. The store must be SnugKV; use `-auth-store` to point at a different server than the cache.
- **Answers.** No or unknown or revoked key: 401. Key on a missing or invalid plan: 403. Over the rate, the batch size or the daily quota: 429 with `Retry-After` (seconds; to the next UTC midnight for the daily quota). Errors are JSON-RPC bodies with codes -32001 and -32005.
- **Metering.** Each admitted call is counted per key per UTC day in `calls`, split into `immutable`, `static`, `recent`, `state`, `tip` and `bypass` by the method table; refused calls go to `denied`. Counts are buffered and written once a second, and kept 45 days. `rpckeys usage` prints them. The numbers are calls, not cache hits: a cached call counts like any other.
- **Secrets.** The key is `rk_` plus 256 random bits. Only the first 128 bits of its SHA-256 are stored (as the key id); the secret cannot be recovered or listed.
- **Staleness.** Key and plan records are trusted for `-auth-record-ttl` (5 s), so a revocation or plan change applies within that time. The daily quota is checked against the stored count plus what this process has admitted since it last read it, so with several `rpccache` processes it can overshoot by about what the others admit in one TTL.
- **When the store is down.** A key already seen keeps working on its last record. A key never seen is refused with 503. Rate limiting and usage counting fail open (calls are allowed, and buffered usage is retried), so an outage costs accuracy, not availability. `rpcauth_store_errors_total` counts it.
- **Cost.** About 36 µs per request in the sandbox (a store round trip for the rate limit, with the store on the same two cores), so a cached request costs roughly seven times the bare proxy there. Measure on your own hardware with `go test ./internal/rpccache -bench AuthOverhead`.
- **Not included:** billing, a customer portal, per-method pricing weights, IP allow-lists, and WebSocket authentication. Keys are created by an operator with `rpckeys`.

## Measured

Fake node that answers `getAccountInfo` with a real-shaped token-account response (about 470 bytes of JSON), 64 clients, Zipf-distributed addresses, run on a 2-core sandbox. Numbers are for comparing the two sides of each test, not for absolute capacity.

**Latency and node load** (20 ms node, 20,000 addresses, 300,000 requests, 1 s state TTL): direct to the node p50 21.3 ms at 3.0k requests/s; through the proxy p50 2.3 ms at 9.8k requests/s, 82% hits and 1.7% coalesced, so the node saw 48k calls instead of 300k (84% fewer).

**Cache memory** (90,000 entries held, 1 h TTL), same proxy, two backends:

| Cache | Bytes per entry |
|---|---|
| Redis | 597 |
| SnugKV | 345 (after its optimizer finished, about 40 s) |

Reproduce with `cmd/rpcbench`: `rpcbench upstream` is the fake node, `rpcbench load` the client; `-cache` reports the cache server's memory.

## Wallet simulation

`rpcbench wallets` models users of a wallet or dashboard. Each user watches 15 accounts (half of them from a shared pool of 200 popular accounts, Zipf-distributed) and refreshes every 3 s with `getSlot`, one `getMultipleAccounts` for the whole set, `getBalance` for the owner and five `getAccountInfo` reads of popular accounts. With `-direct` it repeats a sample of the popular reads against the node to measure how stale cached answers are, and with `-keys` and `-store` it compares each key's usage in SnugKV with what the client sent. Start the fake node with `-advance` so the slot moves and some accounts change (a tenth every 2 slots, a fifth every 150, the rest never).

```
rpcbench upstream -listen 127.0.0.1:9100 -advance
rpccache -auth -upstream http://127.0.0.1:9100 -cache 127.0.0.1:6383 -listen 127.0.0.1:8899
rpcbench wallets -url http://127.0.0.1:8899 -direct http://127.0.0.1:9100 \
         -upstream-stats http://127.0.0.1:9100 -users 100 -duration 30s \
         -keys KEY1,KEY2 -store 127.0.0.1:6383
```

Result on the fake node, 100 users, 30 s, one key on a generous plan and one on a tight plan (default 1 s state TTL):

| Measure | Result |
|---|---|
| Calls the cache answered without the node | 48.9% overall |
| `getAccountInfo` of popular accounts | 62% hit or coalesced |
| `getSlot` | 78% |
| `getMultipleAccounts`, `getBalance` | 0% (every user's set and owner is unique) |
| Cached answers that differed from the node | 1.5% of 404 checked, 2 slots behind at the median, 3 at most |
| Generous key: calls sent vs counted | 3,520 vs 3,520 |
| Tight key (1 call/s, burst 10, 400/day): admitted | 39 of 3,648 requests; counts also matched |

These come from a generated workload, so the hit rates only describe the pattern above. What they show is where the cache helps: reads many users share. Per-user reads do not hit, and a `getMultipleAccounts` with a different set of accounts is a different cache entry even when most accounts are shared.
