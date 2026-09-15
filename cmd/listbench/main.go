package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"snugkv/internal/engine"
)

func main() {
	keys := flag.Int("keys", 100000, "number of list keys")
	elements := flag.Int("elements", 8, "elements per list")
	elementBytes := flag.Int("element-bytes", 16, "bytes per list element")
	shards := flag.Int("shards", 256, "SnugKV shard count")
	redisAddr := flag.String("redis-addr", "127.0.0.1:6379", "Redis address")
	redisDB := flag.Int("redis-db", 15, "Redis DB used for the benchmark")
	redisReset := flag.Bool("redis-reset", false, "flush selected Redis DB if non-empty")
	cleanup := flag.Bool("cleanup", true, "flush benchmark data after measurement")
	flag.Parse()

	if *keys <= 0 || *elements <= 0 || *elementBytes < 0 {
		fatalf("keys and elements must be positive; element-bytes must be non-negative")
	}

	values := dataset(*elements, *elementBytes)
	store, err := engine.NewWithOptions(engine.Options{Shards: *shards})
	if err != nil {
		fatalf("create SnugKV store: %v", err)
	}

	snugBefore := store.Memory()
	layoutBefore := store.Layout()
	loadStart := time.Now()
	for i := 0; i < *keys; i++ {
		if _, err := store.ListPushRight(listKey(i), values); err != nil {
			fatalf("SnugKV RPUSH %q: %v", listKey(i), err)
		}
	}
	snugLoad := time.Since(loadStart)
	snugAfter := store.Memory()
	layoutAfter := store.Layout()

	sample, ok, err := store.ListStorageStats(listKey(0))
	if err != nil || !ok {
		fatalf("read SnugKV sample list stats: found=%t err=%v", ok, err)
	}

	redis, err := newRedisClient(*redisAddr, *redisDB)
	if err != nil {
		fatalf("connect Redis at %s: %v", *redisAddr, err)
	}
	defer redis.Close()

	initialKeys, err := redis.DBSize()
	if err != nil {
		fatalf("Redis DBSIZE: %v", err)
	}
	if initialKeys != 0 {
		if !*redisReset {
			fatalf("Redis DB %d is not empty (%d keys); use empty DB or -redis-reset", *redisDB, initialKeys)
		}
		if err := redis.FlushDB(); err != nil {
			fatalf("Redis FLUSHDB: %v", err)
		}
	}

	redisBefore, err := redis.UsedMemory()
	if err != nil {
		fatalf("Redis INFO memory before: %v", err)
	}
	redisStart := time.Now()
	if err := redis.LoadLists(*keys, values); err != nil {
		fatalf("Redis RPUSH load: %v", err)
	}
	redisLoad := time.Since(redisStart)
	redisKeys, err := redis.DBSize()
	if err != nil {
		fatalf("Redis DBSIZE after load: %v", err)
	}
	redisAfter, err := redis.UsedMemory()
	if err != nil {
		fatalf("Redis INFO memory after: %v", err)
	}
	if redisAfter < redisBefore {
		fatalf("Redis used_memory decreased: before=%d after=%d", redisBefore, redisAfter)
	}

	snugDelta := snugAfter.AccountedBytes - snugBefore.AccountedBytes
	indexDelta := snugAfter.IndexReservedBytes - snugBefore.IndexReservedBytes
	entryDelta := snugAfter.EntryBytes - snugBefore.EntryBytes
	arenaDelta := snugAfter.ArenaBytes - snugBefore.ArenaBytes
	payloadDelta := snugAfter.ArenaPayloadBytes - snugBefore.ArenaPayloadBytes
	liveBlockDelta := snugAfter.ArenaLiveBlockBytes - snugBefore.ArenaLiveBlockBytes
	metaDelta := snugAfter.MetaBytes - snugBefore.MetaBytes
	redisDelta := redisAfter - redisBefore

	fmt.Printf("LIST benchmark\n")
	fmt.Printf("keys: %d\n", *keys)
	fmt.Printf("elements_per_list: %d\n", *elements)
	fmt.Printf("element_bytes: %d\n", *elementBytes)
	fmt.Printf("element_bytes_total_per_list: %d\n", sample.ElementBytes)
	fmt.Printf("snug_packed_bytes_per_list: %d\n", sample.PackedBytes)
	fmt.Printf("snug_stored_bytes_per_list: %d\n", sample.StoredBytes)
	fmt.Printf("snug_storage_encoding: %s\n", sample.Encoding)

	fmt.Printf("\nSnugKV\n")
	fmt.Printf("accounted_before: %d\n", snugBefore.AccountedBytes)
	fmt.Printf("accounted_after: %d\n", snugAfter.AccountedBytes)
	fmt.Printf("accounted_delta: %d\n", snugDelta)
	fmt.Printf("bytes_per_list_delta: %.2f\n", perList(snugDelta, *keys))
	fmt.Printf("index_delta: %d\n", indexDelta)
	fmt.Printf("index_bytes_per_list: %.2f\n", perList(indexDelta, *keys))
	fmt.Printf("entry_delta: %d\n", entryDelta)
	fmt.Printf("entry_bytes_per_list: %.2f\n", perList(entryDelta, *keys))
	fmt.Printf("arena_delta: %d\n", arenaDelta)
	fmt.Printf("arena_bytes_per_list: %.2f\n", perList(arenaDelta, *keys))
	fmt.Printf("arena_payload_delta: %d\n", payloadDelta)
	fmt.Printf("arena_payload_bytes_per_list: %.2f\n", perList(payloadDelta, *keys))
	fmt.Printf("arena_live_block_delta: %d\n", liveBlockDelta)
	fmt.Printf("arena_live_block_bytes_per_list: %.2f\n", perList(liveBlockDelta, *keys))
	fmt.Printf("meta_delta: %d\n", metaDelta)
	fmt.Printf("meta_bytes_per_list: %.2f\n", perList(metaDelta, *keys))
	fmt.Printf("entry_struct_bytes: %d\n", layoutAfter.EntryStructBytes)
	fmt.Printf("index_slot_bytes: %d\n", layoutAfter.IndexSlotBytes)
	fmt.Printf("entry_capacity_before: %d\n", layoutBefore.EntryCapacity)
	fmt.Printf("entry_capacity_after: %d\n", layoutAfter.EntryCapacity)
	fmt.Printf("entry_storage_delta: %d\n", layoutAfter.EntryStorageBytes-layoutBefore.EntryStorageBytes)
	fmt.Printf("entry_storage_bytes_per_list: %.2f\n", perList(layoutAfter.EntryStorageBytes-layoutBefore.EntryStorageBytes, *keys))
	fmt.Printf("load_time: %s\n", snugLoad)

	fmt.Printf("\nRedis\n")
	fmt.Printf("db: %d\n", *redisDB)
	fmt.Printf("keys_after_load: %d\n", redisKeys)
	fmt.Printf("used_memory_before: %d\n", redisBefore)
	fmt.Printf("used_memory_after: %d\n", redisAfter)
	fmt.Printf("used_memory_delta: %d\n", redisDelta)
	fmt.Printf("bytes_per_list_delta: %.2f\n", perList(redisDelta, *keys))
	fmt.Printf("load_time: %s\n", redisLoad)

	fmt.Printf("\nResult\n")
	if redisDelta == 0 {
		fmt.Printf("snug_vs_redis_memory_saving_pct: n/a\n")
	} else {
		saving := (1 - float64(snugDelta)/float64(redisDelta)) * 100
		fmt.Printf("snug_vs_redis_memory_saving_pct: %.2f%%\n", saving)
	}

	if *cleanup {
		if err := redis.FlushDB(); err != nil {
			fatalf("Redis cleanup FLUSHDB: %v", err)
		}
	}
}

