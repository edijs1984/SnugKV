#!/usr/bin/env python3
import os
import socket
import struct

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
PORT = int(os.environ.get("PRIMARY_PORT", "6395"))
OUT = os.environ.get("RDB_OUT", "/tmp/redis-repl-fullsync.rdb")


def resp_command(sock, *args):
    out = [("*%d\r\n" % len(args)).encode()]
    for arg in args:
        if not isinstance(arg, (bytes, bytearray)):
            arg = str(arg).encode()
        out += [("$%d\r\n" % len(arg)).encode(), bytes(arg), b"\r\n"]
    sock.sendall(b"".join(out))


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
    if p == b"*":
        n = int(reader.readline()[:-2])
        return [read_resp(reader) for _ in range(n)]
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


def read_fullsync_rdb(sock, reader):
    line = reader.readline()
    if not line.startswith(b"+FULLRESYNC "):
        raise RuntimeError("expected FULLRESYNC, got %r" % line)
    parts = line.strip().split()
    replid = parts[1].decode()
    offset = int(parts[2])

    header = reader.readline()
    if not header.startswith(b"$"):
        raise RuntimeError("expected RDB bulk header, got %r" % header)

    # We deliberately do not advertise EOF capability, so Redis should use a
    # length-prefixed snapshot that is easiest to compare and validate.
    if header.startswith(b"$EOF:"):
        raise RuntimeError("unexpected EOF-framed RDB; oracle did not request EOF capability")

    n = int(header[1:-2])
    if n <= 0:
        raise RuntimeError("empty RDB payload")
    rdb = reader.read(n)
    if len(rdb) != n:
        raise RuntimeError("truncated RDB payload")
    return replid, offset, rdb


control = socket.create_connection((HOST, PORT), timeout=5)
control_reader = control.makefile("rb")

try:
    command(control, control_reader, "FLUSHALL")

    command(control, control_reader, "SET", "rdb:string", "hello")
    command(control, control_reader, "SET", "rdb:ttl", "expires")
    command(control, control_reader, "PEXPIRE", "rdb:ttl", "600000")
    command(control, control_reader, "HSET", "rdb:hash", "a", "1", "b", "two")
    command(control, control_reader, "SADD", "rdb:set", "1", "2", "3")
    command(control, control_reader, "RPUSH", "rdb:list", "a", "b", "c")
    command(control, control_reader, "ZADD", "rdb:zset", "1.5", "a", "2", "b")
    command(control, control_reader, "XADD", "rdb:stream", "1-0", "field", "value")

    repl = socket.create_connection((HOST, PORT), timeout=10)
    repl_reader = repl.makefile("rb")
    try:
        # No "capa eof" on purpose: force the ordinary length-prefixed RDB path.
        resp_command(repl, "REPLCONF", "listening-port", "0")
        read_resp(repl_reader)
        resp_command(repl, "REPLCONF", "capa", "psync2")
        read_resp(repl_reader)
        resp_command(repl, "PSYNC", "?", "-1")

        replid, offset, rdb = read_fullsync_rdb(repl, repl_reader)
    finally:
        repl_reader.close()
        repl.close()

    with open(OUT, "wb") as f:
        f.write(rdb)

    magic = rdb[:5]
    version_raw = rdb[5:9]
    version = int(version_raw) if len(version_raw) == 4 and version_raw.isdigit() else -1

    checksum_present = len(rdb) >= 8
    checksum_matches = False
    if checksum_present:
        stored = struct.unpack("<Q", rdb[-8:])[0]
        checksum_matches = stored == crc64_redis(rdb[:-8])

    print("=== full sync RDB envelope ===")
    print("replid_len=" + repr(len(replid)))
    print("offset_is_integer=True")
    print("snapshot_mode='bulk'")
    print("rdb_nonempty=" + repr(bool(rdb)))
    print("rdb_magic=" + repr(magic.decode(errors="replace")))
    print("rdb_version=" + repr(version))
    print("checksum_matches=" + repr(checksum_matches))
    print("snapshot_bytes=" + repr(len(rdb)))
    print("saved_to=" + repr(OUT))

    print("\n=== source dataset ===")
    print("string=" + repr(command(control, control_reader, "GET", "rdb:string").decode()))
    print("hash_len=" + repr(command(control, control_reader, "HLEN", "rdb:hash")))
    print("set_len=" + repr(command(control, control_reader, "SCARD", "rdb:set")))
    print("list_len=" + repr(command(control, control_reader, "LLEN", "rdb:list")))
    print("zset_len=" + repr(command(control, control_reader, "ZCARD", "rdb:zset")))
    print("stream_len=" + repr(command(control, control_reader, "XLEN", "rdb:stream")))
    ttl = command(control, control_reader, "PTTL", "rdb:ttl")
    print("ttl_positive=" + repr(isinstance(ttl, int) and ttl > 0))
finally:
    control_reader.close()
    control.close()
