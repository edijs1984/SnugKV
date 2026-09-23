#!/usr/bin/env python3
import os
import socket
import time

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
PORT = int(os.environ.get("PRIMARY_PORT", "6395"))


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

    def read_line(self):
        b = self.f.readline()
        if not b:
            raise EOFError("closed")
        return b[:-2]

    def read(self):
        p = self.f.read(1)
        if p == b"+":
            return ("simple", self.read_line().decode(errors="replace"))
        if p == b"-":
            return ("error", self.read_line().decode(errors="replace"))
        if p == b":":
            return ("int", int(self.read_line()))
        if p == b"$":
            n = int(self.read_line())
            if n < 0:
                return ("bulk", None)
            d = self.f.read(n)
            self.f.read(2)
            return ("bulk", d.decode(errors="replace"))
        if p == b"*":
            n = int(self.read_line())
            if n < 0:
                return ("array", None)
            return ("array", [self.read() for _ in range(n)])
        raise RuntimeError(repr(p))


def scalar(reply):
    if reply[0] in ("simple", "bulk", "int"):
        return reply[1]
    raise TypeError(reply)


def info(conn):
    body = scalar(conn.command("INFO", "replication"))
    out = {}
    for line in body.splitlines():
        if ":" in line and not line.startswith("#"):
            k, v = line.split(":", 1)
            out[k] = v
    return out


def parse_replica(line):
    if not line:
        return {}
    out = {}
    for part in line.split(","):
        if "=" in part:
            k, v = part.split("=", 1)
            out[k] = v
    return out


def send_command(sock, *args):
    out = [("*%d\r\n" % len(args)).encode()]
    for arg in args:
        raw = str(arg).encode()
        out += [("$%d\r\n" % len(raw)).encode(), raw, b"\r\n"]
    sock.sendall(b"".join(out))


def consume_snapshot(reader):
    header = reader.readline()
    if not header:
        raise EOFError("missing snapshot header")
    header = header.decode(errors="replace").strip()

    if header.startswith("$EOF:"):
        marker = header[5:].encode()
        buf = bytearray()
        keep = max(1, len(marker) - 1)
        while True:
            chunk = reader.read(65536)
            if not chunk:
                raise EOFError("diskless snapshot truncated")
            buf.extend(chunk)
            pos = buf.find(marker)
            if pos >= 0:
                return "eof"
            if len(buf) > keep:
                del buf[:len(buf)-keep]

    if not header.startswith("$"):
        raise RuntimeError("bad snapshot header %r" % header)

    n = int(header[1:])
    remaining = n
    while remaining:
        chunk = reader.read(min(65536, remaining))
        if not chunk:
            raise EOFError("snapshot truncated")
        remaining -= len(chunk)
    return "bulk"


control = RedisConn(PORT)
replica_sock = socket.create_connection((HOST, PORT), timeout=3)
replica_reader = replica_sock.makefile("rb")

try:
    scalar(control.command("FLUSHALL"))

    send_command(replica_sock, "PSYNC", "?", "-1")
    first = replica_reader.readline().decode(errors="replace").strip()
    if not first.startswith("+FULLRESYNC "):
        raise RuntimeError("expected FULLRESYNC, got %r" % first)

    snapshot_mode = consume_snapshot(replica_reader)

    deadline = time.time() + 10
    first_replica = {}
    primary = {}
    while time.time() < deadline:
        primary = info(control)
        first_replica = parse_replica(primary.get("slave0", primary.get("replica0", "")))
        if first_replica.get("state") == "online":
            break
        time.sleep(0.05)

    print("=== replica visible after full sync ===")
    print("replica_visible=" + repr(bool(first_replica)))
    print("replica_online=" + repr(first_replica.get("state") == "online"))
    print("snapshot_mode=" + repr(snapshot_mode))
    print("replica_offset_is_integer=" + repr(first_replica.get("offset", "").lstrip("-").isdigit()))
    print("replica_lag_is_integer=" + repr(first_replica.get("lag", "").isdigit()))

    scalar(control.command("SET", "ack:a", "1"))
    scalar(control.command("INCR", "ack:counter"))
    current = int(info(control)["master_repl_offset"])

    print("\n=== explicit ACK ===")
    send_command(replica_sock, "REPLCONF", "ACK", str(current))

    deadline = time.time() + 3
    after_ack = {}
    while time.time() < deadline:
        state = info(control)
        after_ack = parse_replica(state.get("slave0", state.get("replica0", "")))
        if after_ack.get("offset") == str(current):
            break
        time.sleep(0.02)

    print("ack_offset_matches_master=" + repr(after_ack.get("offset") == str(current)))
    print("ack_lag_fresh=" + repr(after_ack.get("lag", "").isdigit() and int(after_ack["lag"]) <= 1))

    print("\n=== stale ACK is monotonic ===")
    send_command(replica_sock, "REPLCONF", "ACK", "0")
    time.sleep(0.05)
    stale = info(control)
    stale_replica = parse_replica(stale.get("slave0", stale.get("replica0", "")))
    print("stale_ack_ignored=" + repr(stale_replica.get("offset") == str(current)))
finally:
    try:
        replica_reader.close()
    except Exception:
        pass
    try:
        replica_sock.close()
    except Exception:
        pass
    control.close()
