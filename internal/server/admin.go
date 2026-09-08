package server

import (
	"errors"
	"net"
	"strings"
)

// OpenAdmin adds a separate loopback-only RESP diagnostics listener.
func (s *TCPServer) OpenAdmin(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("admin address must be a loopback IP")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	child := &TCPServer{listener: ln, server: s.server, config: s.config, connections: make(map[net.Conn]struct{}), done: make(chan struct{}), adminOnly: true}
	s.mu.Lock()
	if s.closing || s.admin != nil {
		s.mu.Unlock()
		ln.Close()
		return errors.New("admin listener already started or server closing")
	}
	s.admin = child
	child.wg.Add(1)
	go child.serve()
	s.mu.Unlock()
	return nil
}
func adminAllowed(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.ToUpper(string(args[0]))
	if strings.HasPrefix(name, "MORPH.") {
		return true
	}
	switch name {
	case "PING", "ECHO", "QUIT", "HELLO", "SELECT", "INFO", "COMMAND", "DBSIZE":
		return true
	}
	return false
}
