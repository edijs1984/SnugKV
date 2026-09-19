package server

import (
	"bufio"
	"net"
	"sync"
	"time"
)

// serializedResponseWriter serializes complete RESP responses, not individual
// net.Conn.Write calls. This prevents a partial socket write from allowing an
// asynchronous Pub/Sub push to interleave with an ordinary command response.
type serializedResponseWriter struct {
	server *TCPServer
	conn   net.Conn
	buf    *bufio.Writer
	mu     sync.Mutex
}

func newSerializedResponseWriter(server *TCPServer, conn net.Conn) *serializedResponseWriter {
	return &serializedResponseWriter{
		server: server,
		conn:   conn,
		buf:    bufio.NewWriterSize(conn, 256<<10),
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

	if len(response) > w.buf.Available() {
		if err := w.conn.SetWriteDeadline(time.Now().Add(time.Duration(w.server.config.WriteTimeoutMS) * time.Millisecond)); err != nil {
			return err
		}
	}
	_, err := w.buf.Write(response)
	return err
}

func (w *serializedResponseWriter) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *serializedResponseWriter) flushLocked() error {
	if w.buf.Buffered() == 0 {
		return nil
	}
	if err := w.conn.SetWriteDeadline(time.Now().Add(time.Duration(w.server.config.WriteTimeoutMS) * time.Millisecond)); err != nil {
		return err
	}
	return w.buf.Flush()
}
