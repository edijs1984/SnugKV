import redis
import time

r = redis.Redis(
    host="127.0.0.1",
    port=6380,
    decode_responses=False,
    protocol=2,
    socket_connect_timeout=3,
)

prefix = f"compat:python:{int(time.time() * 1000)}"

assert r.ping() is True

r.set(prefix + ":string", b"hello")
assert r.get(prefix + ":string") == b"hello"

r.mset({
    prefix + ":a": b"one",
    prefix + ":b": b"two",
})

assert r.mget(
    prefix + ":a",
    prefix + ":b",
) == [b"one", b"two"]

r.set(prefix + ":counter", b"10")
assert r.incr(prefix + ":counter") == 11

r.pexpire(prefix + ":string", 60000)

ttl = r.pttl(prefix + ":string")
assert 0 < ttl <= 60000

binary = bytes([0, 1, 2, 13, 10, 255, 128])

r.set(prefix + ":binary", binary)
assert r.get(prefix + ":binary") == binary

pipe = r.pipeline(transaction=False)

pipe.set(prefix + ":pipe1", b"x")
pipe.set(prefix + ":pipe2", b"y")
pipe.get(prefix + ":pipe1")
pipe.get(prefix + ":pipe2")

result = pipe.execute()

assert result == [True, True, b"x", b"y"], result

r.connection_pool.disconnect()

r2 = redis.Redis(
    host="127.0.0.1",
    port=6380,
    decode_responses=False,
    protocol=2,
)

r2.set(prefix + ":reconnect", b"survives")
r2.connection_pool.disconnect()

r3 = redis.Redis(
    host="127.0.0.1",
    port=6380,
    decode_responses=False,
    protocol=2,
)

assert r3.get(prefix + ":reconnect") == b"survives"
assert r3.ping() is True

print("redis-py PASS")
