package server

import "strings"

func (s *Server) signalListAvailability(args [][]byte, response []byte) {
	if len(args) == 0 {
		return
	}
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "LPUSH", "RPUSH", "LPUSHX", "RPUSHX":
		if len(args) > 1 {
			s.signalListKey(string(args[1]))
		}
	case "LMOVE", "RPOPLPUSH":
		if len(args) > 2 && string(response) != "$-1\r\n" {
			s.signalListKey(string(args[2]))
		}
	}
}
