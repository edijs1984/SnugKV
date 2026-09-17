#!/usr/bin/env python3

import os
import socket
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path.cwd()
SERVER_PROCESS = None


class TestFailure(Exception):
    pass


class RedisError(Exception):
    pass


class RedisErrorValue:
    """Represents an error contained inside a RESP array."""
    def __init__(self, message):
        self.message = message

    def __repr__(self):
        return f"RedisErrorValue({self.message!r})"


def section(title):
    print("\n" + "=" * 78)
    print(title)
    print("=" * 78)


def run(cmd, *, env=None):
    print(f"\n$ {' '.join(cmd)}")
    result = subprocess.run(
        cmd,
        cwd=ROOT,
        env=env,
        text=True,
    )
    if result.returncode != 0:
        raise TestFailure(
            f"Command failed with exit code {result.returncode}: "
            f"{' '.join(cmd)}"
        )


def assert_equal(actual, expected, message):
    if actual != expected:
        raise TestFailure(
            f"{message}\n"
            f"  expected: {expected!r}\n"
            f"  actual:   {actual!r}"
        )
    print(f"  PASS: {message}")


def assert_redis_error(fn, contains, message):
    try:
        fn()
    except RedisError as exc:
        if contains.lower() not in str(exc).lower():
            raise TestFailure(
                f"{message}\n"
                f"  expected error containing: {contains!r}\n"
                f"  actual error: {str(exc)!r}"
            )
        print(f"  PASS: {message} -> {exc}")
        return

    raise TestFailure(f"{message}: expected Redis error")


def find_free_port():
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


class RedisClient:
    def __init__(self, host, port):
        self.sock = socket.create_connection((host, port), timeout=5)
        self.file = self.sock.makefile("rb")

    def close(self):
        try:
            self.file.close()
        except Exception:
            pass
        try:
            self.sock.close()
        except Exception:
            pass

    def read_line(self):
        line = self.file.readline()
        if not line:
            raise ConnectionError("SnugKV closed connection")

        if not line.endswith(b"\r\n"):
            raise ConnectionError(f"Invalid RESP line: {line!r}")

        return line[:-2]

    def read_response(self, nested=False):
        prefix = self.file.read(1)

        if not prefix:
            raise ConnectionError("SnugKV closed connection")

        if prefix == b"+":
            return self.read_line().decode(errors="replace")

        if prefix == b"-":
            message = self.read_line().decode(errors="replace")

            # Errors inside EXEC arrays are array values, not top-level errors.
            if nested:
                return RedisErrorValue(message)

            raise RedisError(message)

        if prefix == b":":
            return int(self.read_line())

        if prefix == b"$":
            length = int(self.read_line())

            if length == -1:
                return None

            data = self.file.read(length)

            if len(data) != length:
                raise ConnectionError("Short bulk read")

            if self.file.read(2) != b"\r\n":
                raise ConnectionError("Invalid bulk terminator")

            return data.decode(errors="replace")

        if prefix == b"*":
            count = int(self.read_line())

            if count == -1:
                return None

            # Important: fully consume every element, including errors.
            return [
                self.read_response(nested=True)
                for _ in range(count)
            ]

        raise ConnectionError(f"Unknown RESP prefix: {prefix!r}")

    def command(self, *args):
        parts = [f"*{len(args)}\r\n".encode()]

        for arg in args:
            raw = arg if isinstance(arg, bytes) else str(arg).encode()

            parts.append(f"${len(raw)}\r\n".encode())
            parts.append(raw)
            parts.append(b"\r\n")

        self.sock.sendall(b"".join(parts))
        return self.read_response()


def wait_for_server(port, timeout=20):
    deadline = time.time() + timeout

    while time.time() < deadline:
        try:
            c = RedisClient("127.0.0.1", port)
            result = c.command("PING")
            c.close()

            if result == "PONG":
                return

        except Exception:
            time.sleep(0.1)

    raise TestFailure("SnugKV failed to start")


def start_server():
    global SERVER_PROCESS

    port = find_free_port()
    log_path = ROOT / ".transaction-test-server.log"

    env = os.environ.copy()
    env["SNUGKV_ENCODING"] = "true"
    env["SNUGKV_COMPRESSION"] = "true"

    log_file = open(log_path, "w")

    print(f"Starting isolated SnugKV on port {port}")
    print(f"Server log: {log_path}")

    SERVER_PROCESS = subprocess.Popen(
        [
            "go",
            "run",
            "./cmd/snugkv",
            f"--listen=127.0.0.1:{port}",
            "--admin-listen=",
        ],
        cwd=ROOT,
        env=env,
        stdout=log_file,
        stderr=subprocess.STDOUT,
        text=True,
    )

    try:
        wait_for_server(port)
    except Exception:
        log_file.flush()
        log_file.close()

        print("\n--- SnugKV server log ---")
        if log_path.exists():
            print(log_path.read_text())

        raise

    return port, log_file


