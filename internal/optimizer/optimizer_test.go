package optimizer

import (
	"context"
	"sync/atomic"
	"bytes"
	"snugkv/internal/engine"
	"strconv"
	"testing"
	"time"
)

func TestOptimizerProfiles(t *testing.T) {
	dedicated := ForMode("dedicated")
	sidecar := ForMode("sidecar")

	if dedicated.Workers < sidecar.Workers {
		t.Fatalf("dedicated workers=%d sidecar=%d", dedicated.Workers, sidecar.Workers)
	}
	if dedicated.CPUPercent <= sidecar.CPUPercent {
		t.Fatalf("dedicated cpu=%d sidecar=%d", dedicated.CPUPercent, sidecar.CPUPercent)
	}
	if dedicated.MaxBytesPerSecond <= sidecar.MaxBytesPerSecond {
		t.Fatalf("dedicated bandwidth=%d sidecar=%d", dedicated.MaxBytesPerSecond, sidecar.MaxBytesPerSecond)
	}
	if Default() != dedicated {
		t.Fatal("default optimizer profile is not dedicated")
	}
}

func TestBudgetsAndCancellation(t *testing.T) {
	s := engine.New()
	c := Default()
	c.MaxBytesPerSecond = 100
	c.MaxScratchBytes = (16 << 20) + 6400
	o, err := New(s, c)
	if err != nil {
		t.Fatal(err)
	}
	if !o.reserve(100) || o.reserve(1) {
		t.Fatal("byte/scratch budget")
	}
	o.release(100)
	if o.reserve(1) {
		t.Fatal("byte rate reset too early")
	}
	start := time.Now()
	o.Close()
	o.Close()
	if time.Since(start) > time.Second {
		t.Fatal("slow cancellation")
	}
	if o.Queue("k") {
		t.Fatal("accepted work after shutdown")
	}
}
func TestInvalidConfiguration(t *testing.T) {
	c := Default()
	c.CPUPercent = 0
	if _, err := New(engine.New(), c); err == nil {
		t.Fatal("invalid CPU percent")
	}
}

func TestBackgroundCompression(t *testing.T) {
	store, err := engine.NewWithOptions(engine.Options{Shards: 1, Encoding: true, Compression: true})
	if err != nil {
		t.Fatal(err)
	}
	value := bytes.Repeat([]byte("compressible-value"), 1024)
	if err = store.Set("k", value, 0); err != nil {
		t.Fatal(err)
	}
	config := Default()
	config.MinRewriteInterval = 0
	optimizer, err := New(store, config)
	if err != nil {
		t.Fatal(err)
	}
	defer optimizer.Close()
	if !optimizer.Queue("k") {
		t.Fatal("queue rejected")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		name, _, _, _ := store.Encoding("k")
		switch name {
		case "repeat-byte", "periodic", "lz4", "zstd":
			got, ok := store.Get("k")
			if !ok || !bytes.Equal(got, value) {
				t.Fatal("background rewrite changed bytes")
			}
			if meta := store.Memory().MetaBytes; meta != 0 {
				t.Fatalf("compressed scalar retained %d metadata bytes, want 0", meta)
			}
			if _, eligible := store.OptimizationEligible("k", 0, 0); eligible {
				t.Fatal("compressed scalar remained optimizer-eligible")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("rewrite did not complete: %+v", optimizer.Stats())
}


func TestSampleRespectsQueueCapacity(t *testing.T) {
	store := engine.New()
	for i := 0; i < 64; i++ {
		if err := store.Set(strconv.Itoa(i), []byte("value"), 0); err != nil {
			t.Fatal(err)
		}
	}

	config := Default()
	config.Workers = 1
	config.QueueDepth = 4
	optimizer, err := New(store, config)
	if err != nil {
		t.Fatal(err)
	}
	defer optimizer.Close()

	optimizer.Sample(64)
	stats := optimizer.Stats()
	if stats.QueueCapacity != 4 {
		t.Fatalf("queue capacity=%d want=4", stats.QueueCapacity)
	}
	if stats.QueueDepth > stats.QueueCapacity {
		t.Fatalf("queue depth=%d capacity=%d", stats.QueueDepth, stats.QueueCapacity)
	}
}


func TestCPUPercentForBacklog(t *testing.T) {
	store := engine.New()
	config := Default()
	config.Workers = 1
	config.QueueDepth = 160
	config.CPUPercent = 50
	o, err := New(store, config)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	for _, depth := range []int{0, 9, 10, 39, 40, 160} {
		if got := o.cpuPercentForBacklog(depth); got != 50 {
			t.Fatalf("depth=%d cpu=%d want=50", depth, got)
		}
	}
}

func TestCPUPercentForBacklogNeverRaisesConfiguredBudget(t *testing.T) {
	store := engine.New()
	config := Default()
	config.Workers = 1
	config.QueueDepth = 160
	config.CPUPercent = 10
	o, err := New(store, config)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	if got := o.cpuPercentForBacklog(160); got != 10 {
		t.Fatalf("cpu=%d want=10", got)
	}
}


func TestShouldCompactArenaPolicy(t *testing.T) {
	const arena = uint64(100 << 20)

	tests := []struct {
		name       string
		live       uint64
		queueDepth int
		want       bool
	}{
		{"empty arena", 0, 0, false},
		{"idle below 25 percent dead", 80 << 20, 0, false},
		{"idle at 25 percent dead", 75 << 20, 0, true},
		{"backlog below 40 percent dead", 70 << 20, 1, false},
		{"backlog at 40 percent dead", 60 << 20, 1, true},
		{"backlog tiny dead bytes", 93 << 20, 1, false},
	}

	if shouldCompactArena(0, 0, 0) {
		t.Fatal("zero arena should never compact")
	}

	for _, tc := range tests[1:] {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldCompactArena(arena, tc.live, tc.queueDepth); got != tc.want {
				t.Fatalf("compact=%v want=%v arena=%d live=%d queue=%d", got, tc.want, arena, tc.live, tc.queueDepth)
			}
		})
	}
}


func TestShouldCompactEntriesPolicy(t *testing.T) {
	tests := []struct {
		name       string
		capacity   uint64
		live       uint64
		queueDepth int
		want       bool
	}{
		{"no slack", 4096, 4096, 0, false},
		{"idle below minimum slack", 4096, 3200, 0, false},
		{"idle 25 percent slack", 4096, 3072, 0, true},
		{"backlog requires larger slack", 8192, 5120, 1, false},
		{"backlog 50 percent slack", 8192, 4096, 1, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldCompactEntries(tc.capacity, tc.live, tc.queueDepth); got != tc.want {
				t.Fatalf("compact=%v want=%v capacity=%d live=%d queue=%d",
					got, tc.want, tc.capacity, tc.live, tc.queueDepth)
			}
		})
	}
}

