# snug-agent

`snug-agent` reports the server it runs on to SnugKV every few seconds; the console's **Servers** page shows it: CPU, memory, load, disks, disk I/O, network, OS and hardware, Docker containers (with health) and the Swarm role. It is one static binary that reads `/proc`, `/sys` and the Docker socket, and talks to SnugKV like any client (password and TLS supported). It only reads; it changes nothing on the server.

## Add a server from the console (easiest)

In the console, **Servers > Add server** creates a one-time command for a fresh Debian 12+ / Ubuntu 22.04+ machine:

```bash
curl -fsSL 'https://panel.example.com/join.sh' | sudo bash -s -- --panel 'https://panel.example.com' --token snj_...
```

It downloads `snug-agent` (from the latest GitHub release, checksum checked), runs `snug-agent join`, which trades the token for the region's SnugKV address and writes `/etc/snug-agent/agent.env`, then installs Docker, the systemd unit below, automatic security updates, key-only SSH and a firewall (`table inet snug`: SSH, WireGuard and, with `--ingress`, 80/443 in; nothing else). See the console's README for the options.

`snug-agent join -panel URL -token TOKEN [-name NAME] [-dir /etc/snug-agent]` can also be run by hand.

Binaries: pushing a tag `v*` runs `.github/workflows/release.yml`, which attaches `snug-agent-linux-amd64`, `snug-agent-linux-arm64` and `SHA256SUMS` to the release.

## The mesh (WireGuard between the servers of a region)

Every region has a private range (`10.88.<n>.0/24` unless you set one on the region in the console). When a server joins, `snug-agent join` creates a WireGuard key (`/etc/snug-agent/wg.key`, never leaves the server) and sends the public key and the server's public address; the console gives the server the next free address in the range, publishes it to the other servers and returns the current ones. `join.sh` then starts a second unit:

```text
snug-mesh.service   snug-agent mesh   (root, CAP_NET_ADMIN only)
```

It keeps the interface `snug0` up with the server's address (UDP 51820), publishes its key and state to `snug:agent:mesh:<region>`, and every 10 seconds makes the interface's peers match what the region's servers published: new servers are added, moved ones updated, forgotten ones removed. Records from SnugKV are checked before they reach `wg` (key format, address inside the range, `ip:port` endpoint; when two servers claim one address, the first by name keeps it). It changes nothing while SnugKV cannot be read, and the interface stays up when the unit stops or restarts, so the mesh never depends on SnugKV being up. Until the first sync, it uses the peers the console gave at join (`mesh-peers.json`), so a new server can reach SnugKV over the mesh right away.

To keep SnugKV off the internet: join the SnugKV server first (it gets `.1`), set the region's agent address in the console to `redis://agent:PASSWORD@10.88.<n>.1:6380`, and let the firewall close the SnugKV port to the outside (join.sh allows only SSH, WireGuard and, on ingress servers, 80/443 in; everything from `snug0` is allowed).

