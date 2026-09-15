//go:build !linux

package server

import "net"

// Non-Linux builds keep the cancellable blocking-command API but currently rely
// on command timeout/server shutdown rather than a platform socket-hangup poll.
func watchConnectionDisconnect(conn net.Conn) (<-chan struct{}, func()) {
	return nil, func() {}
}
