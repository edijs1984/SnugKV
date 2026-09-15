//go:build linux

package server

import (
	"net"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	pollIn     = int16(0x0001)
	pollErr    = int16(0x0008)
	pollHup    = int16(0x0010)
	pollNVal   = int16(0x0020)
	pollRDHup  = int16(0x2000)
)

type linuxPollFD struct {
	fd      int32
	events  int16
	revents int16
}

// watchConnectionDisconnect reports a peer close without consuming bytes from
// the connection. That matters for blocked clients because a subsequent
// pipelined command may already be buffered while BLPOP/BZPOPMIN is sleeping.
func watchConnectionDisconnect(conn net.Conn) (<-chan struct{}, func()) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return nil, func() {}
	}

	disconnected := make(chan struct{})
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopWatch := func() { stopOnce.Do(func() { close(stop) }) }

	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			if tcpPeerGone(tcp) {
				close(disconnected)
				return
			}

			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()

	return disconnected, stopWatch
}

func tcpPeerGone(conn *net.TCPConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return true
	}

	gone := false
	err = raw.Control(func(fd uintptr) {
		pollfd := linuxPollFD{
			fd:     int32(fd),
			events: pollIn | pollRDHup,
		}
		timeout := syscall.Timespec{}

		_, _, errno := syscall.Syscall6(
			syscall.SYS_PPOLL,
			uintptr(unsafe.Pointer(&pollfd)),
			1,
			uintptr(unsafe.Pointer(&timeout)),
			0,
			0,
			0,
		)
		if errno != 0 {
			if errno != syscall.EINTR {
				gone = true
			}
			return
		}

		gone = pollfd.revents&(pollErr|pollHup|pollNVal|pollRDHup) != 0
	})

	return err != nil || gone
}
