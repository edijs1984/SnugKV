#!/usr/bin/env python3
import argparse, json, pathlib

def flatten_redis_command(entry, out):
    if not isinstance(entry, list) or len(entry) < 1:
        return
    name = str(entry[0]).upper()
    out[name] = {
        "name": name,
        "arity": entry[1] if len(entry) > 1 else None,
        "flags": entry[2] if len(entry) > 2 else [],
        "first_key": entry[3] if len(entry) > 3 else 0,
        "last_key": entry[4] if len(entry) > 4 else 0,
        "step": entry[5] if len(entry) > 5 else 0,
        "acl_categories": entry[6] if len(entry) > 6 else [],
        "tips": entry[7] if len(entry) > 7 else [],
    }
    if len(entry) > 9 and isinstance(entry[9], list):
        for child in entry[9]:
            flatten_redis_command(child, out)

def load_snug(path):
    data = json.loads(path.read_text())
    return {e["name"].upper(): e for e in data["entries"]}

def load_redis(path):
    data = json.loads(path.read_text())
    if not isinstance(data, list):
        raise SystemExit("Redis COMMAND JSON root is not an array")
    out = {}
    for entry in data:
        flatten_redis_command(entry, out)
    return out

def is_subcommand(name):
    return "|" in name

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--snug", required=True, type=pathlib.Path)
    ap.add_argument("--redis", required=True, type=pathlib.Path)
    ap.add_argument("--markdown", required=True, type=pathlib.Path)
    ap.add_argument("--json", required=True, type=pathlib.Path)
    args = ap.parse_args()

    snug = load_snug(args.snug)
    redis = load_redis(args.redis)
    shared = sorted(set(snug) & set(redis))
    redis_only = sorted(set(redis) - set(snug))
    snug_only = sorted(set(snug) - set(redis))

    result = {
        "redis_entry_count": len(redis),
        "snug_entry_count": len(snug),
        "shared_count": len(shared),
        "redis_only_count": len(redis_only),
        "snug_only_count": len(snug_only),
        "redis_top_level_count": sum(not is_subcommand(x) for x in redis),
        "redis_subcommand_count": sum(is_subcommand(x) for x in redis),
        "snug_top_level_count": sum(not is_subcommand(x) for x in snug),
        "snug_subcommand_count": sum(is_subcommand(x) for x in snug),
        "redis_only": [{"name": n, "redis": redis[n], "classification": "UNCLASSIFIED", "reason": ""} for n in redis_only],
        "snug_only": [{"name": n, "snug": snug[n]} for n in snug_only],
        "shared": [{"name": n, "redis": redis[n], "snug": snug[n]} for n in shared],
    }

    args.json.parent.mkdir(parents=True, exist_ok=True)
    args.json.write_text(json.dumps(result, indent=2) + "\n")

    lines = [
        "# Redis 8.2 Command Gap Audit",
        "",
        "> Generated from a live Redis 8.2 COMMAND oracle and SnugKV's retained command inventory.",
        "> Missing Redis entries are initially UNCLASSIFIED; classification is a separate review step.",
        "",
        "## Counts",
        "",
        f"- Redis entries: **{len(redis)}** ({result['redis_top_level_count']} top-level, {result['redis_subcommand_count']} subcommands)",
        f"- SnugKV entries: **{len(snug)}** ({result['snug_top_level_count']} top-level, {result['snug_subcommand_count']} subcommands)",
        f"- Shared entries: **{len(shared)}**",
        f"- Redis-only entries: **{len(redis_only)}**",
        f"- SnugKV-only entries: **{len(snug_only)}**",
        "",
        "## Redis entries not currently present in SnugKV",
        "",
        "| Command | Classification | Reason |",
        "|---|---|---|",
    ]
    for name in redis_only:
        lines.append(f"| " + chr(96) + f"{name}" + chr(96) + " | UNCLASSIFIED | — |")

    lines += ["", "## SnugKV entries not present in Redis 8.2 core", "", "| Command |", "|---|"]
    for name in snug_only:
        lines.append(f"| " + chr(96) + f"{name}" + chr(96) + " |")

    lines += [
        "",
        "## Classification rules",
        "",
        "- **REQUIRED** — supported first-release client/workflow breaks without it.",
        "- **USEFUL** — materially improves adoption or operability.",
        "- **DEFER** — low-value, architectural mismatch, obscure, or unused by supported workflows.",
        "- **IRRELEVANT** — Redis implementation/internal behavior that does not map to SnugKV.",
        "",
        "A Redis-only command is not automatically a first-release requirement.",
    ]
    args.markdown.write_text("\n".join(lines) + "\n")
    print(json.dumps({"redis_entries":len(redis),"snug_entries":len(snug),"shared":len(shared),"redis_only":len(redis_only),"snug_only":len(snug_only)}, indent=2))

if __name__ == "__main__":
    main()
