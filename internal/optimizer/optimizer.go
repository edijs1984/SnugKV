// Package optimizer schedules bounded optimistic representation rewrites.
package optimizer

import (
	"context"
	"errors"
	"snugkv/internal/engine"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Workers, QueueDepth, MaxScratchBytes, MaxBytesPerSecond, CPUPercent int
	MinRewriteInterval                                                  time.Duration
}

func Default() Config {
	return Config{Workers: 1, QueueDepth: 256, MaxScratchBytes: 128 << 20, MaxBytesPerSecond: 8 << 20, CPUPercent: 10, MinRewriteInterval: 5 * time.Minute}
}

type Stats struct {
	Queued, Rewritten, Skipped, Stale, Dropped uint64
	QueueDepth                                 int
}
type Optimizer struct {
	store                                      *engine.Store
	config                                     Config
	ctx                                        context.Context
	cancel                                     context.CancelFunc
	wg                                         sync.WaitGroup
	queue                                      chan string
	mu                                         sync.Mutex
	scratch, bytes                             int
	window                                     time.Time
	queued, rewritten, skipped, stale, dropped uint64
}

func New(store *engine.Store, c Config) (*Optimizer, error) {
	if c.Workers < 1 || c.Workers > 64 || c.QueueDepth < 1 || c.MaxScratchBytes < 16<<20 || c.MaxBytesPerSecond < 1 || c.CPUPercent < 1 || c.CPUPercent > 100 || c.MinRewriteInterval < 0 {
		return nil, errors.New("invalid optimizer configuration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := &Optimizer{store: store, config: c, ctx: ctx, cancel: cancel, queue: make(chan string, c.QueueDepth), window: time.Now()}
	for i := 0; i < c.Workers; i++ {
		o.wg.Add(1)
		go o.worker()
	}
	return o, nil
}
func (o *Optimizer) Close() { o.cancel(); o.wg.Wait() }
func (o *Optimizer) Queue(key string) bool {
	select {
	case <-o.ctx.Done():
		return false
	default:
	}
	select {
	case o.queue <- key:
		atomic.AddUint64(&o.queued, 1)
		return true
	default:
		atomic.AddUint64(&o.dropped, 1)
		return false
	}
}
func (o *Optimizer) Sample(limit int) {
	for _, key := range o.store.SampleKeys(limit) {
		o.Queue(key)
	}
}
func (o *Optimizer) Stats() Stats {
	return Stats{atomic.LoadUint64(&o.queued), atomic.LoadUint64(&o.rewritten), atomic.LoadUint64(&o.skipped), atomic.LoadUint64(&o.stale), atomic.LoadUint64(&o.dropped), len(o.queue)}
}
func (o *Optimizer) reserve(n int) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if time.Since(o.window) >= time.Second {
		o.window = time.Now()
		o.bytes = 0
	}
	if n > o.config.MaxBytesPerSecond-o.bytes || n > (o.config.MaxScratchBytes-o.scratch-(16<<20))/64 {
		return false
	}
	o.bytes += n
	o.scratch += (16 << 20) + n*64
	return true
}
func (o *Optimizer) release(n int) { o.mu.Lock(); o.scratch -= (16 << 20) + n*64; o.mu.Unlock() }
func (o *Optimizer) worker() {
	defer o.wg.Done()
	for {
		select {
		case <-o.ctx.Done():
			return
		case key := <-o.queue:
			start := time.Now()
			_, size, _, found := o.store.Encoding(key)
			if !found || !o.reserve(size) {
				atomic.AddUint64(&o.skipped, 1)
				continue
			}
			candidate, ok := o.store.Candidate(key, size)
			if !ok || candidate.Heat == "write-heavy" || time.Since(candidate.LastRewrite) < o.config.MinRewriteInterval {
				atomic.AddUint64(&o.skipped, 1)
				o.release(size)
				continue
			}
			record := o.store.EncodeCandidate(candidate)
			// Hysteresis requires at least 16 bytes and 12.5% improvement.
			saving := candidate.EncodedBytes - len(record.Data)
			if saving < 16 || saving*8 < candidate.EncodedBytes {
				atomic.AddUint64(&o.skipped, 1)
			} else if o.store.Rewrite(candidate, record) {
				atomic.AddUint64(&o.rewritten, 1)
			} else {
				atomic.AddUint64(&o.stale, 1)
			}
			o.release(size)
			// Duty-cycle throttling bounds worker activity by wall-clock work time.
			pause := time.Since(start) * time.Duration(100-o.config.CPUPercent) / time.Duration(o.config.CPUPercent)
			timer := time.NewTimer(pause)
			select {
			case <-o.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
