package optimizer

import (
	"sync/atomic"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func newGateOptimizer(t *testing.T) *Optimizer {
	t.Helper()
	o, err := New(engine.New(), Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)
	return o
}

// A worker waits while foreground writes keep arriving, even when the queue is
// full, and proceeds once they stop.
func TestWorkersWaitForWriteBurst(t *testing.T) {
	o := newGateOptimizer(t)
	for i := 0; i < cap(o.queue); i++ {
		o.queue <- "k"
	}
	done := make(chan bool, 1)
	// A timestamp in the future keeps the foreground "busy" without relying
	// on a writer goroutine being scheduled every couple of milliseconds.
	atomic.StoreInt64(&o.lastForegroundWrite, time.Now().Add(time.Hour).UnixNano())
	go func() { done <- o.waitForForegroundQuiet() }()

	select {
	case <-done:
		t.Fatal("worker proceeded during a write burst with a full queue")
	case <-time.After(100 * time.Millisecond):
	}
	atomic.StoreInt64(&o.lastForegroundWrite, time.Now().Add(-time.Second).UnixNano())
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("worker aborted")
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not resume after the burst")
	}
	if atomic.LoadInt64(&o.deferralStart) != 0 {
		t.Fatal("burst deadline not reset after quiet")
	}
}

// A continuous write workload cannot starve the optimizer forever.
func TestWorkersResumeAfterBurstDeadline(t *testing.T) {
	o := newGateOptimizer(t)
	atomic.StoreInt64(&o.deferralStart, time.Now().Add(-6*time.Second).UnixNano())
	o.NoteForegroundWrite()
	start := time.Now()
	if !o.waitForForegroundQuiet() {
		t.Fatal("worker aborted")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("expired burst deadline did not release the worker")
	}
}
