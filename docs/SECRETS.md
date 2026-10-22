# Secrets

An optional secrets manager: environment variables for your services, grouped by project and environment (`airsig/prod`, `airsig/dev`), encrypted before they are stored, versioned, and with an audit log of every read and change. It is off unless you start it with `-secrets-api-listen`, and it does not change the data protocol.

The module connects to the server over its own connection like any client, so everything it stores is ordinary data: journaled, snapshotted and replicated. Values are encrypted with AES-256-GCM inside the module, so the keys, the AOF, snapshots and replicas only ever hold ciphertext. Each value is bound to its environment and name: ciphertext copied to another secret does not decrypt.

## Start it

```bash
snugkv -listen :6379 \
  -secrets-api-listen 127.0.0.1:8092 \        # the API (an admin token is required when not loopback)
  -secrets-key "$SNUGKV_SECRETS_KEY" \         # 16+ characters, encrypts the values
  -secrets-api-token "$SNUGKV_SECRETS_API_TOKEN"
```

| Flag | Environment | Meaning |
|---|---|---|
| `-secrets-api-listen` | `SNUGKV_SECRETS_API_LISTEN` | Address of the API. Turns the module on. |
| `-secrets-api-token` | `SNUGKV_SECRETS_API_TOKEN` | The first admin token (24+ characters, e.g. `openssl rand -hex 24`). Required unless the API listens on loopback. |
| `-secrets-key` | `SNUGKV_SECRETS_KEY` | Encrypts the values. Required. **Losing it makes the stored secrets unreadable**; keep a copy outside SnugKV. |
| `-secrets-old-key` | `SNUGKV_SECRETS_OLD_KEY` | The previous key during a key rotation (see below). |
| `-secrets-auth` | `SNUGKV_SECRETS_AUTH` | `password` or `user:password` the module uses against this server when ACLs are on. |

Starting with a different key than the one the data was written with fails at once ("the secrets key does not match"), instead of on the first read.

Put TLS in front of the API when it is reached over a network; the module speaks plain HTTP.

## Who can do what

- **The first admin token** (`-secrets-api-token`): everything below. Use it to create named admin tokens, then keep it for emergencies.
- **Admin tokens** (`ska_…`), created through the API or the console: everything the first one can, under their own name in the audit log (`admin:<name>`), and each can be revoked on its own. One per person or tool (the console, CI).
- **Read tokens** (`sks_…`): read the secrets of the environments they list (`airsig/prod`, or `airsig/*` for every environment of a project), nothing else.
- Created tokens can expire (`ttlDays`). Only a hash is stored; a token is shown once. The listing shows when each was last used. Revoking works at once.

Every read (one value, a reveal, an export) and every change is written to the `snugkv:secrets:audit` stream with who did it: `admin`, `admin:<name>` when a tool in front says who acts (the console sends its basic-auth user), or `token:<name>`.

Everything lives under `snugkv:secrets:*`. With ACLs on, give the module its own user with access to that prefix and keep other users away from it; they would only see ciphertext, but they could delete it.

## Changing the key

1. Restart SnugKV with the new key and the old one:
   `SNUGKV_SECRETS_KEY=<new> SNUGKV_SECRETS_OLD_KEY=<old>`. At start the module re-encrypts every value and every history entry still sealed with the old key, and logs how many. If it stops halfway, the next start with both keys continues.
2. Restart with only the new key. Starting with the old key now fails.

On replicas, set both keys too until the primary has rotated (they read values sealed with either key and leave the rewriting to the primary). The audit log and tokens are not encrypted with the key, so they are unaffected; tokens keep working.

## Using the secrets

Any process, any language: the `snugkv-secrets` binary (in the SnugKV image at `/snugkv-secrets`) starts a command with the secrets as environment variables:

```dockerfile
COPY --from=your-snugkv-image /snugkv-secrets /usr/local/bin/snugkv-secrets
ENTRYPOINT ["snugkv-secrets", "run", "--env", "airsig/prod", "--"]
CMD ["node", "dist/main.js"]
```

```yaml
environment:
  SNUGKV_SECRETS_URL: http://snugkv:8092
  SNUGKV_SECRETS_TOKEN: ${AIRSIG_SECRETS_TOKEN}   # a read token
```

Variables already set win, so a value in docker-compose still overrides a secret (`--override` lets the secrets win). The token is removed from the command's environment, signals are passed on and the command's exit code is returned. It retries for `--wait` (default 30 s) while the API is starting. `snugkv-secrets export --env airsig/prod > .env` and `snugkv-secrets get --env airsig/prod NAME` read without running anything.

NestJS: `await loadSecrets({ env: 'airsig/prod' })` from `snugkv-nest` before `NestFactory.create` (see its README).

The SnugKV console (`snugkv-stream-ui --secrets-url …`) has a Secrets page for all of this.

## API

All paths take `Authorization: Bearer <token>`. Answers are JSON with `Cache-Control: no-store`. `{project}` and `{env}` use `a-z 0-9 - _`; secret names look like environment variables (`[A-Za-z_][A-Za-z0-9_]*`).

| Method and path | Who | Does |
|---|---|---|
| `GET /v1/status` | any | `ready`, `primary`; for a read token its name and environments |
| `GET /v1/envs` | admin | environments with secret counts |
| `PUT /v1/envs/{project}/{env}` | admin | create (idempotent) |
| `DELETE /v1/envs/{project}/{env}` | admin | delete with every secret and its history |
| `GET /v1/envs/{project}/{env}/secrets` | admin | names, versions, a hint (`••••` + last 4 characters of values of 12+ characters); `?reveal=1` adds values |
| `GET /v1/envs/{project}/{env}/secrets/{name}` | admin, read token | one secret with its value |
| `PUT /v1/envs/{project}/{env}/secrets/{name}` | admin | `{"value": "…"}`; creates the environment if needed; the same value again makes no new version |
| `DELETE /v1/envs/{project}/{env}/secrets/{name}` | admin | delete (the history stays; version numbers never repeat) |
| `GET …/secrets/{name}/versions` | admin | the last 20 versions, newest first, with hints |
| `POST …/secrets/{name}/rollback` | admin | `{"version": n}` makes that value current again, as a new version |
| `POST /v1/envs/{project}/{env}/import` | admin | `{"dotenv": "…", "overwrite": false}` or `{"values": {…}}`; answers `created`, `updated`, `unchanged`, `skipped` |
| `GET /v1/envs/{project}/{env}/export` | admin, read token | `{"values": {…}}`; `?format=dotenv` for a `.env` file |
| `GET /v1/audit?env=&limit=` | admin | newest first (the last 5,000 are kept) |
| `GET /v1/tokens` · `POST /v1/tokens` · `DELETE /v1/tokens/{id}` | admin | list; create a read token `{"name": "…", "envs": ["airsig/prod"], "ttlDays": 90}` or an admin token `{"name": "…", "admin": true}` (answers the token once); revoke |

`.env` import understands comments, `export `, unquoted values (a ` #` starts a comment), `'single quoted'` (literal) and `"double quoted"` values with `\n \t \" \\ \$` escapes and line breaks. Export writes values that import back unchanged.

Limits: 200 environments, 1,000 secrets per environment, 64 KiB per value, 200 read tokens.

On a replica the API reads but refuses changes (`409`): change secrets on the primary.
