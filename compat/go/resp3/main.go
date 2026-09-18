package main

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	host := getenv("REDIS_HOST", "127.0.0.1")
	port := getenv("REDIS_PORT", "6380")
	target := getenv("TARGET_NAME", "snugkv")
	addr := host + ":" + port
	prefix := fmt.Sprintf("compat:resp3:go:%d", time.Now().UnixMilli())

	fmt.Printf("RESP3 go-redis smoke target=%s %s\n", target, addr)

	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Protocol: 3,
	})
	defer rdb.Close()

	pong, err := rdb.Ping(ctx).Result()
	must(err)
	assert(pong == "PONG", "PING failed")

	must(rdb.Set(ctx, prefix+":string", "hello", 0).Err())
	got, err := rdb.Get(ctx, prefix+":string").Result()
	must(err)
	assert(got == "hello", "SET/GET failed")

	_, err = rdb.Get(ctx, prefix+":missing").Result()
	assert(err == redis.Nil, fmt.Sprintf("missing GET error = %v", err))

	must(rdb.MSet(ctx, prefix+":a", "one", prefix+":b", "two").Err())
	mget, err := rdb.MGet(ctx, prefix+":a", prefix+":b").Result()
	must(err)
	assert(fmt.Sprint(mget) == "[one two]", fmt.Sprintf("MGET = %v", mget))

	must(rdb.HSet(ctx, prefix+":hash", "a", "1", "b", "2").Err())
	h, err := rdb.HGetAll(ctx, prefix+":hash").Result()
	must(err)
	assert(h["a"] == "1" && h["b"] == "2", fmt.Sprintf("HGETALL = %v", h))

	must(rdb.SAdd(ctx, prefix+":set", "a", "b").Err())
	members, err := rdb.SMembers(ctx, prefix+":set").Result()
	must(err)
	assert(len(members) == 2, fmt.Sprintf("SMEMBERS = %v", members))

	must(rdb.ZAdd(ctx, prefix+":z",
		redis.Z{Score: 1.5, Member: "a"},
		redis.Z{Score: 2.25, Member: "b"},
	).Err())
	score, err := rdb.ZScore(ctx, prefix+":z", "a").Result()
	must(err)
	assert(score == 1.5, fmt.Sprintf("ZSCORE = %v", score))

	stream := prefix + ":stream"
	id, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		ID:     "1-0",
		Values: map[string]interface{}{"f": "one"},
	}).Result()
	must(err)
	assert(id == "1-0", "XADD failed")

	entries, err := rdb.XRange(ctx, stream, "-", "+").Result()
	must(err)
	assert(len(entries) == 1 && entries[0].ID == "1-0", fmt.Sprintf("XRANGE = %v", entries))

	must(rdb.XGroupCreate(ctx, stream, "g", "0").Err())
	read, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    "g",
		Consumer: "c",
		Streams:  []string{stream, ">"},
		Count:    1,
	}).Result()
	must(err)
	assert(len(read) == 1, fmt.Sprintf("XREADGROUP = %v", read))

	pending, err := rdb.XPending(ctx, stream, "g").Result()
	must(err)
	assert(pending.Count == 1, fmt.Sprintf("XPENDING = %+v", pending))

	groups, err := rdb.XInfoGroups(ctx, stream).Result()
	must(err)
	assert(len(groups) == 1, fmt.Sprintf("XINFO GROUPS = %+v", groups))

	pipe := rdb.Pipeline()
	pipe.Set(ctx, prefix+":p1", "x", 0)
	pget := pipe.Get(ctx, prefix+":p1")
	_, err = pipe.Exec(ctx)
	must(err)
	assert(pget.Val() == "x", "pipeline failed")

	_, err = rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, prefix+":tx", "ok", 0)
		pipe.Get(ctx, prefix+":tx")
		return nil
	})
	must(err)

	must(rdb.Set(ctx, prefix+":wrong", "x", 0).Err())
	_, err = rdb.HGetAll(ctx, prefix+":wrong").Result()
	assert(err != nil && strings.Contains(err.Error(), "WRONGTYPE"), fmt.Sprintf("WRONGTYPE error = %v", err))

	must(rdb.Close())

	rdb2 := redis.NewClient(&redis.Options{Addr: addr, Protocol: 3})
	defer rdb2.Close()
	got, err = rdb2.Get(ctx, prefix+":string").Result()
	must(err)
	assert(got == "hello", "reconnect GET failed")
	pong, err = rdb2.Ping(ctx).Result()
	must(err)
	assert(pong == "PONG", "reconnect PING failed")

	fmt.Println("go-redis RESP3 PASS")
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

var _ = strconv.IntSize