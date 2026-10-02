package server

import (
	"net"
	"strconv"
	"sync"
)

// serializedResponseWriter serializes complete RESP responses, not individual
// net.Conn.Write calls. This prevents a partial socket write from allowing an
// asynchronous Pub/Sub push to interleave with an ordinary command response.
const serializedResponseBufferBytes = 256 << 10

type serializedResponseWriter struct {
	server *TCPServer
	conn   net.Conn
	buf    []byte
	mu     sync.Mutex
}

func newSerializedResponseWriter(server *TCPServer, conn net.Conn) *serializedResponseWriter {
	return &serializedResponseWriter{
		server: server,
		conn:   conn,
		buf:    make([]byte, 0, serializedResponseBufferBytes),
	}
}

// write is used for asynchronous pushes and other responses that must become
// visible immediately.
func (w *serializedResponseWriter) write(response []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.flushLocked(); err != nil {
		return err
	}
	return w.server.write(w.conn, response)
}

// writeBuffered appends an ordinary command response to the per-connection
// output buffer. The TCP command loop flushes when the currently buffered input
// pipeline has been drained.
func (w *serializedResponseWriter) writeBuffered(response []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(response) > cap(w.buf) {
		if err := w.flushLocked(); err != nil {
			return err
		}
		return w.server.write(w.conn, response)
	}
	if len(response) > cap(w.buf)-len(w.buf) {
		if err := w.flushLocked(); err != nil {
			return err
		}
	}
	w.buf = append(w.buf, response...)
	return nil
}

func (w *serializedResponseWriter) writeBulkBuffered(payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Ordinary bulk replies fit in the retained per-connection buffer. Build
	// the RESP header directly into that buffer so the temporary header slice
	// cannot escape and allocate on every GET.
	const maxBulkHeaderBytes = 32
	totalUpperBound := len(payload) + maxBulkHeaderBytes + 2
	if totalUpperBound <= cap(w.buf) {
		if totalUpperBound > cap(w.buf)-len(w.buf) {
			if err := w.flushLocked(); err != nil {
				return err
			}
		}
		w.buf = append(w.buf, '
func (w *serializedResponseWriter) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *serializedResponseWriter) flushLocked() error {
	if len(w.buf) == 0 {
		return nil
	}
	if err := w.server.write(w.conn, w.buf); err != nil {
		return err
	}
	w.buf = w.buf[:0]
	return nil
}
)
		w.buf = strconv.AppendInt(w.buf, int64(len(payload)), 10)
		w.buf = append(w.buf, '\r', '\n')
		w.buf = append(w.buf, payload...)
		w.buf = append(w.buf, '\r', '\n')
		return nil
	}

	// Oversized replies bypass the retained buffer. This path is uncommon and
	// intentionally keeps the small stack header used by large-value writes.
	var header [32]byte
	header[0] = '
func (w *serializedResponseWriter) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *serializedResponseWriter) flushLocked() error {
	if len(w.buf) == 0 {
		return nil
	}
	if err := w.server.write(w.conn, w.buf); err != nil {
		return err
	}
	w.buf = w.buf[:0]
	return nil
}

	framed := strconv.AppendInt(header[:1], int64(len(payload)), 10)
	framed = append(framed, '\r', '\n')

	if err := w.flushLocked(); err != nil {
		return err
	}
	if err := w.server.write(w.conn, framed); err != nil {
		return err
	}
	if err := w.server.write(w.conn, payload); err != nil {
		return err
	}
	return w.server.write(w.conn, []byte{'\r', '\n'})
}

func (w *serializedResponseWriter) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *serializedResponseWriter) flushLocked() error {
	if len(w.buf) == 0 {
		return nil
	}
	if err := w.server.write(w.conn, w.buf); err != nil {
		return err
	}
	w.buf = w.buf[:0]
	return nil
}
