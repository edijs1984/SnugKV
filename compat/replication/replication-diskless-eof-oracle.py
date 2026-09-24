#!/usr/bin/env python3
import os
import socket
import struct

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
PORT = int(os.environ.get("PRIMARY_PORT", "6395"))
OUT = os.environ.get("RDB_OUT", "/tmp/redis-repl-eof.rdb")


def resp_command(sock, *args):
    parts = [("*%d\r\n" % len(args)).encode()]
    for arg in args:
        if not isinstance(arg, (bytes, bytearray)):
            arg = str(arg).encode()
        parts += [("$%d\r\n" % len(arg)).encode(), bytes(arg), b"\r\n"]
    sock.sendall(b"".join(parts))


def read_resp(reader):
    p = reader.read(1)
    if p == b"+":
        return reader.readline()[:-2].decode()
    if p == b"-":
        raise RuntimeError(reader.readline()[:-2].decode())
    if p == b":":
        return int(reader.readline()[:-2])
    if p == b"$":
        n = int(reader.readline()[:-2])
        if n < 0:
            return None
        data = reader.read(n)
        if reader.read(2) != b"\r\n":
            raise RuntimeError("bad bulk terminator")
        return data
    raise RuntimeError("unsupported RESP prefix %r" % p)


def command(sock, reader, *args):
    resp_command(sock, *args)
    return read_resp(reader)


def crc64_redis(data):
    poly = 0x95AC9329AC4BC9B5
    table = []
    for i in range(256):
        crc = i
        for _ in range(8):
            crc = (crc >> 1) ^ poly if (crc & 1) else (crc >> 1)
        table.append(crc & 0xFFFFFFFFFFFFFFFF)
    crc = 0
    for b in data:
        crc = table[(crc ^ b) & 0xFF] ^ (crc >> 8)
    return crc & 0xFFFFFFFFFFFFFFFF


def read_until_marker(reader, marker, limit=128 << 20):
    out = bytearray()
    window = bytearray()
    while True:
        b = reader.read(1)
        if not b:
            raise RuntimeError("EOF before diskless marker")
        window += b
        if len(window) > len(marker):
            out.append(window[0])
            del window[0]
            if len(out) > limit:
                raise RuntimeError("RDB exceeds limit")
        if len(window) == len(marker) and bytes(window) == marker:
            return bytes(out)


control = socket.create_connection((HOST, PORT), timeout=5)
control_reader = control.makefile("rb")
try:
    command(control, control_reader, "FLUSHALL")
    command(control, control_reader, "SET", "eof:string", "hello")
    command(control, control_reader, "SET", "eof:ttl", "alive", "PX", "3600000")

    repl = socket.create_connection((HOST, PORT), timeout=10)
    repl_reader = repl.makefile("rb")
    try:
        resp_command(repl, "REPLCONF", "capa", "eof")
        capa = read_resp(repl_reader)
        resp_command(repl, "PSYNC", "?", "-1")

        full = repl_reader.readline()
        if not full.startswith(b"+FULLRESYNC "):
            raise RuntimeError("expected FULLRESYNC, got %r" % full)

        header = repl_reader.readline()
        if not header.startswith(b"$EOF:"):
            raise RuntimeError("expected EOF-framed RDB, got %r" % header)

        marker = header[5:-2]
        if len(marker) != 40:
            raise RuntimeError("bad EOF marker length %d" % len(marker))

        rdb = read_until_marker(repl_reader, marker)
    finally:
        repl_reader.close()
        repl.close()

    with open(OUT, "wb") as f:
        f.write(rdb)

    version_raw = rdb[5:9]
    version = int(version_raw) if len(version_raw) == 4 and version_raw.isdigit() else -1
    stored = struct.unpack("<Q", rdb[-8:])[0] if len(rdb) >= 8 else None
    checksum_matches = stored is not None and stored == crc64_redis(rdb[:-8])

    print("=== diskless EOF full sync ===")
    print("replconf_capa_eof=" + repr(capa))
    print("snapshot_mode='eof'")
    print("marker_len=" + repr(len(marker)))
    print("rdb_magic=" + repr(rdb[:5].decode(errors="replace")))
    print("rdb_version=" + repr(version))
    print("checksum_matches=" + repr(checksum_matches))
    print("snapshot_bytes=" + repr(len(rdb)))
    print("saved_to=" + repr(OUT))

    print("\n=== source dataset ===")
    print("string=" + repr(command(control, control_reader, "GET", "eof:string").decode()))
    ttl = command(control, control_reader, "PTTL", "eof:ttl")
    print("ttl_positive=" + repr(isinstance(ttl, int) and ttl > 0))
finally:
    control_reader.close()
    control.close()
