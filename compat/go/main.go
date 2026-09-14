package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func assert(ok bool, msg string) {
	if !ok {
		panic(msg)
	}
}

func main() {
	ctx := context.Background()

	rdb := redis.NewClient(&redis.Options{
		Addr:     "127.0.0.1:6380",
		Protocol: 2,
	})

	defer rdb.Close()

	prefix := fmt.Sprintf("compat:go:%d", time.Now().UnixMilli())

	pong, err := rdb.Ping(ctx).Result()
	must(err)
	assert(pong == "PONG", "PING failed")

	must(rdb.Set(ctx, prefix+":string", "hello", 0).Err())

	got, err := rdb.Get(ctx, prefix+":string").Result()
	must(err)
	assert(got == "hello", "SET/GET failed")

	must(rdb.MSet(
		ctx,
		prefix+":a", "one",
		prefix+":b", "two",
	).Err())

	values, err := rdb.MGet(
		ctx,
		prefix+":a",
		prefix+":b",
	).Result()
	must(err)

	assert(
		fmt.Sprint(values) == "[one two]",
		fmt.Sprintf("MGET mismatch: %v", values),
	)

	must(rdb.Set(ctx, prefix+":counter", "10", 0).Err())

	counter, err := rdb.Incr(ctx, prefix+":counter").Result()
	must(err)
	assert(counter == 11, "INCR failed")

	ok, err := rdb.PExpire(ctx, prefix+":string", 60*time.Second).Result()
	must(err)
	assert(ok, "PEXPIRE failed")

	ttl, err := rdb.PTTL(ctx, prefix+":string").Result()
	must(err)

	assert(
		ttl > 0 && ttl <= 60*time.Second,
		fmt.Sprintf("invalid TTL: %v", ttl),
	)

	binary := []byte{0, 1, 2, 13, 10, 255, 128}

	must(rdb.Set(ctx, prefix+":binary", binary, 0).Err())

	binaryBack, err := rdb.Get(ctx, prefix+":binary").Bytes()
	must(err)

	assert(
		bytes.Equal(binary, binaryBack),
		fmt.Sprintf("binary mismatch: %x", binaryBack),
	)

	pipe := rdb.Pipeline()

	pipe.Set(ctx, prefix+":pipe1", "x", 0)
	pipe.Set(ctx, prefix+":pipe2", "y", 0)
	get1 := pipe.Get(ctx, prefix+":pipe1")
	get2 := pipe.Get(ctx, prefix+":pipe2")

	_, err = pipe.Exec(ctx)
	must(err)

	assert(get1.Val() == "x", "pipeline GET 1 failed")
	assert(get2.Val() == "y", "pipeline GET 2 failed")

	must(rdb.Set(ctx, prefix+":reconnect", "survives", 0).Err())
	must(rdb.Close())

	rdb2 := redis.NewClient(&redis.Options{
		Addr:     "127.0.0.1:6380",
		Protocol: 2,
	})

	defer rdb2.Close()

	value, err := rdb2.Get(ctx, prefix+":reconnect").Result()
	must(err)
	assert(value == "survives", "reconnect GET failed")

	pong, err = rdb2.Ping(ctx).Result()
	must(err)
	assert(pong == "PONG", "PING after reconnect failed")

	fmt.Println("go-redis PASS")
	os.Exit(0)
}
