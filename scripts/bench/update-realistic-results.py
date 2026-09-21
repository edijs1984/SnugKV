#!/usr/bin/env python3
import json
import pathlib
import statistics
import sys
from datetime import datetime

if len(sys.argv) != 2:
    raise SystemExit("usage: update-realistic-results.py <benchmark-result-dir>")

root = pathlib.Path(sys.argv[1])
rows = []
for path in sorted(root.glob("*/*.json")):
    if path.name == "matrix-summary.json":
        continue
    with path.open() as f:
        d = json.load(f)
    d["_profile"] = path.parent.name
    rows.append(d)

if not rows:
    raise SystemExit(f"no benchmark JSON rows found under {root}")

profiles = sorted({r["_profile"] for r in rows})
if len(profiles) != 1:
    raise SystemExit(f"expected exactly one profile, found: {profiles}")
profile = profiles[0]

servers = ("redis", "snug_raw", "snug_opt")
by = {}
for server in servers:
    for workload in ("load", "get"):
        items = [r for r in rows if r["server"] == server and r["workload"] == workload]
        if not items:
            raise SystemExit(f"missing {profile} {server} {workload}")
        by[(server, workload)] = {
            "ops": statistics.median(r["ops_per_second"] for r in items),
            "p95_us": statistics.median(r["p95_ns"] for r in items) / 1000.0,
            "bpk": statistics.median(r["bytes_per_key_delta"] for r in items) if workload == "load" else None,
        }

first = rows[0]
record = {
    "date": datetime.now().astimezone().strftime("%Y-%m-%d %H:%M %z"),
    "profile": profile,
    "keys": int(first["keys"]),
    "value_bytes": int(first["value_bytes"]),
    "workers": int(first["workers"]),
    "pipeline": int(first["pipeline"]),
    "runs": max(1, len([r for r in rows if r["server"] == "redis" and r["workload"] == "load"])),
    "redis_load": by[("redis", "load")]["ops"],
    "raw_load": by[("snug_raw", "load")]["ops"],
    "opt_load": by[("snug_opt", "load")]["ops"],
    "redis_get": by[("redis", "get")]["ops"],
    "raw_get": by[("snug_raw", "get")]["ops"],
    "opt_get": by[("snug_opt", "get")]["ops"],
    "redis_bpk": by[("redis", "load")]["bpk"],
    "raw_bpk": by[("snug_raw", "load")]["bpk"],
    "opt_bpk": by[("snug_opt", "load")]["bpk"],
    "redis_get_p95_us": by[("redis", "get")]["p95_us"],
    "raw_get_p95_us": by[("snug_raw", "get")]["p95_us"],
    "opt_get_p95_us": by[("snug_opt", "get")]["p95_us"],
}

json_path = pathlib.Path("benchmarks/realistic-profile-results.json")
if json_path.exists():
    data = json.loads(json_path.read_text())
else:
    data = {}

data[profile] = record
json_path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")

order = ["cache-json", "session-json", "api-json", "counter", "uuid", "text", "repetitive", "compressed", "random"]
names = {
    "cache-json": "Cached request/response JSON",
    "session-json": "Session JSON",
    "api-json": "API JSON",
    "counter": "Counter",
    "uuid": "UUID",
    "text": "Application text",
    "repetitive": "Compressible control",
    "compressed": "Already-compressed control",
    "random": "Incompressible control",
}

lines = [
    "# Realistic profile benchmark scoreboard",
    "",
    "Latest retained manual result for each profile. Each row is replaced when that profile is rerun.",
    "Defaults are 1,000,000 keys, Redis -> Snug raw -> Snug optimized in fresh isolated containers, LOAD + pipelined GET.",
    "",
    "| Profile | Value | Date | Redis SET/s | Raw SET/s | Opt SET/s | Redis GET/s | Raw GET/s | Opt GET/s | Redis B/key | Raw B/key | Opt B/key |",
    "|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
]
for p in order:
    if p not in data:
        continue
    r = data[p]
    lines.append(
        f"| {names.get(p,p)} (`{p}`) | {r['value_bytes']} B | {r['date']} | "
        f"{r['redis_load']:,.0f} | {r['raw_load']:,.0f} | {r['opt_load']:,.0f} | "
        f"{r['redis_get']:,.0f} | {r['raw_get']:,.0f} | {r['opt_get']:,.0f} | "
        f"{r['redis_bpk']:.2f} | {r['raw_bpk']:.2f} | {r['opt_bpk']:.2f} |"
    )

lines += [
    "",
    "## Reproduce one row",
    "",
    "```bash",
    "bash scripts/bench/bench-one.sh cache-json",
    "```",
    "",
    "Override defaults with environment variables such as `KEYS`, `GET_OPS`, `WORKERS`, `PIPELINE`, or `RUNS`.",
    "Set `BUILD_IMAGE=0` only when intentionally reusing an already-current benchmark image.",
    "",
]
pathlib.Path("benchmarks/REALISTIC_RESULTS.md").write_text("\n".join(lines))
print(f"updated benchmarks/REALISTIC_RESULTS.md row for {profile}")