func dataset(count, elementBytes int) [][]byte {
	out := make([][]byte, count)
	for i := 0; i < count; i++ {
		value := make([]byte, elementBytes)
		for j := range value {
			value[j] = 'x'
		}
		prefix := []byte(fmt.Sprintf("e:%08d", i))
		copy(value, prefix)
		out[i] = value
	}
	return out
}

func listKey(i int) string { return fmt.Sprintf("list:%08d", i) }
func perList(n uint64, keys int) float64 { return float64(n) / float64(keys) }
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "listbench: "+format+"\n", args...)
	os.Exit(1)
}

type redisClient struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

func newRedisClient(addr string, db int) (*redisClient, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	c := &redisClient{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), w: bufio.NewWriterSize(conn, 256<<10)}
	if err := c.commandOK([]byte("SELECT"), []byte(strconv.Itoa(db))); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *redisClient) Close() error { return c.conn.Close() }

func (c *redisClient) DBSize() (int64, error) {
	if err := c.writeCommand([]byte("DBSIZE")); err != nil {
		return 0, err
	}
	if err := c.w.Flush(); err != nil {
		return 0, err
	}
	return c.readInteger()
}

func (c *redisClient) FlushDB() error { return c.commandOK([]byte("FLUSHDB")) }

func (c *redisClient) UsedMemory() (uint64, error) {
	if err := c.writeCommand([]byte("INFO"), []byte("memory")); err != nil {
		return 0, err
	}
	if err := c.w.Flush(); err != nil {
		return 0, err
	}
	payload, err := c.readBulk()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(payload), "\r\n") {
		if strings.HasPrefix(line, "used_memory:") {
			return strconv.ParseUint(strings.TrimPrefix(line, "used_memory:"), 10, 64)
		}
	}
	return 0, errors.New("used_memory missing from INFO memory")
}

