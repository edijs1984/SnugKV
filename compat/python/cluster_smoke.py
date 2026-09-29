import os
import sys

import redis
from redis.cluster import RedisCluster

HOST = os.getenv("REDIS_HOST", "127.0.0.1")
PORT = int(os.getenv("REDIS_PORT", "7000"))


def check(condition, message):
    if not condition:
        raise AssertionError(message)


client = RedisCluster(
    host=HOST,
    port=PORT,
    decode_responses=True,
    socket_connect_timeout=3,
    socket_timeout=3,
)

try:
    check(client.ping() is True, "PING failed")

    # Different hash tags deliberately route to different shards.
    client.set("pycluster:{1}:a", "one")
    client.set("pycluster:{2}:b", "two")
    client.set("pycluster:{3}:c", "three")

    check(client.get("pycluster:{1}:a") == "one", "GET shard 1 failed")
    check(client.get("pycluster:{2}:b") == "two", "GET shard 2 failed")
    check(client.get("pycluster:{3}:c") == "three", "GET shard 3 failed")

    # Multi-key operations remain valid when all keys share a hash tag.
    check(
        client.mset({
            "pycluster:{42}:a": "A",
            "pycluster:{42}:b": "B",
        }) is True,
        "same-slot MSET failed",
    )
    check(
        client.mget("pycluster:{42}:a", "pycluster:{42}:b") == ["A", "B"],
        "same-slot MGET failed",
    )

    # Cluster pipelines should partition commands by destination node.
    pipe = client.pipeline(transaction=False)
    pipe.set("pycluster:{11}:p", "P1")
    pipe.set("pycluster:{12}:p", "P2")
    pipe.set("pycluster:{13}:p", "P3")
    check(pipe.execute() == [True, True, True], "cross-shard pipeline SET failed")

    pipe = client.pipeline(transaction=False)
    pipe.get("pycluster:{11}:p")
    pipe.get("pycluster:{12}:p")
    pipe.get("pycluster:{13}:p")
    check(pipe.execute() == ["P1", "P2", "P3"], "cross-shard pipeline GET failed")

    print("redis-py cluster: PASS")
finally:
    client.close()
