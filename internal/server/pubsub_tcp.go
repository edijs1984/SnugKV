package server

import (
	"net"
	"sync"
)

// serializedResponseWriter serializes complete RESP responses, not individual
// net.Conn.Write calls. This prevents a partial socket write from allowing an
// asynchronous Pub/Sub push to interleave with an ordinary command response.
type serializedResponseWriter struct {
	server *TCPServer
	conn   net.Conn
	mu     sync.Mutex
}

func (w *serializedResponseWriter) write(response []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.server.write(w.conn, response)
}
