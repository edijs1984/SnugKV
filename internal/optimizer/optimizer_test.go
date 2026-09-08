package optimizer

import (
	"bytes"
	"morphcache/internal/engine"
	"testing"
	"time"
)

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
		if name == "lz4" || name == "zstd" {
			got, ok := store.Get("k")
			if !ok || !bytes.Equal(got, value) {
				t.Fatal("background rewrite changed bytes")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("rewrite did not complete: %+v", optimizer.Stats())
}
