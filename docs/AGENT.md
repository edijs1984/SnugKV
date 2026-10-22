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
user agent on >AGENT-PASSWORD ~snug:agent:* +set +zadd +xadd +del +auth +ping
```

## What it writes

| Key | Type | |
|---|---|---|
| `snug:agent:servers` | sorted set | `<region>/<server>` → time of the last report (ms) |
| `snug:agent:card:<region>:<server>` | string (JSON) | inventory, latest metrics, disks, interfaces, containers; expires after 3 intervals, so a server that stops reporting shows as offline |
| `snug:agent:metrics:<region>:<server>` | stream | one sample per interval (`s` = `{"c":cpu%,"m":memory used,"l":load,"rx","tx","rd","wr":bytes/s,"d":fullest disk %,"r":running containers}`), trimmed to the retention |

A stopped agent deletes its card at once. The console's Servers page can forget a server that no longer reports.

Network rates count physical interfaces only (loopback, Docker bridges, veth, VPN tunnels left out); disk I/O counts whole disks, not partitions; disks are real filesystems (ext4, xfs, btrfs, zfs, …), one per device.
