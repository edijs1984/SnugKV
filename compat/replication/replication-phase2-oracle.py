#!/usr/bin/env python3
import os
import socket
import time

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
PORT = int(os.environ.get("PRIMARY_PORT", "6393"))

class RedisConn:
    def __init__(self, port):
        self.s = socket.create_connection((HOST, port), timeout=3)
        self.f = self.s.makefile("rb")

    def close(self):
        try:
            self.f.close()
        finally:
            self.s.close()

    def command(self, *args):
        out = [("*%d\r\n" % len(args)).encode()]
        for arg in args:
            if not isinstance(arg, (bytes, bytearray)):
                arg = str(arg).encode()
            out += [("$%d\r\n" % len(arg)).encode(), bytes(arg), b"\r\n"]
        self.s.sendall(b"".join(out))
        return self.read()

    def line(self):
        b = self.f.readline()
        if not b:
            raise EOFError("closed")
        return b[:-2]

    def read(self):
        p = self.f.read(1)
        if p == b"+":
            return ("simple", self.line().decode(errors="replace"))
        if p == b"-":
            return ("error", self.line().decode(errors="replace"))
        if p == b":":
            return ("int", int(self.line()))
        if p == b"$":
            n = int(self.line())
            if n < 0:
                return ("bulk", None)
            d = self.f.read(n)
            self.f.read(2)
            return ("bulk", d.decode(errors="replace"))
        if p == b"*":
            n = int(self.line())
            if n < 0:
                return ("array", None)
            return ("array", [self.read() for _ in range(n)])
        raise RuntimeError(repr(p))

def scalar(r):
    if r[0] in ("simple", "bulk", "int"):
        return r[1]
    raise TypeError(r)

def parse_info(body):
    out = {}
    for line in body.splitlines():
        if ":" in line and not line.startswith("#"):
            k, v = line.split(":", 1)
            out[k] = v
    return out

def info(c, section="replication"):
    return parse_info(scalar(c.command("INFO", section)))

def send_psync(replid, offset):
    s = socket.create_connection((HOST, PORT), timeout=3)
    f = s.makefile("rb")
    rid = str(replid).encode()
    off = str(offset).encode()
    cmd = (
        b"*3\r\n"
        b"$5\r\nPSYNC\r\n"
        + ("$%d\r\n" % len(rid)).encode()
        + rid + b"\r\n"
        + ("$%d\r\n" % len(off)).encode()
        + off + b"\r\n"
    )
    s.sendall(cmd)
    first = f.readline().decode(errors="replace").strip()
    return s, f, first

def consume_full_sync(f):
    second_raw = f.readline()
    if not second_raw:
        raise EOFError("missing snapshot header")
    second = second_raw.decode(errors="replace").strip()

    if second.startswith("$EOF:"):
        marker = second[5:].encode()
        if not marker:
            raise RuntimeError("empty diskless EOF marker")

        total = 0
        buf = bytearray()
        keep = max(1, len(marker) - 1)

        while True:
            chunk = f.read(65536)
            if not chunk:
                raise EOFError("diskless snapshot truncated before EOF marker")
            buf.extend(chunk)

            pos = buf.find(marker)
            if pos >= 0:
                total += pos
                return total, "eof"

            if len(buf) > keep:
                flush = len(buf) - keep
                total += flush
                del buf[:flush]

    if not second.startswith("$"):
        raise RuntimeError("expected snapshot header, got %r" % second)

    n = int(second[1:])
    remaining = n
    while remaining:
        chunk = f.read(min(65536, remaining))
        if not chunk:
            raise EOFError("snapshot truncated")
        remaining -= len(chunk)

    trailer = f.read(2)
    if trailer != b"\r\n":
        raise RuntimeError("bad snapshot trailer: %r" % (trailer,))
    return n, "bulk"

def full_sync_probe():
    s, f, first = send_psync("?", -1)
    try:
        parts = first.split()
        if len(parts) < 3 or parts[0] != "+FULLRESYNC":
            raise RuntimeError("unexpected PSYNC full-sync reply: %r" % first)
        replid = parts[1]
        offset = int(parts[2])
        snapshot_bytes, snapshot_mode = consume_full_sync(f)
        print("full_sync=" + repr({
            "kind": "FULLRESYNC",
            "replid_len": len(replid),
            "offset_is_integer": True,
            "snapshot_nonempty": snapshot_bytes > 0,
            "snapshot_mode": snapshot_mode,
        }))
        return replid, offset
    finally:
        f.close()
        s.close()

def partial_sync_probe(replid, offset):
    s, f, first = send_psync(replid, offset)
    try:
        parts = first.split()
        kind = parts[0].lstrip("+") if parts else ""
        print("partial_sync=" + repr({
            "kind": kind,
            "has_optional_replid": len(parts) > 1,
        }))
        return kind
    finally:
        f.close()
        s.close()

def fallback_probe(replid, offset):
    s, f, first = send_psync(replid, offset)
    try:
        parts = first.split()
        kind = parts[0].lstrip("+") if parts else ""
        snapshot = False
        snapshot_mode = None
        if kind == "FULLRESYNC":
            _, snapshot_mode = consume_full_sync(f)
            snapshot = True
        print("future_offset=" + repr({
            "kind": kind,
            "snapshot_received": snapshot,
            "snapshot_mode": snapshot_mode,
        }))
        return kind
    finally:
        f.close()
        s.close()

c = RedisConn(PORT)
try:
    scalar(c.command("FLUSHALL"))

    print("=== initial replication metadata ===")
    before = info(c)
    print("metadata=" + repr({
        "role": before.get("role"),
        "master_replid_len": len(before.get("master_replid", "")),
        "master_repl_offset_is_integer": before.get("master_repl_offset", "").lstrip("-").isdigit(),
        "repl_backlog_active": before.get("repl_backlog_active"),
        "repl_backlog_size_is_integer": before.get("repl_backlog_size", "").isdigit(),
        "repl_backlog_first_byte_offset_is_integer": before.get("repl_backlog_first_byte_offset", "").lstrip("-").isdigit(),
        "repl_backlog_histlen_is_integer": before.get("repl_backlog_histlen", "").isdigit(),
    }))

    print("\n=== establish full sync ===")
    replid, base_offset = full_sync_probe()

    time.sleep(0.05)
    after_full = info(c)
    print("backlog_after_full_sync=" + repr({
        "active": after_full.get("repl_backlog_active"),
        "histlen_positive_or_zero": int(after_full.get("repl_backlog_histlen", "0")) >= 0,
    }))

    print("\n=== advance replication stream ===")
    scalar(c.command("SET", "phase2:a", "1"))
    scalar(c.command("INCR", "phase2:counter"))
    scalar(c.command("HSET", "phase2:hash", "field", "value"))
    after_writes = info(c)
    current_offset = int(after_writes.get("master_repl_offset", "0"))
    print("offset_advanced=" + repr(current_offset > base_offset))

    print("\n=== partial resynchronization ===")
    kind = partial_sync_probe(replid, base_offset)
    print("partial_sync_accepted=" + repr(kind == "CONTINUE"))

    print("\n=== invalid future offset fallback ===")
    kind = fallback_probe(replid, current_offset + 1000000)
    print("future_offset_forces_full_sync=" + repr(kind == "FULLRESYNC"))
finally:
    c.close()
