package server

import (
	"errors"
	"io"
	"net"
	"time"
)

// Subscribers receive pushes through a bounded per-session queue drained by a
// sender goroutine, so one client that stops reading cannot stall PUBLISH for
// everyone else. A subscriber is disconnected when its queue is full or when
// the sender fails `attempts` times in a row to make progress, each attempt
// bounded by `timeout`. It then sees a closed connection and can resubscribe.

const (
	defaultPubSubSendAttempts = 5
	defaultPubSubSendTimeout  = time.Second
	defaultPubSubQueueSize    = 1024
)

// configure sets the delivery policy. Zero values keep the defaults.
func (h *pubSubHub) configure(attempts int, timeout time.Duration, queueSize int) {
	h.attempts.Store(int64(attempts))
	h.timeoutNS.Store(int64(timeout))
	h.queueSize.Store(int64(queueSize))
}

func (h *pubSubHub) policy() (int, time.Duration, int) {
	attempts, timeout, queue := int(h.attempts.Load()), time.Duration(h.timeoutNS.Load()), int(h.queueSize.Load())
	if attempts <= 0 {
		attempts = defaultPubSubSendAttempts
	}
	if timeout <= 0 {
		timeout = defaultPubSubSendTimeout
	}
	if queue <= 0 {
		queue = defaultPubSubQueueSize
	}
	return attempts, timeout, queue
}

// setPush switches the session to queued delivery. onDrop is called, once,
// after the hub gave up on the subscriber, and should close its connection.
func (session *pubSubSession) setPush(send func([]byte, int, time.Duration) error, onDrop func()) {
	h := session.hub
	h.mu.Lock()
	session.pushSend = send
	session.onDrop = onDrop
	h.mu.Unlock()
}

// deliverLocked queues frame for the session; false means the session must be dropped.
func (h *pubSubHub) deliverLocked(session *pubSubSession, frame []byte) bool {
	if session.pushSend == nil {
		return session.send(frame) == nil
	}
	if session.push == nil {
		_, _, queue := h.policy()
		session.push = make(chan []byte, queue)
		session.stop = make(chan struct{})
		go h.runSender(session, session.push, session.stop)
	}
	select {
	case session.push <- frame:
		return true
	default:
		return false
	}
}

func (h *pubSubHub) runSender(session *pubSubSession, push <-chan []byte, stop <-chan struct{}) {
	attempts, timeout, _ := h.policy()
	for {
		select {
		case <-stop:
			return
		case frame := <-push:
			if err := session.pushSend(frame, attempts, timeout); err != nil {
				h.mu.Lock()
				live := !session.closed
				if live {
					h.removeSessionLocked(session)
					h.dropped.Add(1)
				}
				h.mu.Unlock()
				if live && session.onDrop != nil {
					session.onDrop()
				}
				return
			}
		}
	}
}

// dropFailedLocked removes sessions whose delivery failed during publish.
func (h *pubSubHub) dropFailedLocked(failed map[*pubSubSession]struct{}) {
	for session := range failed {
		if session.closed {
			continue
		}
		h.removeSessionLocked(session)
		if session.pushSend != nil {
			h.dropped.Add(1)
			if session.onDrop != nil {
				go session.onDrop()
			}
		}
	}
}

// writeResumable writes data, giving each attempt `timeout`. Progress resets
// the failure count, and a timed-out attempt resumes after the bytes already
// written so a partial write never corrupts the stream.
func writeResumable(conn net.Conn, data []byte, attempts int, timeout time.Duration) error {
	fails := 0
	for len(data) > 0 {
		if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
		n, err := conn.Write(data)
		if n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
		if err == nil {
			if n == 0 {
				return io.ErrShortWrite
			}
			fails = 0
			continue
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			return err
		}
		if n > 0 {
			fails = 0
		}
		fails++
		if fails >= attempts {
			return err
		}
	}
	return nil
}

// writePush sends an asynchronous push with the retry policy above, after any
// buffered command output so the stream stays in order.
func (w *serializedResponseWriter) writePush(frame []byte, attempts int, timeout time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	data := frame
	if len(w.buf) > 0 {
		data = append(append(make([]byte, 0, len(w.buf)+len(frame)), w.buf...), frame...)
	}
	if err := writeResumable(w.conn, data, attempts, timeout); err != nil {
		return err
	}
	w.buf = w.buf[:0]
	return nil
}
