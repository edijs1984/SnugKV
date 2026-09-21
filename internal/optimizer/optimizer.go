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
	MinAttemptInterval                                                  time.Duration
}

func Default() Config {
	return Config{
		Workers:            2,
		QueueDepth:         65536,
		MaxScratchBytes:    128 << 20,
		MaxBytesPerSecond:  64 << 20,
		CPUPercent:         50,
		MinRewriteInterval: 5 * time.Minute,
		MinAttemptInterval: 30 * time.Second,
	}
}

type Stats struct {
	Queued, Rewritten, Skipped, Stale, Dropped uint64
	QueueDepth, QueueCapacity                  int
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
	if c.Workers < 1 || c.Workers > 64 || c.QueueDepth < 1 || c.MaxScratchBytes < 16<<20 || c.MaxBytesPerSecond < 1 || c.CPUPercent < 1 || c.CPUPercent > 100 || c.MinRewriteInterval < 0 || c.MinAttemptInterval < 0 {
		return nil, errors.New("invalid optimizer configuration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := &Optimizer{store: store, config: c, ctx: ctx, cancel: cancel, queue: make(chan string, c.QueueDepth), window: time.Now()}
	for i := 0; i < c.Workers; i++ {
		o.wg.Add(1)
		go o.worker()
	}

	o.wg.Add(1)
	go o.maintenance()

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
	if limit <= 0 {
		return
	}

	// Sampling is recovery work for keys whose direct write-time enqueue was
	// dropped. Never generate a sampling burst larger than the queue can accept,
	// otherwise catch-up creates its own avoidable drop storm.
	available := cap(o.queue) - len(o.queue)
	if available <= 0 {
		return
	}
	if limit > available {
		limit = available
	}

	for _, key := range o.store.SampleKeys(limit) {
		if !o.Queue(key) {
			break
		}
	}
}
func (o *Optimizer) Stats() Stats {
	return Stats{
		Queued:        atomic.LoadUint64(&o.queued),
		Rewritten:     atomic.LoadUint64(&o.rewritten),
		Skipped:       atomic.LoadUint64(&o.skipped),
		Stale:         atomic.LoadUint64(&o.stale),
		Dropped:       atomic.LoadUint64(&o.dropped),
		QueueDepth:    len(o.queue),
		QueueCapacity: cap(o.queue),
	}
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
func (o *Optimizer) maintenance() {
	defer o.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	const minDeadBytes uint64 = 1 << 20

	for {
		select {
		case <-o.ctx.Done():
			return

		case <-ticker.C:
			// During initial catch-up, prioritize representation rewrites.
			// Compaction copies arena data and competes for CPU/memory bandwidth.
			if len(o.queue) != 0 {
				continue
			}

			m := o.store.Memory()

			if m.ArenaBytes == 0 || m.ArenaBytes <= m.ArenaLiveBlockBytes {
				continue
			}

			dead := m.ArenaBytes - m.ArenaLiveBlockBytes

			// Compact only when at least 1 MiB is dead and
			// dead storage is at least 25% of the arena.
			if dead < minDeadBytes || dead*4 < m.ArenaBytes {
				continue
			}

			o.store.Compact(uint64(o.config.MaxScratchBytes))
		}
	}
}

func (o *Optimizer) retrySoon(key string) {
	const delay = time.Second

	time.AfterFunc(delay, func() {
		select {
		case <-o.ctx.Done():
			return
		default:
			o.Queue(key)
		}
	})
}


func (o *Optimizer) cpuPercentForBacklog(depth int) int {
	cpu := o.config.CPUPercent
	capacity := cap(o.queue)
	if capacity <= 0 || depth <= 0 {
		return cpu
	}

	// Heavy backlog: prioritize foreground command execution. With the default
	// two workers on a four-core machine, 15% per worker keeps compression
	// progress moving without consuming a large fraction of available CPU.
	if depth*4 >= capacity {
		if cpu > 15 {
			return 15
		}
		return cpu
	}

	// Moderate backlog: start yielding before the queue becomes saturated.
	if depth*16 >= capacity {
		if cpu > 25 {
			return 25
		}
	}

	return cpu
}

func (o *Optimizer) worker() {
	defer o.wg.Done()
	var candidateScratch []byte
	const maxRetainedCandidateScratch = 1 << 20
	for {
		select {
		case <-o.ctx.Done():
			return
		case key := <-o.queue:
			start := time.Now()

			rawBytes, eligible := o.store.OptimizationEligible(
				key,
				o.config.MinRewriteInterval,
				o.config.MinAttemptInterval,
			)
			if !eligible {
				atomic.AddUint64(&o.skipped, 1)
				continue
			}

			if !o.reserve(rawBytes) {
				atomic.AddUint64(&o.skipped, 1)
				continue
			}

			candidate, ok := o.store.CandidateInto(key, rawBytes, candidateScratch)
			if !ok {
				atomic.AddUint64(&o.skipped, 1)
				o.release(rawBytes)
				continue
			}
			if cap(candidate.Value) <= maxRetainedCandidateScratch {
				candidateScratch = candidate.Value[:0]
			} else {
				candidateScratch = nil
			}

			// JSON-shape learning belongs off the foreground SET path. Observe the
			// dequeued value once here; once the admission threshold is reached,
			// EncodeCandidate below can immediately select the shared shape.
			if o.store.OptimizationClassForValue(candidate.Value) == engine.OptimizationJSON {
				o.store.ObserveJSONShape(candidate.Key, candidate.Value)
			}

			// The optimizer requires at least 16 bytes of absolute savings.
			// If the current physical representation is already smaller than
			// 16 bytes, no possible codec can satisfy that requirement.
			if candidate.EncodedBytes < 16 {
				atomic.AddUint64(&o.skipped, 1)
				o.release(rawBytes)
				continue
			}

			record := o.store.EncodeCandidate(candidate)
			saving := candidate.EncodedBytes - len(record.Data)

			// A key that does not yet own optimizer metadata must also earn back
			// that allocation. Otherwise enabling optimization can increase the
			// total footprint even when the encoded payload is smaller.
			requiredSaving := 16 + candidate.AdditionalMetadataBytes
			if saving < requiredSaving || saving*8 < candidate.EncodedBytes {
				atomic.AddUint64(&o.skipped, 1)
				o.release(rawBytes)
				continue
			}

			// Record the attempt only after a representation has proven to be a
			// net memory win. Sparse keys therefore stay metadata-free.
			if !o.store.MarkOptimizationAttempt(
				key,
				o.config.MinRewriteInterval,
				o.config.MinAttemptInterval,
			) {
				o.release(rawBytes)
				atomic.AddUint64(&o.skipped, 1)
				continue
			}

			if o.store.Rewrite(candidate, record) {
				atomic.AddUint64(&o.rewritten, 1)
			} else {
				atomic.AddUint64(&o.stale, 1)
			}
			o.release(rawBytes)

			// Background optimization must not compete aggressively with a sustained
			// write burst. A growing queue is the pressure signal: while backlog is
			// high, yield below the configured steady-state CPU budget, then return
			// to that budget as the queue drains. This defers work rather than
			// guessing that queued values are incompressible.
			cpuPercent := o.cpuPercentForBacklog(len(o.queue))

			pause := time.Since(start) * time.Duration(100-cpuPercent) / time.Duration(cpuPercent)
			if pause <= 0 {
				continue
			}
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