Options of `snug-agent mesh`: `-ip` (`SNUG_AGENT_MESH_IP`, from join), `-key`, `-endpoint` (`SNUG_AGENT_ENDPOINT`; default: the server's address towards the internet), `-port` (51820), `-iface` (snug0). `snug-agent join -no-mesh` joins without it.

Anyone who can write `snug:agent:mesh:<region>` can add a peer to the region's mesh: keep the agent password as safe as the servers (signed commands, a later phase, will not depend on it).

## Commands from the console

The console can send the agent commands, signed with the console's own Ed25519 key (a file on the console, `--command-key`; not stored in SnugKV). A server gets the console's public key when it joins (`SNUG_AGENT_COMMAND_KEY` in agent.env) and runs a command only when:

- the signature is valid for that key,
- the command names this region and server,
- it is current (expires 2 minutes after it was sent; anything meant to live over 10 minutes is refused) and was not run before.

So the SnugKV password alone is not enough to run anything on a server. Commands arrive on `snug:agent:cmd:<region>:<server>` (fields `c` = JSON, `s` = signature), results go to `snug:agent:results:<region>:<server>`; the last command handled is kept in the systemd state directory, so a restart neither repeats nor skips one.

| Command | What it does |
|---|---|
| `ping` | answers `pong` |
| `swarm.init` | starts a Docker Swarm with this server as the first manager, on its mesh address (`ListenAddr`/`AdvertiseAddr`/`DataPathAddr`), returns the join tokens |
| `swarm.join` | joins a swarm through the given managers, on this server's mesh address |
| `swarm.leave` | leaves the swarm |
| `swarm.tokens` | the join tokens (managers) |

In the console: Servers shows a Swarm line under each region (create the swarm on one server, then add the others: the first three become managers, the rest workers), and each server's page has a Commands card (ping, history). Servers that joined before need the 0.3 agent and a new join command to get the key.

Overlay networks run over WireGuard: give them an MTU of 1370 (`--opt com.docker.network.driver.mtu=1370`); phase 3 will create them that way.

## Run it on a server (systemd)

```bash
install -m 755 snug-agent /usr/local/bin/
cat > /etc/systemd/system/snug-agent.service <<'UNIT'
[Unit]
Description=snug-agent
After=network-online.target docker.service

[Service]
Environment=SNUG_AGENT_URL=rediss://agent:PASSWORD@snugkv.internal:6390
Environment=SNUG_AGENT_REGION=eu-1
ExecStart=/usr/local/bin/snug-agent
Restart=always
RestartSec=5
DynamicUser=yes
SupplementaryGroups=docker

[Install]
WantedBy=multi-user.target
UNIT
systemctl enable --now snug-agent
```

`SupplementaryGroups=docker` lets it read the Docker socket; leave it out (and pass `-docker ""`) for servers without Docker.

## Run it in a container

The SnugKV image contains it at `/snug-agent`. Give it the host's `/proc`, `/sys` and `/` read-only, and the Docker socket:

```bash
docker run -d --name snug-agent --restart always --user 0 --pid host --network host \
  -v /proc:/host/proc:ro -v /sys:/host/sys:ro -v /:/host/root:ro \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -e SNUG_AGENT_PROC=/host/proc -e SNUG_AGENT_SYS=/host/sys -e SNUG_AGENT_ROOT=/host/root \
  -e SNUG_AGENT_URL=redis://:PASSWORD@10.0.0.2:6380 -e SNUG_AGENT_REGION=eu-1 \
  --entrypoint /snug-agent <snugkv image>
```

The server name is the host's name unless `SNUG_AGENT_SERVER` says otherwise.

## Options

| Flag | Environment | Default | |
|---|---|---|---|
| `-url` | `SNUG_AGENT_URL` | | `redis://[user:]password@host:port`, `rediss://…` for TLS |
| `-ca` | `SNUG_AGENT_CA` | | CA file for `rediss://` with a private CA |
| `-region` | `SNUG_AGENT_REGION` | `default` | region of this server (`a-z 0-9 . _ -`) |
| `-server` | `SNUG_AGENT_SERVER` | host name | name of this server |
| `-interval` | | `5s` | how often to report |
| `-retention` | | `24h` | how long samples are kept |
| `-docker` | `SNUG_AGENT_DOCKER` | `/var/run/docker.sock` | Docker socket; empty to skip containers |
| `-proc`, `-sys`, `-root` | `SNUG_AGENT_PROC`, `_SYS`, `_ROOT` | `/proc`, `/sys`, `/` | where the host's files are (in a container) |

With ACLs, give the agents their own user that can only write their keys:

```text
user agent on >AGENT-PASSWORD ~snug:agent:* +set +zadd +xadd +del +hset +hgetall +xread +xrevrange +auth +ping
```

## What it writes

| Key | Type | |
|---|---|---|
| `snug:agent:servers` | sorted set | `<region>/<server>` → time of the last report (ms) |
| `snug:agent:card:<region>:<server>` | string (JSON) | inventory, latest metrics, disks, interfaces, containers; expires after 3 intervals, so a server that stops reporting shows as offline |
| `snug:agent:metrics:<region>:<server>` | stream | one sample per interval (`s` = `{"c":cpu%,"m":memory used,"l":load,"rx","tx","rd","wr":bytes/s,"d":fullest disk %,"r":running containers}`), trimmed to the retention |
| `snug:agent:mesh:<region>` | hash | server → `{"publicKey","ip","endpoint","peersTotal","peersUp","updatedMs","error"}`, written by `snug-agent mesh` |

A stopped agent deletes its card at once. The console's Servers page can forget a server that no longer reports.

Network rates count physical interfaces only (loopback, Docker bridges, veth, VPN tunnels left out); disk I/O counts whole disks, not partitions; disks are real filesystems (ext4, xfs, btrfs, zfs, …), one per device.
