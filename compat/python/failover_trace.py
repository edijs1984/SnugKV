import os
import sys
import time
from pathlib import Path

from redis.cluster import RedisCluster, ClusterNode

p0 = int(os.getenv("P0", "7120"))
p1 = int(os.getenv("P1", "7121"))
p2 = int(os.getenv("P2", "7122"))
password = os.getenv("PASSWORD", "cluster-failover-secret")
state_dir = Path(os.getenv("CLIENT_STATE_DIR", "/tmp/snugkv-d-failover-python"))
state_dir.mkdir(parents=True, exist_ok=True)

ready = state_dir / "ready"
proceed = state_dir / "post-failover"
passed = state_dir / "passed"
failed = state_dir / "failed"


def retry(label, fn, timeout=15.0):
    deadline = time.monotonic() + timeout
    last = None
    attempts = 0
    while time.monotonic() < deadline:
        attempts += 1
        try:
            value = fn()
            print(f"[{label}] success after {attempts} attempt(s)", flush=True)
            return value
        except Exception as exc:
            last = exc
            print(f"[{label}] attempt {attempts}: {exc}", flush=True)
            time.sleep(0.1)
    raise RuntimeError(f"{label}: {last or 'timeout'}")


def main():
    client = RedisCluster(
        startup_nodes=[
            ClusterNode("127.0.0.1", p0),
            ClusterNode("127.0.0.1", p1),
            ClusterNode("127.0.0.1", p2),
        ],
        password=password,
        decode_responses=True,
        socket_connect_timeout=2,
        socket_timeout=2,
        retry_on_timeout=True,
    )
    try:
        retry(
            "redis-py preflight",
            lambda: (
                client.set("d:failover:{3}:py", "before"),
                client.get("d:failover:{3}:py") == "before" or (_ for _ in ()).throw(RuntimeError("bad value")),
            ),
        )
        ready.write_text("ready\n")
        while not proceed.exists():
            time.sleep(0.05)

        def recovered():
            client.set("d:failover:{3}:py", "after")
            if client.get("d:failover:{3}:py") != "after":
                raise RuntimeError("bad value")
            return True

        retry("redis-py recovery", recovered)
        passed.write_text("PASS\n")
        print("persistent redis-py failover/reconnect: PASS", flush=True)
    finally:
        client.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        failed.write_text(repr(exc))
        print(exc, file=sys.stderr, flush=True)
        raise
