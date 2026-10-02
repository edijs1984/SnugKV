package server

import (
	"runtime"
	"snugkv/internal/index"
	"sync"
	"time"
)

type autoSetRequest struct {
	key   []byte
	value []byte
	done  chan autoSetResult
}

type autoSetResult struct {
	handled bool
	err     error
}

type autoSetBatchLane struct {
	server   *Server
	maxBatch int
	maxWait  time.Duration
	queue    chan *autoSetRequest
	stop     chan struct{}
	stopped  chan struct{}
}

type autoSetBatcher struct {
	lanes     []*autoSetBatchLane
	laneMask  uint64
	closeOnce sync.Once
}

func newAutoSetBatcher(server *Server, maxBatch int, maxWait time.Duration) *autoSetBatcher {
	if maxBatch < 2 {
		maxBatch = 2
	}
	if maxWait <= 0 {
		maxWait = 25 * time.Microsecond
	}

	laneCount := runtime.GOMAXPROCS(0)
	if laneCount < 1 {
		laneCount = 1
	}
	// Keep lane selection as a cheap mask while avoiding excessive scheduler
	// fragmentation on large machines. Four lanes on a four-core host preserve
	// shard parallelism while still leaving enough concurrent requests to batch.
	pow2 := 1
	for pow2 < laneCount && pow2 < 16 {
		pow2 <<= 1
	}
	laneCount = pow2

	b := &autoSetBatcher{
		lanes:    make([]*autoSetBatchLane, laneCount),
		laneMask: uint64(laneCount - 1),
	}
	for i := range b.lanes {
		lane := &autoSetBatchLane{
			server:   server,
			maxBatch: maxBatch,
			maxWait:  maxWait,
			queue:    make(chan *autoSetRequest, maxBatch*16),
			stop:     make(chan struct{}),
			stopped:  make(chan struct{}),
		}
		b.lanes[i] = lane
		go lane.run()
	}
	return b
}

func (b *autoSetBatcher) close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		for _, lane := range b.lanes {
			close(lane.stop)
		}
		for _, lane := range b.lanes {
			<-lane.stopped
		}
	})
}

func (b *autoSetBatcher) submit(key, value []byte) (bool, error) {
	if b == nil || len(b.lanes) == 0 {
		return false, nil
	}
	hash := index.HashBytes(key)
	lane := b.lanes[(hash>>32)&b.laneMask]

	req := &autoSetRequest{
		key:   key,
		value: value,
		done:  make(chan autoSetResult, 1),
	}
	select {
	case lane.queue <- req:
	case <-lane.stop:
		return false, nil
	}

	select {
	case result := <-req.done:
		return result.handled, result.err
	case <-lane.stop:
		return false, nil
	}
}

func (b *autoSetBatchLane) run() {
	defer close(b.stopped)

	batch := make([]*autoSetRequest, 0, b.maxBatch)
	keys := make([][]byte, 0, b.maxBatch)
	values := make([][]byte, 0, b.maxBatch)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		var first *autoSetRequest
		select {
		case <-b.stop:
			b.failPending()
			return
		case first = <-b.queue:
		}

		batch = batch[:0]
		batch = append(batch, first)

		timer.Reset(b.maxWait)
	collect:
		for len(batch) < b.maxBatch {
			// Drain already queued work first. This is the hot heavy-load path
			// and avoids a timer/select round trip for every request.
			select {
			case req := <-b.queue:
				batch = append(batch, req)
				continue
			default:
			}

			select {
			case req := <-b.queue:
				batch = append(batch, req)
			case <-timer.C:
				break collect
			case <-b.stop:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				for _, req := range batch {
					req.done <- autoSetResult{}
				}
				b.failPending()
				return
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}

		if len(batch) == 1 {
			msg := [][]byte{[]byte("SET"), batch[0].key, batch[0].value}
			_, handled, err := b.server.executeAuthorizedConcurrentSet(msg)
			batch[0].done <- autoSetResult{handled: handled, err: err}
			continue
		}

		keys = keys[:0]
		values = values[:0]
		for _, req := range batch {
			keys = append(keys, req.key)
			values = append(values, req.value)
		}

		handled, err := b.server.executeAuthorizedConcurrentSetBatch(keys, values)
		result := autoSetResult{handled: handled, err: err}
		for _, req := range batch {
			req.done <- result
		}
	}
}

func (b *autoSetBatchLane) failPending() {
	for {
		select {
		case req := <-b.queue:
			req.done <- autoSetResult{}
		default:
			return
		}
	}
}
