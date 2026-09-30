#!/usr/bin/env python3
"""Bounded first-release SnugKV cluster bootstrap helper.

This tool deliberately supports only the documented three-node first-release
layouts. It generates existing SnugKV configuration; it does not introduce a
new membership, discovery, or consensus protocol.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time
from typing import Any

SLOT_COUNT = 16384
NODE_COUNT = 3


def parse_nodes(text: str) -> list[str]:
    nodes = [part.strip() for part in text.split(",") if part.strip()]
    if len(nodes) != NODE_COUNT:
        raise ValueError(f"first-release bootstrap requires exactly {NODE_COUNT} nodes")
    if len(set(nodes)) != len(nodes):
        raise ValueError("node addresses must be unique")
    for node in nodes:
        validate_address(node)
    return nodes


def validate_address(addr: str) -> None:
    if addr.count(":") != 1:
        raise ValueError(f"node address must be host:port: {addr}")
    host, port_text = addr.rsplit(":", 1)
    if not host:
        raise ValueError(f"node address must include a host: {addr}")
    try:
        port = int(port_text)
    except ValueError as exc:
        raise ValueError(f"node address has invalid port: {addr}") from exc
    if not 1 <= port <= 65535:
        raise ValueError(f"node address has invalid port: {addr}")


def split_address(addr: str) -> tuple[str, int]:
    validate_address(addr)
    host, port_text = addr.rsplit(":", 1)
    return host, int(port_text)


def deterministic_slot_ranges(nodes: list[str]) -> dict[str, str]:
    """Split all 16,384 slots into stable near-equal contiguous ranges."""
    if not nodes:
        raise ValueError("at least one node is required")
    ranges: dict[str, str] = {}
    for index, node in enumerate(nodes):
        start = (index * SLOT_COUNT + len(nodes) // 2) // len(nodes)
        next_start = ((index + 1) * SLOT_COUNT + len(nodes) // 2) // len(nodes)
        end = next_start - 1
        ranges[f"{start}-{end}"] = node
    return ranges


def write_json(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def generate(args: argparse.Namespace) -> int:
    nodes = parse_nodes(args.nodes)
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)

    acl_path = output / "users.acl"
    acl_path.write_text(
        f"user default on >{args.password} ~* &* +@all\n",
        encoding="utf-8",
    )
    os.chmod(acl_path, 0o600)

    if args.mode == "sharded":
        slot_map = deterministic_slot_ranges(nodes)
        primary = None
    else:
        primary = nodes[args.primary_index]
        slot_map = {"0-16383": primary}

    configs: list[str] = []
    for index, node in enumerate(nodes):
        peers = [candidate for candidate in nodes if candidate != node]
        cfg: dict[str, Any] = {
            "listen": node,
            "admin_listen": "",
            "metrics_listen": "",
            "acl_file": str(acl_path),
            "cluster_enabled": True,
            "cluster_node_addr": node,
            "cluster_control_auth": args.control_auth,
            "cluster_slots": slot_map,
        }

        if args.mode == "ha":
            cfg.update(
                {
                    "aof_path": str(output / f"node-{index}.aof"),
                    "fsync": "everysec",
                    "masterauth": args.password,
                    "auto_failover_timeout_ms": args.failover_timeout_ms,
                    "failover_peers": peers,
                    "failover_quorum": 2,
                    "failover_priority": 100 if index == args.primary_index else 10 + index,
                    "failover_group_id": args.group_id,
                    "failover_config_epoch": 1,
                    "failover_advertise_addr": node,
                }
            )

        config_path = output / f"node-{index}.json"
        write_json(config_path, cfg)
        configs.append(str(config_path))

    manifest = {
        "version": 1,
        "mode": args.mode,
        "nodes": nodes,
        "configs": configs,
        "acl_file": str(acl_path),
        "cluster_control_auth": args.control_auth,
        "slot_map": slot_map,
        "primary": primary,
        "primary_index": args.primary_index if args.mode == "ha" else None,
        "group_id": args.group_id if args.mode == "ha" else None,
    }
    manifest_path = output / "manifest.json"
    write_json(manifest_path, manifest)

    print(f"generated {args.mode} first-release cluster:")
    print(f"  manifest: {manifest_path}")
    for index, config_path in enumerate(configs):
        print(f"  node {index}: {nodes[index]} -> {config_path}")
    if args.mode == "ha":
        replicas = [node for node in nodes if node != primary]
        print(f"  primary: {primary}")
        print(f"  replicas: {', '.join(replicas)}")
    else:
        for slot_range, owner in slot_map.items():
            print(f"  slots {slot_range}: {owner}")
    return 0


def load_manifest(path: str) -> dict[str, Any]:
    manifest_path = Path(path).resolve()
    data = json.loads(manifest_path.read_text(encoding="utf-8"))
    if data.get("version") != 1:
        raise ValueError("unsupported bootstrap manifest version")
    nodes = data.get("nodes")
    if not isinstance(nodes, list) or len(nodes) != NODE_COUNT:
        raise ValueError("manifest must contain exactly three nodes")
    for node in nodes:
        validate_address(node)
    if data.get("mode") not in {"sharded", "ha"}:
        raise ValueError("manifest mode must be sharded or ha")
    return data


def redis_cli_command(
    redis_cli: str,
    addr: str,
    password: str,
    command: list[str],
    *,
    cluster: bool = False,
) -> list[str]:
    host, port = split_address(addr)
    argv = [
        redis_cli,
        "--no-auth-warning",
        "--raw",
        "-h",
        host,
        "-p",
        str(port),
        "-a",
        password,
    ]
    if cluster:
        argv.append("-c")
    argv.extend(command)
    proc = subprocess.run(argv, text=True, capture_output=True, check=False)
    output = (proc.stdout + proc.stderr).strip()
    if proc.returncode != 0:
        raise RuntimeError(f"{addr} {' '.join(command)} failed: {output}")
    lines = [line.rstrip("\r") for line in output.splitlines()]
    if lines and (
        lines[0].startswith("ERR ")
        or lines[0].startswith("NOAUTH ")
        or lines[0].startswith("WRONGPASS ")
    ):
        raise RuntimeError(f"{addr} {' '.join(command)} failed: {output}")
    return lines


def wait_ready(redis_cli: str, addr: str, password: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    last_error = ""
    while time.monotonic() < deadline:
        try:
            lines = redis_cli_command(redis_cli, addr, password, ["PING"])
            if lines == ["PONG"]:
                return
            last_error = repr(lines)
        except Exception as exc:  # retry boundary
            last_error = str(exc)
        time.sleep(0.1)
    raise RuntimeError(f"node {addr} did not become ready: {last_error}")


def parse_pair_lines(lines: list[str]) -> dict[str, str]:
    if len(lines) % 2 != 0:
        raise RuntimeError(f"expected key/value reply, got: {lines!r}")
    return dict(zip(lines[0::2], lines[1::2]))


def wait_replica(
    redis_cli: str,
    addr: str,
    password: str,
    timeout: float,
) -> None:
    deadline = time.monotonic() + timeout
    last = ""
    while time.monotonic() < deadline:
        lines = redis_cli_command(redis_cli, addr, password, ["INFO", "replication"])
        text = "\n".join(lines)
        last = text
        if "role:slave" in text and "master_link_status:up" in text:
            return
        time.sleep(0.1)
    raise RuntimeError(f"replica {addr} did not stabilize:\n{last}")


def wait_primary_writable(
    redis_cli: str,
    addr: str,
    password: str,
    timeout: float,
) -> None:
    deadline = time.monotonic() + timeout
    last = ""
    while time.monotonic() < deadline:
        try:
            lines = redis_cli_command(
                redis_cli,
                addr,
                password,
                ["SET", "snug:bootstrap:lease-ready", "yes"],
            )
            last = "\n".join(lines)
            if lines == ["OK"]:
                redis_cli_command(redis_cli, addr, password, ["DEL", "snug:bootstrap:lease-ready"])
                return
        except Exception as exc:
            last = str(exc)
        time.sleep(0.1)
    raise RuntimeError(f"primary {addr} did not become writable: {last}")


def verify_cluster(manifest: dict[str, Any], args: argparse.Namespace) -> None:
    nodes: list[str] = manifest["nodes"]
    for addr in nodes:
        wait_ready(args.redis_cli, addr, args.password, args.timeout)
        consistency = parse_pair_lines(
            redis_cli_command(
                args.redis_cli,
                addr,
                args.password,
                ["CLUSTER", "CONSISTENCY"],
            )
        )
        if consistency.get("coverage_ok") != "1":
            raise RuntimeError(f"{addr} has incomplete cluster coverage: {consistency}")
        if consistency.get("assigned_slots") != str(SLOT_COUNT):
            raise RuntimeError(f"{addr} assigned_slots mismatch: {consistency}")
        if consistency.get("transitioning") != "0":
            raise RuntimeError(f"{addr} has active slot transition: {consistency}")

    if manifest["mode"] == "ha":
        primary = manifest["primary"]
        assert isinstance(primary, str)
        wait_primary_writable(args.redis_cli, primary, args.password, args.timeout)

        primary_info = "\n".join(
            redis_cli_command(args.redis_cli, primary, args.password, ["INFO", "replication"])
        )
        if "role:master" not in primary_info:
            raise RuntimeError(f"expected primary role on {primary}:\n{primary_info}")

        for replica in [node for node in nodes if node != primary]:
            wait_replica(args.redis_cli, replica, args.password, args.timeout)

        health_lines = redis_cli_command(
            args.redis_cli,
            primary,
            args.password,
            ["SNUG.FAILOVER", "HEALTH"],
        )
        if len(health_lines) != 1:
            raise RuntimeError(f"unexpected failover health reply: {health_lines!r}")
        health = json.loads(health_lines[0])
        if health.get("status") != "healthy":
            raise RuntimeError(f"failover health is not healthy: {health}")
        if health.get("quorum_reachable") is not True:
            raise RuntimeError(f"failover quorum is not reachable: {health}")

    probe_key = "snug:bootstrap:probe"
    redis_cli_command(
        args.redis_cli,
        nodes[0],
        args.password,
        ["SET", probe_key, "ok"],
        cluster=True,
    )
    for addr in nodes:
        got = redis_cli_command(
            args.redis_cli,
            addr,
            args.password,
            ["GET", probe_key],
            cluster=True,
        )
        if got != ["ok"]:
            raise RuntimeError(f"cluster routed read failed via {addr}: {got!r}")
    redis_cli_command(
        args.redis_cli,
        nodes[0],
        args.password,
        ["DEL", probe_key],
        cluster=True,
    )


def apply(args: argparse.Namespace) -> int:
    manifest = load_manifest(args.manifest)
    nodes: list[str] = manifest["nodes"]

    for addr in nodes:
        wait_ready(args.redis_cli, addr, args.password, args.timeout)

    if manifest["mode"] == "ha":
        primary = manifest["primary"]
        if not isinstance(primary, str):
            raise ValueError("HA manifest is missing primary")
        host, port = split_address(primary)
        for replica in [node for node in nodes if node != primary]:
            lines = redis_cli_command(
                args.redis_cli,
                replica,
                args.password,
                ["REPLICAOF", host, str(port)],
            )
            if lines != ["OK"]:
                raise RuntimeError(f"{replica} REPLICAOF returned {lines!r}")
        for replica in [node for node in nodes if node != primary]:
            wait_replica(args.redis_cli, replica, args.password, args.timeout)

    verify_cluster(manifest, args)
    print("first-release cluster bootstrap: PASS")
    return 0


def verify(args: argparse.Namespace) -> int:
    manifest = load_manifest(args.manifest)
    verify_cluster(manifest, args)
    print("first-release cluster verification: PASS")
    return 0


def add_runtime_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--manifest", required=True, help="generated manifest.json")
    parser.add_argument("--password", required=True, help="default-user ACL password")
    parser.add_argument("--redis-cli", default="redis-cli", help="redis-cli executable")
    parser.add_argument("--timeout", type=float, default=20.0, help="seconds per readiness/stability wait")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Generate/apply the bounded three-node first-release SnugKV cluster."
    )
    sub = parser.add_subparsers(dest="action", required=True)

    gen = sub.add_parser("generate", help="generate per-node configs and bootstrap manifest")
    gen.add_argument("--mode", choices=["sharded", "ha"], required=True)
    gen.add_argument("--nodes", required=True, help="exactly three comma-separated host:port addresses")
    gen.add_argument("--output", required=True)
    gen.add_argument("--password", required=True, help="default-user ACL password")
    gen.add_argument("--control-auth", required=True, help="internal cluster control credential")
    gen.add_argument("--group-id", default="snug-first-release")
    gen.add_argument("--primary-index", type=int, choices=[0, 1, 2], default=0)
    gen.add_argument("--failover-timeout-ms", type=int, default=1000)
    gen.set_defaults(func=generate)

    apply_parser = sub.add_parser("apply", help="attach replicas where needed and verify the cluster")
    add_runtime_args(apply_parser)
    apply_parser.set_defaults(func=apply)

    verify_parser = sub.add_parser("verify", help="verify an already-bootstrapped cluster")
    add_runtime_args(verify_parser)
    verify_parser.set_defaults(func=verify)
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        return int(args.func(args))
    except (ValueError, RuntimeError, json.JSONDecodeError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
