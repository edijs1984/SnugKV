import os
import time
import redis

HOST = os.getenv("REDIS_HOST", "127.0.0.1")
PORT = int(os.getenv("REDIS_PORT", "6380"))
TARGET = os.getenv("TARGET_NAME", "snugkv")
prefix = f"compat:resp3:python:{int(time.time() * 1000)}"


def client():
    return redis.Redis(
        host=HOST,
        port=PORT,
        decode_responses=True,
        protocol=3,
        socket_connect_timeout=3,
    )


r = client()
print(f"RESP3 redis-py smoke target={TARGET} {HOST}:{PORT}")

assert r.ping() is True
r.set(prefix + ":string", "hello")
assert r.get(prefix + ":string") == "hello"
assert r.get(prefix + ":missing") is None

r.mset({prefix + ":a": "one", prefix + ":b": "two"})
assert r.mget(prefix + ":a", prefix + ":b") == ["one", "two"]

r.hset(prefix + ":hash", mapping={"a": "1", "b": "2"})
assert r.hgetall(prefix + ":hash") == {"a": "1", "b": "2"}

r.sadd(prefix + ":set", "a", "b")
assert r.smembers(prefix + ":set") == {"a", "b"}

r.zadd(prefix + ":z", {"a": 1.5, "b": 2.25})
assert r.zscore(prefix + ":z", "a") == 1.5

stream = prefix + ":stream"
assert r.xadd(stream, {"f": "one"}, id="1-0") == "1-0"
entries = r.xrange(stream, "-", "+")
assert len(entries) == 1 and entries[0][0] == "1-0"

assert r.xgroup_create(stream, "g", id="0") is True
messages = r.xreadgroup("g", "c", {stream: ">"}, count=1)
assert messages
summary = r.xpending(stream, "g")
assert summary is not None
groups = r.xinfo_groups(stream)
assert len(groups) == 1

pipe = r.pipeline(transaction=False)
pipe.set(prefix + ":p1", "x")
pipe.get(prefix + ":p1")
assert pipe.execute() == [True, "x"]

tx = r.pipeline(transaction=True)
tx.set(prefix + ":tx", "ok")
tx.get(prefix + ":tx")
assert tx.execute() == [True, "ok"]

r.set(prefix + ":wrong", "x")
try:
    r.hgetall(prefix + ":wrong")
    raise AssertionError("WRONGTYPE was not raised")
except redis.ResponseError as exc:
    assert "WRONGTYPE" in str(exc)

r.connection_pool.disconnect()

r2 = client()
assert r2.get(prefix + ":string") == "hello"
assert r2.ping() is True
r2.connection_pool.disconnect()

print("redis-py RESP3 PASS")
