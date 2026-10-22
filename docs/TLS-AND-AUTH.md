# Passwords and TLS

## Password

```bash
SNUGKV_REQUIREPASS='a-long-random-password' snugkv -listen 0.0.0.0:6380
```

`-requirepass` (or `SNUGKV_REQUIREPASS`, which keeps it out of process listings) gives the default user a password, like Redis `requirepass`. Every connection must then send `AUTH <password>` (or `HELLO 3 AUTH default <password>`) before anything else; until then only `AUTH`, `HELLO … AUTH` and `QUIT` are accepted and everything else answers `NOAUTH`.

Clients put it in the URL: `redis://:password@host:6380` (or `redis://user:password@…` for ACL users).

The secrets and webhook modules connect to the server like any client and use `-requirepass` automatically; `-secrets-auth` / `-webhooks-auth` override it (for example with their own ACL user).

## Users (ACL file)

For more than one password, use a Redis-style ACL file instead of `-requirepass`:

```text
user default on >admin-password ~* &* +@all
user airsig on >service-password ~* &* +@all
```

```bash
snugkv -acl-file /etc/snugkv/users.acl
```

`ACL SETUSER` changes users at run time and `ACL SAVE` writes them back to the file. `-requirepass` and `-acl-file` cannot be combined (set the default user's password in the file).

## TLS

```bash
snugkv -listen 127.0.0.1:6380 \
  -tls-listen 0.0.0.0:6390 -tls-cert /etc/snugkv/server.crt -tls-key /etc/snugkv/server.key
```

`-tls-listen` serves the same server over TLS (1.2 or newer) on its own port, next to the plain one. Bind the plain listener to loopback (for the modules and local tools) and give everything else the TLS port. Clients use `rediss://:password@host:6390`; with a private CA, Node clients need `NODE_EXTRA_CA_CERTS=/path/ca.crt`.

The certificate and key are read again when the files change (checked every 10 seconds), so renewed certificates (Let's Encrypt, an internal CA) are picked up without a restart. A broken or half-written new file keeps the last good certificate.

Client certificates (mutual TLS): `-tls-ca-cert ca.crt -tls-auth-clients` requires every TLS client to present a certificate signed by that CA. With `-tls-ca-cert` alone, certificates are checked when a client sends one.

| Flag | Environment | |
|---|---|---|
| `-requirepass` | `SNUGKV_REQUIREPASS` | password of the default user |
| `-acl-file` | `SNUGKV_ACL_FILE` | ACL file with users |
| `-tls-listen` | `SNUGKV_TLS_LISTEN` | TLS address |
| `-tls-cert`, `-tls-key` | `SNUGKV_TLS_CERT`, `SNUGKV_TLS_KEY` | PEM certificate (with chain) and key |
| `-tls-ca-cert` | `SNUGKV_TLS_CA_CERT` | CA for client certificates |
| `-tls-auth-clients` | `SNUGKV_TLS_AUTH_CLIENTS` | require client certificates |

## Replication over TLS

A replica connects to the primary's TLS port:

```bash
snugkv -listen 127.0.0.1:6380 -mastertls -mastertls-ca-cert ca.crt -masterauth "$PRIMARY_PASSWORD"
# then: REPLICAOF primary-host 6390
```

`-mastertls-cert` / `-mastertls-key` add a client certificate when the primary requires one.
