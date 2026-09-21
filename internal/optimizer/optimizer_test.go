package optimizer

import (
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
