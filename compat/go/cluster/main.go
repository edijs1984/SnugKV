package main

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func assert(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(format, args...))
	}
}

func main() {
	ctx := context.Background()

	rdb := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{"127.0.0.1:7000"},
	})
	defer rdb.Close()

	pong, err := rdb.Ping(ctx).Result()
	must(err)
	assert(pong == "PONG", "PING=%q", pong)

	// Distinct hash tags force routing across the three SnugKV shards.
	for _, tc := range []struct {
		key, value string
	}{
		{"gocluster:{1}:a", "one"},
		{"gocluster:{2}:b", "two"},
		{"gocluster:{3}:c", "three"},
	} {
		must(rdb.Set(ctx, tc.key, tc.value, 0).Err())
		got, err := rdb.Get(ctx, tc.key).Result()
		must(err)
		assert(got == tc.value, "GET %s=%q want %q", tc.key, got, tc.value)
	}

	must(rdb.MSet(
		ctx,
		"gocluster:{42}:a", "A",
		"gocluster:{42}:b", "B",
	).Err())
	values, err := rdb.MGet(
		ctx,
		"gocluster:{42}:a",
		"gocluster:{42}:b",
	).Result()
	must(err)
	assert(
		len(values) == 2 && values[0] == "A" && values[1] == "B",
		"MGET=%v",
		values,
	)

	pipe := rdb.Pipeline()
	s1 := pipe.Set(ctx, "gocluster:{11}:p", "P1", 0)
	s2 := pipe.Set(ctx, "gocluster:{12}:p", "P2", 0)
	s3 := pipe.Set(ctx, "gocluster:{13}:p", "P3", 0)
	_, err = pipe.Exec(ctx)
	must(err)
	must(s1.Err())
	must(s2.Err())
	must(s3.Err())

	pipe = rdb.Pipeline()
	g1 := pipe.Get(ctx, "gocluster:{11}:p")
	g2 := pipe.Get(ctx, "gocluster:{12}:p")
	g3 := pipe.Get(ctx, "gocluster:{13}:p")
	_, err = pipe.Exec(ctx)
	must(err)
	assert(g1.Val() == "P1", "pipeline GET1=%q", g1.Val())
	assert(g2.Val() == "P2", "pipeline GET2=%q", g2.Val())
	assert(g3.Val() == "P3", "pipeline GET3=%q", g3.Val())

	fmt.Println("go-redis cluster: PASS")
}
