package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"snugkv/internal/engine"
)

func main() {
	keys := flag.Int("keys", 1000, "number of keys to load")
	valueBytes := flag.Int("value-bytes", 16, "bytes per value")
	shards := flag.Int("shards", 256, "SnugKV shard count")
	flag.Parse()

	if *keys <= 0 || *valueBytes < 0 {
		fmt.Fprintln(os.Stderr, "sparsebench: keys must be positive and value-bytes non-negative")
		os.Exit(2)
	}

	store, err := engine.NewWithOptions(engine.Options{Shards: *shards})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sparsebench: create store: %v\n", err)
		os.Exit(1)
	}

	before := store.Memory()
	layoutBefore := store.Layout()
	value := make([]byte, *valueBytes)
	for i := range value {
		value[i] = 'x'
	}

	start := time.Now()
	for i := 0; i < *keys; i++ {
		key := fmt.Sprintf("sparse:%08d", i)
		if err := store.Set(key, value, 0); err != nil {
			fmt.Fprintf(os.Stderr, "sparsebench: SET %s: %v\n", key, err)
			os.Exit(1)
		}
	}
	elapsed := time.Since(start)

	after := store.Memory()
	layoutAfter := store.Layout()
	sparse := store.SparseLayout()
	delta := after.AccountedBytes - before.AccountedBytes

	fmt.Printf("SnugKV sparse benchmark\n")
	fmt.Printf("keys: %d\n", *keys)
	fmt.Printf("value_bytes: %d\n", *valueBytes)
	fmt.Printf("shards: %d\n", *shards)
	fmt.Printf("load_time: %s\n", elapsed)
	fmt.Printf("\nMemory\n")
	fmt.Printf("accounted_before: %d\n", before.AccountedBytes)
	fmt.Printf("accounted_after: %d\n", after.AccountedBytes)
	fmt.Printf("accounted_delta: %d\n", delta)
	fmt.Printf("bytes_per_key_delta: %.2f\n", float64(delta)/float64(*keys))
	fmt.Printf("index_reserved_bytes: %d\n", after.IndexReservedBytes)
	fmt.Printf("entry_bytes: %d\n", after.EntryBytes)
	fmt.Printf("arena_bytes: %d\n", after.ArenaBytes)
	fmt.Printf("arena_payload_bytes: %d\n", after.ArenaPayloadBytes)
	fmt.Printf("arena_live_block_bytes: %d\n", after.ArenaLiveBlockBytes)
	fmt.Printf("\nLayout\n")
	fmt.Printf("entry_struct_bytes: %d\n", layoutAfter.EntryStructBytes)
	fmt.Printf("index_slot_bytes: %d\n", layoutAfter.IndexSlotBytes)
	fmt.Printf("entry_capacity_before: %d\n", layoutBefore.EntryCapacity)
	fmt.Printf("entry_capacity_after: %d\n", layoutAfter.EntryCapacity)
	fmt.Printf("entry_storage_bytes_after: %d\n", layoutAfter.EntryStorageBytes)
	fmt.Printf("active_shards: %d\n", sparse.ActiveShards)
	fmt.Printf("entry_slots_used: %d\n", sparse.EntrySlotsUsed)
	fmt.Printf("entry_free_slots: %d\n", sparse.EntryFreeSlots)
	fmt.Printf("arena_active_shards: %d\n", sparse.ArenaActiveShards)
	fmt.Printf("arena_segments: %d\n", sparse.ArenaSegments)
}
