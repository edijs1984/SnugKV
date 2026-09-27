package server

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

type monitorSubscription struct {
	queue chan []byte
	done chan struct{}
	conn net.Conn
	once sync.Once
	deliveryMu sync.Mutex
}

func (s *Server) removeMonitor(m *monitorSubscription) {
	if m == nil { return }
	m.once.Do(func() {
		s.monitorMu.Lock()
		delete(s.monitors, m)
		s.monitorCount.Add(-1)
		s.monitorMu.Unlock()
		close(m.done)
	})
	// Wait for any in-flight event before RESET writes its reply.
	m.deliveryMu.Lock()
	m.deliveryMu.Unlock()
}

func (s *Server) addMonitor(conn net.Conn, write func([]byte) error) *monitorSubscription {
	m := &monitorSubscription{queue: make(chan []byte, 128), done: make(chan struct{}), conn: conn}
	s.monitorMu.Lock()
	if s.monitors == nil { s.monitors = make(map[*monitorSubscription]struct{}) }
	s.monitors[m] = struct{}{}
	s.monitorCount.Add(1)
	s.monitorMu.Unlock()
	go func() {
		defer s.removeMonitor(m)
		for {
			select {
			case <-m.done:
				return
			case line := <-m.queue:
				m.deliveryMu.Lock()
				select {
				case <-m.done:
					m.deliveryMu.Unlock()
					return
				default:
				}
				err := write(line)
				m.deliveryMu.Unlock()
				if err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	return m
}

func monitorQuote(arg []byte) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range arg {
		switch c {
		case '\\', '"':
			b.WriteByte('\\'); b.WriteByte(c)
		case '\n': b.WriteString("\\n")
		case '\r': b.WriteString("\\r")
		case '\t': b.WriteString("\\t")
		case '\a': b.WriteString("\\a")
		case '\b': b.WriteString("\\b")
		default:
			if c >= 32 && c <= 126 { b.WriteByte(c) } else {
				b.WriteString("\\x"); b.WriteByte(hex[c>>4]); b.WriteByte(hex[c&15])
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func monitorVisible(args [][]byte) bool {
	if len(args) == 0 { return false }
	name := strings.ToUpper(string(args[0]))
	info, ok := commandTable[name]
	if !ok || len(args) < info.min || (info.max > 0 && len(args) > info.max) { return false }
	// Credentials can be embedded in these commands. Never publish them.
	switch name {
	case "AUTH", "HELLO", "ACL", "CONFIG", "MIGRATE", "MONITOR":
		return false
	}
	canonical := strings.ToLower(name)
	if len(args) > 1 {
		if _, admin := redisACLCategoryCommands["admin"][canonical+"|"+strings.ToLower(string(args[1]))]; admin { return false }
	}
	_, admin := redisACLCategoryCommands["admin"][canonical]
	return !admin
}

func (s *Server) feedMonitor(client *clientSession, args [][]byte, response []byte) {
	if client == nil { return }
	s.feedMonitorSource(client.remoteAddr, args, response)
}

func (s *Server) feedMonitorSource(source string, args [][]byte, response []byte) {
	if s.monitorCount.Load() == 0 ||
		bytes.Equal(response, []byte("+QUEUED\r\n")) || !monitorVisible(args) { return }
	// Bound individual event allocation as well as queued event count.
	size := 0
	for _, arg := range args { size += len(arg) }
	if size > 64<<10 {
		s.monitorMu.Lock()
		for m := range s.monitors { _ = m.conn.Close() }
		s.monitorMu.Unlock()
		return
	}
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "+%d.%06d [0 %s]", now.Unix(), now.Nanosecond()/1000, source)
	for _, arg := range args { b.WriteByte(' '); b.WriteString(monitorQuote(arg)) }
	b.WriteString("\r\n")
	line := []byte(b.String())
	s.monitorMu.Lock()
	defer s.monitorMu.Unlock()
	for m := range s.monitors {
		select {
		case m.queue <- line:
		default:
			_ = m.conn.Close()
		}
	}
}

func monitorScriptCommand(args [][]byte) bool {
	if len(args) == 0 { return false }
	switch strings.ToUpper(string(args[0])) {
	case "EVAL", "EVALSHA", "EVAL_RO", "EVALSHA_RO", "FCALL", "FCALL_RO":
		return true
	}
	return false
}
