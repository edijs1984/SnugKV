package server

import (
	"net"
	"sync"
)

// serializedWriteConn allows the normal connection goroutine and Pub/Sub
// publishers to write to the same socket without interleaving RESP frames.
// Reads remain delegated directly to the underlying connection.
type serializedWriteConn struct {
	net.Conn
	mu sync.Mutex
}

func (c *serializedWriteConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.Write(p)
}
