#!/usr/bin/env python3
import argparse
import os
import select
import socket
import threading
import time

def parse_resp_command(buf):
    if not buf.startswith(b"*"):
        return None
    try:
        pos = buf.index(b"\r\n")
        count = int(buf[1:pos])
        pos += 2
        parts = []
        for _ in range(count):
            if pos >= len(buf) or buf[pos:pos+1] != b"$":
                return None
            end = buf.index(b"\r\n", pos)
            n = int(buf[pos+1:end])
            pos = end + 2
            if pos + n + 2 > len(buf):
                return None
            parts.append(buf[pos:pos+n])
            pos += n
            if buf[pos:pos+2] != b"\r\n":
                return None
            pos += 2
        return parts, pos
    except (ValueError, IndexError):
        return None

def blocked_users(path):
    try:
        with open(path, "r", encoding="utf-8") as f:
            return {line.strip() for line in f if line.strip()}
    except FileNotFoundError:
        return set()

def classify_identity(buffer):
    offset = 0
    while offset < len(buffer):
        parsed = parse_resp_command(buffer[offset:])
        if parsed is None:
            return None
        parts, used = parsed
        offset += used
        if not parts:
            continue
        cmd = parts[0].upper()
        if cmd == b"AUTH":
            if len(parts) == 3:
                return parts[1].decode("utf-8", "replace")
            if len(parts) == 2:
                return "default"
        # HELLO ... AUTH username password is also accepted by Redis clients.
        if cmd == b"HELLO":
            for i in range(1, len(parts) - 2):
                if parts[i].upper() == b"AUTH":
                    return parts[i+1].decode("utf-8", "replace")
        # Unauthenticated commands are ordinary client traffic for this harness.
        if cmd not in (b"PING", b"REPLCONF", b"PSYNC"):
            return "default"
    return None

def relay(client, target_host, target_port, block_file):
    upstream = None
    try:
        upstream = socket.create_connection((target_host, target_port), timeout=2.0)
        client.setblocking(False)
        upstream.setblocking(False)

        identity = None
        sniff = bytearray()
        to_upstream = bytearray()
        to_client = bytearray()

        while True:
            if identity is not None and identity in blocked_users(block_file):
                return

            read_list = []
            write_list = []

            # Bound queued data so a slow destination applies backpressure
            # without dropping the connection.
            if len(to_upstream) < 1024 * 1024:
                read_list.append(client)
            if len(to_client) < 1024 * 1024:
                read_list.append(upstream)
            if to_upstream:
                write_list.append(upstream)
            if to_client:
                write_list.append(client)

            readable, writable, exceptional = select.select(
                read_list, write_list, [client, upstream], 0.1
            )
            if exceptional:
                return

            if client in readable:
                try:
                    data = client.recv(65536)
                except BlockingIOError:
                    data = None
                if data == b"":
                    return
                if data:
                    if identity is None:
                        sniff.extend(data)
                        identity = classify_identity(bytes(sniff))
                        if identity is not None and identity in blocked_users(block_file):
                            return
                        if len(sniff) > 65536 and identity is None:
                            identity = "default"
                    to_upstream.extend(data)

            if upstream in readable:
                try:
                    data = upstream.recv(65536)
                except BlockingIOError:
                    data = None
                if data == b"":
                    return
                if data:
                    to_client.extend(data)

            if upstream in writable and to_upstream:
                try:
                    sent = upstream.send(to_upstream)
                except BlockingIOError:
                    sent = 0
                if sent:
                    del to_upstream[:sent]

            if client in writable and to_client:
                try:
                    sent = client.send(to_client)
                except BlockingIOError:
                    sent = 0
                if sent:
                    del to_client[:sent]
    finally:
        try:
            client.close()
        except Exception:
            pass
        if upstream is not None:
            try:
                upstream.close()
            except Exception:
                pass

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--listen-port", type=int, required=True)
    ap.add_argument("--target-port", type=int, required=True)
    ap.add_argument("--block-file", required=True)
    args = ap.parse_args()

    os.makedirs(os.path.dirname(args.block_file), exist_ok=True)
    if not os.path.exists(args.block_file):
        open(args.block_file, "a").close()

    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", args.listen_port))
    listener.listen(128)

    while True:
        client, _ = listener.accept()
        t = threading.Thread(
            target=relay,
            args=(client, "127.0.0.1", args.target_port, args.block_file),
            daemon=True,
        )
        t.start()

if __name__ == "__main__":
    main()
