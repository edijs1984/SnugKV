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

Not supported: WebSocket subscriptions, authentication or rate limiting of clients, and acting as a node.

## Measured

Fake node that answers `getAccountInfo` with a real-shaped token-account response (about 470 bytes of JSON), 64 clients, Zipf-distributed addresses, run on a 2-core sandbox. Numbers are for comparing the two sides of each test, not for absolute capacity.

**Latency and node load** (20 ms node, 20,000 addresses, 300,000 requests, 1 s state TTL): direct to the node p50 21.3 ms at 3.0k requests/s; through the proxy p50 2.3 ms at 9.8k requests/s, 82% hits and 1.7% coalesced, so the node saw 48k calls instead of 300k (84% fewer).

**Cache memory** (90,000 entries held, 1 h TTL), same proxy, two backends:

| Cache | Bytes per entry |
|---|---|
| Redis | 597 |
| SnugKV | 345 (after its optimizer finished, about 40 s) |

Reproduce with `cmd/rpcbench`: `rpcbench upstream` is the fake node, `rpcbench load` the client; `-cache` reports the cache server's memory.