func TestMaintenanceSamplingConvergesDroppedCandidate(t *testing.T) {
	store, err := engine.NewWithOptions(engine.Options{
		Shards:      1,
		Encoding:    true,
		Compression: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	value := bytes.Repeat([]byte("converge-me"), 2048)
	if err := store.Set("k", value, 0); err != nil {
		t.Fatal(err)
	}

	config := Default()
	config.Workers = 1
	config.MinRewriteInterval = 0
	config.MinAttemptInterval = 0

	o, err := New(store, config)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	// Deliberately do not Queue("k"). Periodic maintenance sampling must recover
	// the missed write-time enqueue and converge the representation.
	o.maintenanceStep()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		name, _, _, _ := store.Encoding("k")
		switch name {
		case "repeat-byte", "periodic", "lz4", "zstd":
			got, ok := store.Get("k")
			if !ok || !bytes.Equal(got, value) {
				t.Fatal("maintenance rewrite changed bytes")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("maintenance sampling did not converge candidate: %+v", o.Stats())
}


func TestForegroundWriteDefersOptimizerDuringBurstThenResumes(t *testing.T) {
	store, err := engine.NewWithOptions(engine.Options{
		Shards:      1,
		Encoding:    true,
		Compression: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	value := bytes.Repeat([]byte("foreground-priority"), 1024)
	if err := store.Set("k", value, 0); err != nil {
		t.Fatal(err)
	}

	config := Default()
	config.Workers = 1
	config.MinRewriteInterval = 0
	config.MinAttemptInterval = 0

	o, err := New(store, config)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	// Pin the foreground-write timestamp slightly into the future so the worker
	// cannot observe an accidental quiet gap due to scheduler latency. This
	// exercises the deferral gate deterministically instead of relying on a
	// sub-millisecond ticker or goroutine scheduling.
	atomic.StoreInt64(&o.lastForegroundWrite, time.Now().Add(time.Second).UnixNano())
	if !o.Queue("k") {
		t.Fatal("queue rejected")
	}

	time.Sleep(20 * time.Millisecond)
	if got := o.Stats().Rewritten; got != 0 {
		t.Fatalf("optimizer rewrote before foreground deferral elapsed: %d", got)
	}

	// Clearing the timestamp represents the foreground becoming quiet and must
	// let the queued optimization resume immediately.
	atomic.StoreInt64(&o.lastForegroundWrite, 0)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if o.Stats().Rewritten != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("optimizer did not resume after foreground quiet: %+v", o.Stats())
}


func TestMaintenanceCompactsIndexedZSetWithoutLongTail(t *testing.T) {
	store := engine.New()

	// Grow a ZSET past the indexed threshold one member at a time so its
	// physical representation owns append headroom that maintenance can trim.
	for i := 0; i < 128; i++ {
		if _, _, _, err := store.ZSetAdd(
			"z",
			[]engine.ZSetItem{{
				Member: []byte("m:" + strconv.Itoa(i)),
				Score:  float64(i),
			}},
			engine.ZSetAddOptions{},
		); err != nil {
			t.Fatal(err)
		}
	}

	// The first explicit compaction proves there is indexed growth reserve.
	if !store.CompactIndexedZSet("z") {
		t.Fatal("expected indexed ZSET to have compactable growth reserve")
	}

	// Grow it again so the optimizer maintenance path has fresh reserve to trim.
	for i := 128; i < 160; i++ {
		if _, _, _, err := store.ZSetAdd(
			"z",
			[]engine.ZSetItem{{
				Member: []byte("m:" + strconv.Itoa(i)),
				Score:  float64(i),
			}},
			engine.ZSetAddOptions{},
		); err != nil {
			t.Fatal(err)
		}
	}

	config := Default()
	config.Workers = 1
	o, err := New(store, config)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	// Run the maintenance body directly rather than waiting for the 10s ticker.
	o.maintenanceStep()

	if store.CompactIndexedZSet("z") {
		t.Fatal("maintenance left indexed ZSET growth reserve behind")
	}
}


func TestMaintenanceCompactsIndexedZSetWithOptimizerBacklog(t *testing.T) {
	store := engine.New()

	for i := 0; i < 128; i++ {
		if _, _, _, err := store.ZSetAdd(
			"z",
			[]engine.ZSetItem{{
				Member: []byte("m:" + strconv.Itoa(i)),
				Score:  float64(i),
			}},
			engine.ZSetAddOptions{},
		); err != nil {
			t.Fatal(err)
		}
	}

	if !store.CompactIndexedZSet("z") {
		t.Fatal("expected indexed ZSET to have compactable growth reserve")
	}

	for i := 128; i < 160; i++ {
		if _, _, _, err := store.ZSetAdd(
			"z",
			[]engine.ZSetItem{{
				Member: []byte("m:" + strconv.Itoa(i)),
				Score:  float64(i),
			}},
			engine.ZSetAddOptions{},
		); err != nil {
			t.Fatal(err)
		}
	}

	config := Default()
	o := &Optimizer{
		store:  store,
		config: config,
		queue:  make(chan string, 4),
	}
	o.queue <- "pending:1"
	o.queue <- "pending:2"

	o.maintenanceStep()

	if len(o.queue) == 0 {
		t.Fatal("test did not preserve optimizer backlog")
	}
	if store.CompactIndexedZSet("z") {
		t.Fatal("optimizer backlog blocked indexed ZSET maintenance compaction")
	}
}


func TestSampleSkipsNativeZSetKeys(t *testing.T) {
	store := engine.New()
	for i := 0; i < 32; i++ {
		if _, _, _, err := store.ZSetAdd(
			"z:"+strconv.Itoa(i),
			[]engine.ZSetItem{{Member: []byte("m"), Score: 1}},
			engine.ZSetAddOptions{},
		); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := &Optimizer{
		store:  store,
		config: Default(),
		ctx:    ctx,
		queue:  make(chan string, 64),
	}

	o.Sample(32)

	if got := o.Stats().Queued; got != 0 {
		t.Fatalf("native ZSET sampling queued %d ineligible keys, want 0", got)
	}
	if got := len(o.queue); got != 0 {
		t.Fatalf("native ZSET sampling queue depth=%d want=0", got)
	}
}

func TestShouldCompactIndex(t *testing.T) {
	const slot = 16
	if shouldCompactIndex(0, 0, slot, 0) {
		t.Fatal("empty store must not compact")
	}
	// 1M keys at power-of-two sizing: about 2.1 slots per key.
	if !shouldCompactIndex(33_605_632, 1_000_000, slot, 0) {
		t.Fatal("loose index should compact when idle")
	}
	// Already tight: about 1.25 slots per key.
	if shouldCompactIndex(20_000_000, 1_000_000, slot, 0) {
		t.Fatal("tight index must not compact")
	}
	// Small slack stays below the absolute floor.
	if shouldCompactIndex(300_000, 10_000, slot, 0) {
		t.Fatal("tiny absolute slack must not compact")
	}
	// A busy queue demands more slack before maintenance competes with it.
	if shouldCompactIndex(24_000_000, 1_000_000, slot, 10) {
		t.Fatal("moderate slack must wait while the queue is backed up")
	}
	if !shouldCompactIndex(33_605_632, 1_000_000, slot, 10) {
		t.Fatal("large slack should still compact under a backlog")
	}
}

func TestShouldCompactHotHashes(t *testing.T) {
	if shouldCompactHotHashes(0) || shouldCompactHotHashes(1<<20-1) {
		t.Fatal("small hot-hash footprint must not trigger compaction")
	}
	if !shouldCompactHotHashes(1<<20) || !shouldCompactHotHashes(100<<20) {
		t.Fatal("large hot-hash footprint must trigger compaction")
	}
}
