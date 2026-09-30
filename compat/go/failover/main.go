package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

func envInt(name string, def int) int {
	if value := os.Getenv(name); value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return def
}

func retry(label string, timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var last error
	attempts := 0
	for time.Now().Before(deadline) {
		attempts++
		if err := fn(); err == nil {
			fmt.Printf("[%s] success after %d attempt(s)\n", label, attempts)
			return nil
		} else {
			last = err
			fmt.Printf("[%s] attempt %d: %v\n", label, attempts, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s: %v", label, last)
}

func writeState(dir, name, value string) {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
		panic(err)
	}
}

func main() {
	ctx := context.Background()
	p0 := envInt("P0", 7120)
	p1 := envInt("P1", 7121)
	p2 := envInt("P2", 7122)
	password := os.Getenv("PASSWORD")
	if password == "" {
		password = "cluster-failover-secret"
	}
	stateDir := os.Getenv("CLIENT_STATE_DIR")
	if stateDir == "" {
		stateDir = "/tmp/snugkv-d-failover-go"
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		panic(err)
	}

	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{
			fmt.Sprintf("127.0.0.1:%d", p0),
			fmt.Sprintf("127.0.0.1:%d", p1),
			fmt.Sprintf("127.0.0.1:%d", p2),
		},
		Password: password,
		DialTimeout: 2 * time.Second,
		DialerRetries: 1,
		DialerRetryTimeout: 100 * time.Millisecond,
		ReadTimeout: 2 * time.Second,
		WriteTimeout: 2 * time.Second,
		MaxRedirects: 8,
		// go-redis v9.22.0 does not reactively reload cluster state on a
		// plain TCP dial failure. The library's default periodic reload is 60s.
		// Use the documented reload interval knob for prompt HA recovery.
		ClusterStateReloadInterval: time.Second,
	})
	defer client.Close()

	fail := func(err error) {
		writeState(stateDir, "failed", err.Error())
		panic(err)
	}

	if err := retry("go-redis preflight", 15*time.Second, func() error {
		if err := client.Set(ctx, "d:failover:{4}:go", "before", 0).Err(); err != nil {
			return err
		}
		value, err := client.Get(ctx, "d:failover:{4}:go").Result()
		if err != nil {
			return err
		}
		if value != "before" {
			return fmt.Errorf("bad value %q", value)
		}
		return nil
	}); err != nil {
		fail(err)
	}
	writeState(stateDir, "ready", "ready\n")

	proceed := filepath.Join(stateDir, "post-failover")
	for {
		if _, err := os.Stat(proceed); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := retry("go-redis recovery", 15*time.Second, func() error {
		if err := client.Set(ctx, "d:failover:{4}:go", "after", 0).Err(); err != nil {
			return err
		}
		value, err := client.Get(ctx, "d:failover:{4}:go").Result()
		if err != nil {
			return err
		}
		if value != "after" {
			return fmt.Errorf("bad value %q", value)
		}
		return nil
	}); err != nil {
		fail(err)
	}
	writeState(stateDir, "passed", "PASS\n")
	fmt.Println("persistent go-redis failover/reconnect: PASS")
}