func (c *redisClient) LoadLists(keys int, elements [][]byte) error {
	const batchSize = 512
	for start := 0; start < keys; start += batchSize {
		end := start + batchSize
		if end > keys {
			end = keys
		}
		for i := start; i < end; i++ {
			args := make([][]byte, 0, 2+len(elements))
			args = append(args, []byte("RPUSH"), []byte(listKey(i)))
			args = append(args, elements...)
			if err := c.writeCommand(args...); err != nil {
				return err
			}
		}
		if err := c.w.Flush(); err != nil {
			return err
		}
		for i := start; i < end; i++ {
			if _, err := c.readInteger(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *redisClient) commandOK(args ...[]byte) error {
	if err := c.writeCommand(args...); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if len(line) == 0 {
		return io.ErrUnexpectedEOF
	}
	if line[0] == '-' {
		return errors.New(string(line[1:]))
	}
	if line[0] != '+' {
		return fmt.Errorf("unexpected Redis reply %q", line)
	}
	return nil
}

func (c *redisClient) writeCommand(args ...[]byte) error {
	if _, err := fmt.Fprintf(c.w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(c.w, "$%d\r\n", len(arg)); err != nil {
			return err
		}
		if _, err := c.w.Write(arg); err != nil {
			return err
		}
		if _, err := c.w.WriteString("\r\n"); err != nil {
			return err
		}
	}
	return nil
}

func (c *redisClient) readLine() ([]byte, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, errors.New("invalid Redis response")
	}
	return line[:len(line)-2], nil
}

func (c *redisClient) readInteger() (int64, error) {
	line, err := c.readLine()
	if err != nil {
		return 0, err
	}
	if len(line) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	if line[0] == '-' {
		return 0, errors.New(string(line[1:]))
	}
	if line[0] != ':' {
		return 0, fmt.Errorf("unexpected integer reply %q", line)
	}
	return strconv.ParseInt(string(line[1:]), 10, 64)
}

func (c *redisClient) readBulk() ([]byte, error) {
	line, err := c.readLine()
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	if line[0] == '-' {
		return nil, errors.New(string(line[1:]))
	}
	if line[0] != '$' {
		return nil, fmt.Errorf("unexpected bulk reply %q", line)
	}
	n, err := strconv.Atoi(string(line[1:]))
	if err != nil || n < 0 {
		return nil, errors.New("invalid bulk length")
	}
	payload := make([]byte, n+2)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return nil, err
	}
	if payload[n] != '\r' || payload[n+1] != '\n' {
		return nil, errors.New("invalid bulk terminator")
	}
	return payload[:n], nil
}
