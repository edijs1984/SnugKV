package main

import (
	"flag"
	"fmt"
	"time"

	"snugkv/internal/engine"
	"snugkv/internal/optimizer"
)

func main() {
	keys := flag.Int("keys", 200000, "number of keys to insert")
	keep := flag.Int("keep", 50000, "number of keys to keep")
	flag.Parse()

	if *keys <= 0 || *keep < 0 || *keep > *keys {
		panic("invalid keys/keep")
	}

	store, err := engine.NewWithOptions(engine.Options{
		Shards:      256,
		Encoding:    true,
		Compression: true,
	})
	if err != nil {
		panic(err)
	}

	for i := 0; i < *keys; i++ {
		if err := store.Set(fmt.Sprintf("k:%09d", i), []byte("value"), 0); err != nil {
			panic(err)
		}
	}
	for i := *keep; i < *keys; i++ {
		store.Delete(fmt.Sprintf("k:%09d", i))
	}

	before := store.Layout()
	fmt.Printf("before entry_count=%d entry_capacity=%d entry_storage_bytes=%d\n",
		before.EntryCount, before.EntryCapacity, before.EntryStorageBytes)

	cfg := optimizer.Default()
	cfg.Workers = 1
	cfg.MinRewriteInterval = 0
	cfg.MinAttemptInterval = 0
	o, err := optimizer.New(store, cfg)
	if err != nil {
		panic(err)
	}
	defer o.Close()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		o.Sample(4096)
		time.Sleep(100 * time.Millisecond)
		layout := store.Layout()
		if layout.EntryCapacity == layout.EntryCount {
			break
		}
	}

	after := store.Layout()
	fmt.Printf("after entry_count=%d entry_capacity=%d entry_storage_bytes=%d\n",
		after.EntryCount, after.EntryCapacity, after.EntryStorageBytes)

	if after.EntryCount != uint64(*keep) {
		panic(fmt.Sprintf("entry count mismatch: got=%d want=%d", after.EntryCount, *keep))
	}
	if after.EntryCapacity > before.EntryCapacity {
		panic("entry capacity grew")
	}
	if after.EntryStorageBytes >= before.EntryStorageBytes {
		panic("entry storage did not shrink")
	}

	for _, i := range []int{0, *keep / 2, *keep - 1} {
		if i < 0 {
			continue
		}
		key := fmt.Sprintf("k:%09d", i)
		value, ok := store.Get(key)
		if !ok || string(value) != "value" {
			panic(fmt.Sprintf("key corrupted: %s ok=%v value=%q", key, ok, value))
		}
	}

	fmt.Println("PASS")
}