def stop_server(log_file):
    global SERVER_PROCESS

    if SERVER_PROCESS is not None:
        SERVER_PROCESS.terminate()

        try:
            SERVER_PROCESS.wait(timeout=5)
        except subprocess.TimeoutExpired:
            SERVER_PROCESS.kill()
            SERVER_PROCESS.wait(timeout=5)

    log_file.close()
    SERVER_PROCESS = None


def test_multi_exec(port):
    section("LIVE TEST: MULTI / EXEC")

    c = RedisClient("127.0.0.1", port)

    try:
        c.command("DEL", "tx-a", "tx-b")

        assert_equal(c.command("MULTI"), "OK", "MULTI")
        assert_equal(c.command("SET", "tx-a", "1"), "QUEUED", "queue SET")
        assert_equal(c.command("GET", "tx-a"), "QUEUED", "queue GET")
        assert_equal(c.command("SET", "tx-b", "2"), "QUEUED", "queue second SET")

        assert_equal(
            c.command("EXEC"),
            ["OK", "1", "OK"],
            "EXEC result array",
        )

        assert_equal(
            c.command("MGET", "tx-a", "tx-b"),
            ["1", "2"],
            "transaction committed both keys",
        )

    finally:
        c.close()


def test_execabort(port):
    section("LIVE TEST: queue-time error -> EXECABORT")

    c = RedisClient("127.0.0.1", port)

    try:
        c.command("DEL", "tx-q1", "tx-q2")

        assert_equal(c.command("MULTI"), "OK", "MULTI")
        assert_equal(c.command("SET", "tx-q1", "one"), "QUEUED", "queue first SET")

        assert_redis_error(
            lambda: c.command("INCR", "tx-q1", "extra"),
            "wrong number",
            "invalid command poisons transaction",
        )

        assert_equal(c.command("SET", "tx-q2", "two"), "QUEUED", "queue second SET")

        assert_redis_error(
            lambda: c.command("EXEC"),
            "EXECABORT",
            "EXEC aborts after queue-time error",
        )

        assert_equal(
            c.command("MGET", "tx-q1", "tx-q2"),
            [None, None],
            "aborted transaction wrote nothing",
        )

    finally:
        c.close()


def test_runtime_error(port):
    section("LIVE TEST: runtime error does not stop EXEC")

    c = RedisClient("127.0.0.1", port)

    try:
        c.command("SET", "tx-wrong", "string")
        c.command("DEL", "tx-after")

        assert_equal(c.command("MULTI"), "OK", "MULTI")
        assert_equal(
            c.command("LPUSH", "tx-wrong", "item"),
            "QUEUED",
            "queue LPUSH",
        )
        assert_equal(
            c.command("SET", "tx-after", "yes"),
            "QUEUED",
            "queue SET",
        )

        result = c.command("EXEC")

        if not isinstance(result, list) or len(result) != 2:
            raise TestFailure(
                f"Unexpected EXEC response: {result!r}"
            )

        if not isinstance(result[0], RedisErrorValue):
            raise TestFailure(
                f"First EXEC element should be an error: {result!r}"
            )

        if "WRONGTYPE" not in result[0].message:
            raise TestFailure(
                f"Expected WRONGTYPE, got {result[0].message!r}"
            )

        print(
            "  PASS: EXEC contains runtime error -> "
            + result[0].message
        )

        assert_equal(
            result[1],
            "OK",
            "later command still executed inside EXEC",
        )

        assert_equal(
            c.command("GET", "tx-after"),
            "yes",
            "later command value persisted",
        )

    finally:
        c.close()


def test_discard(port):
    section("LIVE TEST: DISCARD")

    c = RedisClient("127.0.0.1", port)

    try:
        c.command("DEL", "discarded")

        assert_equal(c.command("MULTI"), "OK", "MULTI")
        assert_equal(
            c.command("SET", "discarded", "1"),
            "QUEUED",
            "queued SET",
        )

        assert_equal(c.command("DISCARD"), "OK", "DISCARD")

        assert_equal(
            c.command("GET", "discarded"),
            None,
            "DISCARD prevented mutation",
        )

    finally:
        c.close()


def test_blocking_inside_exec(port):
    section("LIVE TEST: blocking command inside EXEC")

    c = RedisClient("127.0.0.1", port)

    try:
        c.command("DEL", "tx-empty")

        assert_equal(c.command("MULTI"), "OK", "MULTI")

        assert_equal(
            c.command("BLPOP", "tx-empty", "0"),
            "QUEUED",
            "BLPOP queued",
        )

        start = time.time()
        result = c.command("EXEC")
        elapsed = time.time() - start

        assert_equal(
            result,
            [None],
            "BLPOP returns nil instead of blocking inside EXEC",
        )

        if elapsed > 1.0:
            raise TestFailure(
                f"EXEC blocked for {elapsed:.3f}s"
            )

        print(f"  PASS: EXEC returned in {elapsed:.4f}s")

    finally:
        c.close()


