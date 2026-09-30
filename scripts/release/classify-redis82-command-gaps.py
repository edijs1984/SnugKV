#!/usr/bin/env python3
import argparse
import json
import pathlib

IMPLEMENTED = {
    "ACL|CAT","ACL|DELUSER","ACL|DRYRUN","ACL|GENPASS","ACL|GETUSER","ACL|HELP",
    "ACL|LIST","ACL|LOAD","ACL|LOG","ACL|SAVE","ACL|SETUSER","ACL|USERS","ACL|WHOAMI",
    "CLUSTER|ADDSLOTS","CLUSTER|COUNTKEYSINSLOT","CLUSTER|DELSLOTS","CLUSTER|FLUSHSLOTS",
    "CLUSTER|GETKEYSINSLOT","CLUSTER|INFO","CLUSTER|KEYSLOT","CLUSTER|MYID",
    "CLUSTER|NODES","CLUSTER|SETSLOT","CLUSTER|SHARDS","CLUSTER|SLOTS",
    "COMMAND|LIST","MEMORY|USAGE",
    "OBJECT|ENCODING","OBJECT|HELP","OBJECT|REFCOUNT",
    "PUBSUB|CHANNELS","PUBSUB|HELP","PUBSUB|NUMPAT","PUBSUB|NUMSUB","PUBSUB|SHARDCHANNELS","PUBSUB|SHARDNUMSUB",
    "SLOWLOG|GET","SLOWLOG|HELP","SLOWLOG|LEN","SLOWLOG|RESET",
    "XGROUP|CREATE","XGROUP|CREATECONSUMER","XGROUP|DELCONSUMER","XGROUP|DESTROY","XGROUP|SETID",
    "XINFO|CONSUMERS","XINFO|GROUPS","XINFO|HELP","XINFO|STREAM",
}

INTENTIONALLY_UNSUPPORTED = {
    "MODULE","MODULE|HELP","MODULE|LIST","MODULE|LOAD","MODULE|LOADEX","MODULE|UNLOAD",
    "MOVE","SWAPDB","OBJECT|FREQ","OBJECT|IDLETIME",
}

IRRELEVANT = {
    "DEBUG","LOLWUT","PFDEBUG","PFSELFTEST","BF.DEBUG","CF.DEBUG",
    "SEARCH.CLUSTERINFO","SEARCH.CLUSTERREFRESH","SEARCH.CLUSTERSET",
    "TIMESERIES.CLUSTERSET","TIMESERIES.REFRESHCLUSTER",
}

def classify(name):
    if name in IMPLEMENTED:
        return "IMPLEMENTED", "Command/subcommand exists in SnugKV; gap is structured COMMAND metadata coverage."
    if name in INTENTIONALLY_UNSUPPORTED:
        if name.startswith("MODULE"):
            return "INTENTIONALLY_UNSUPPORTED", "SnugKV implements extended features natively and does not load Redis modules."
        if name in {"MOVE","SWAPDB"}:
            return "INTENTIONALLY_UNSUPPORTED", "First release intentionally supports DB 0 only."
        return "INTENTIONALLY_UNSUPPORTED", "Requires Redis-style object access metadata that SnugKV intentionally does not currently track."
    if name in IRRELEVANT:
        return "IRRELEVANT", "Debug/internal/module-coordination surface does not define a first-release SnugKV workflow."
    if name.startswith("_FT.") or name.startswith("FT._"):
        return "IRRELEVANT", "Redis Search internal/private command surface."
    return "CANDIDATE_GAP", "Real Redis surface not yet proven necessary; validate against first-release workflows and client traces."

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("--input", required=True, type=pathlib.Path)
    ap.add_argument("--markdown", required=True, type=pathlib.Path)
    ap.add_argument("--json", required=True, type=pathlib.Path)
    args=ap.parse_args()
    data=json.loads(args.input.read_text())
    rows=[]
    counts={"IMPLEMENTED":0,"INTENTIONALLY_UNSUPPORTED":0,"CANDIDATE_GAP":0,"IRRELEVANT":0}
    for item in data["redis_only"]:
        name=item["name"]
        classification, reason=classify(name)
        counts[classification]+=1
        rows.append({"name":name,"classification":classification,"reason":reason,"redis":item["redis"]})

    output={"source_counts":{"redis_entries":data["redis_entry_count"],"snug_entries":data["snug_entry_count"],"shared":data["shared_count"],"redis_only":data["redis_only_count"],"snug_only":data["snug_only_count"]},"classification_counts":counts,"entries":rows}
    args.json.write_text(json.dumps(output,indent=2)+"\n")

    lines=[
        "# Redis 8.2 Command Gap Classification","",
        "> Classification of the 189 Redis-only inventory entries discovered by the A2 live-oracle comparison.",
        "> This is an A2 compatibility classification, not the final Phase A4 implementation priority.","",
        "## Summary","",
        f"- Already implemented, metadata-only gaps: **{counts['IMPLEMENTED']}**",
        f"- Intentionally unsupported for first-release architecture: **{counts['INTENTIONALLY_UNSUPPORTED']}**",
        f"- Candidate gaps requiring client/workflow evidence: **{counts['CANDIDATE_GAP']}**",
        f"- Redis internal/debug/module-specific and irrelevant: **{counts['IRRELEVANT']}**","",
        "## Classification","",
        "| Command | A2 classification | Reason |","|---|---|---|",
    ]
    for row in rows:
        lines.append("| " + chr(96) + row["name"] + chr(96) + " | **" + row["classification"] + "** | " + row["reason"] + " |")
    lines += [
        "","## Interpretation","",
        "- IMPLEMENTED entries are not feature work; they expose structured COMMAND metadata completeness gaps.",
        "- INTENTIONALLY_UNSUPPORTED entries conflict with a deliberate first-release architecture boundary.",
        "- CANDIDATE_GAP entries feed Phase A3 client/workflow tracing and are not automatically release requirements.",
        "- IRRELEVANT entries are Redis diagnostics, private Search commands, or module-coordination surfaces without a SnugKV first-release use case.","",
        "Phase A4 converts only evidence-backed candidate gaps into REQUIRED / USEFUL / DEFER implementation decisions.",
    ]
    args.markdown.write_text("\n".join(lines)+"\n")
    print(json.dumps(counts,indent=2))

if __name__=="__main__":
    main()
