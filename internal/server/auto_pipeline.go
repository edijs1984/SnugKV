package server

import (
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

type autoSetBatcher struct {
	server    *Server
	maxBatch  int
	maxWait   time.Duration
	queue     chan *autoSetRequest
	stop      chan struct{}
	stopped   chan struct{}
	closeOnce sync.Once
}

func newAutoSetBatcher(server *Server, maxBatch int, maxWait time.Duration) *autoSetBatcher {
	if maxBatch < 2 {
		maxBatch = 2
	}
	if maxWait <= 0 {
		maxWait = 25 * time.Microsecond
	}
	b := &autoSetBatcher{
		server:   server,
		maxBatch: maxBatch,
		maxWait:  maxWait,
		queue:    make(chan *autoSetRequest, maxBatch*64),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *autoSetBatcher) close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		close(b.stop)
		<-b.stopped
	})
}

func (b *autoSetBatcher) submit(key, value []byte) (bool, error) {
	req := &autoSetRequest{
		key:   key,
		value: value,
		done:  make(chan autoSetResult, 1),
	}
	select {
	case b.queue <- req:
	case <-b.stop:
		return false, nil
	}

	select {
	case result := <-req.done:
		return result.handled, result.err
	case <-b.stop:
		return false, nil
	}
}

func (b *autoSetBatcher) run() {
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
			// Drain requests already queued before paying timer/select overhead.
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

func (b *autoSetBatcher) failPending() {
	for {
		select {
		case req := <-b.queue:
			req.done <- autoSetResult{}
		default:
			return
		}
	}
}