def test_watch_other_client(port):
    section("LIVE TEST: WATCH invalidated by another client")

    a = RedisClient("127.0.0.1", port)
    b = RedisClient("127.0.0.1", port)

    try:
        assert_equal(
            a.command("SET", "watched", "original"),
            "OK",
            "seed watched key",
        )

        assert_equal(
            a.command("WATCH", "watched"),
            "OK",
            "client A WATCH",
        )

        assert_equal(
            b.command("SET", "watched", "changed"),
            "OK",
            "client B modifies watched key",
        )

        assert_equal(
            a.command("MULTI"),
            "OK",
            "client A MULTI",
        )

        assert_equal(
            a.command("GET", "watched"),
            "QUEUED",
            "client A queues GET",
        )

        assert_equal(
            a.command("EXEC"),
            None,
            "EXEC aborted because watched key changed",
        )

    finally:
        a.close()
        b.close()


def test_watch_change_restore(port):
    section("LIVE TEST: WATCH detects change -> restore")

    a = RedisClient("127.0.0.1", port)
    b = RedisClient("127.0.0.1", port)

    try:
        assert_equal(
            a.command("SET", "watched", "base"),
            "OK",
            "set base value",
        )

        assert_equal(
            a.command("WATCH", "watched"),
            "OK",
            "WATCH base value",
        )

        assert_equal(
            b.command("SET", "watched", "temporary"),
            "OK",
            "other client changes value",
        )

        assert_equal(
            b.command("SET", "watched", "base"),
            "OK",
            "other client restores original value",
        )

        assert_equal(a.command("MULTI"), "OK", "MULTI")
        assert_equal(a.command("PING"), "QUEUED", "queue PING")

        assert_equal(
            a.command("EXEC"),
            None,
            "change -> restore still invalidates WATCH",
        )

    finally:
        a.close()
        b.close()


def test_unwatch(port):
    section("LIVE TEST: UNWATCH")

    a = RedisClient("127.0.0.1", port)
    b = RedisClient("127.0.0.1", port)

    try:
        a.command("SET", "watched", "before")

        assert_equal(
            a.command("WATCH", "watched"),
            "OK",
            "WATCH",
        )

        assert_equal(
            b.command("SET", "watched", "after"),
            "OK",
            "other client modifies watched key",
        )

        assert_equal(
            a.command("UNWATCH"),
            "OK",
            "UNWATCH",
        )

        assert_equal(
            a.command("MULTI"),
            "OK",
            "MULTI",
        )

        assert_equal(
            a.command("PING"),
            "QUEUED",
            "PING queued",
        )

        assert_equal(
            a.command("EXEC"),
            ["PONG"],
            "EXEC succeeds after UNWATCH",
        )

    finally:
        a.close()
        b.close()


def main():
    global SERVER_PROCESS

    log_file = None

    try:
        section("REPOSITORY")

        if not (ROOT / "go.mod").exists():
            raise TestFailure(
                "Run this script from the SnugKV repository root"
            )

        run(["git", "status", "--short"])

        print("\nCurrent commit:")
        run(["git", "rev-parse", "HEAD"])

        section("FOCUSED GO TRANSACTION TESTS")

        run([
            "go",
            "test",
            "-race",
            "-count=1",
            "-v",
            "./internal/server",
            "-run",
            "^Test(TCPTransaction|TransactionAOF)",
        ])

        section("FULL GO RACE SUITE")

        run([
            "go",
            "test",
            "-race",
            "-count=1",
            "./...",
        ])

        section("GO VET")

        run([
            "go",
            "vet",
            "./...",
        ])

        section("RESP FUZZ")

        run([
            "go",
            "test",
            "./internal/resp",
            "-run",
            "^$",
            "-fuzz",
            "FuzzReadCommand",
            "-fuzztime=10s",
        ])

        section("START ISOLATED SNUGKV")

        port, log_file = start_server()

        print(f"SnugKV ready on 127.0.0.1:{port}")

        test_multi_exec(port)
        test_execabort(port)
        test_runtime_error(port)
        test_discard(port)
        test_blocking_inside_exec(port)
        test_watch_other_client(port)
        test_watch_change_restore(port)
        test_unwatch(port)

        section("FINAL RESULT")

        print("ALL TRANSACTION TESTS PASSED ✅")
        return 0

    except TestFailure as exc:
        print("\n" + "!" * 78)
        print("TEST FAILED ❌")
        print("!" * 78)
        print(exc)

        if (ROOT / ".transaction-test-server.log").exists():
            print("\nServer log tail:")
            try:
                lines = (
                    ROOT /
                    ".transaction-test-server.log"
                ).read_text().splitlines()

                for line in lines[-20:]:
                    print(line)
            except Exception:
                pass

        return 1

    finally:
        if log_file is not None:
            stop_server(log_file)
        elif SERVER_PROCESS is not None:
            try:
                SERVER_PROCESS.terminate()
            except Exception:
                pass


if __name__ == "__main__":
    sys.exit(main())
