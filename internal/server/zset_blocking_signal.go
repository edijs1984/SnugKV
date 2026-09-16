package server

import "strings"

func (s *Server) signalZSetAvailability(args [][]byte, response []byte) {
	if len(args) == 0 {
		return
	}
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "ZADD", "ZINCRBY":
		if len(args) > 1 {
			s.signalZSetKey(string(args[1]))
		}
	case "ZUNIONSTORE", "ZINTERSTORE", "ZDIFFSTORE", "ZRANGESTORE":
		if len(args) > 1 {
			s.signalZSetKey(string(args[1]))
		}
	case "RENAME":
		if len(args) > 2 && strings.HasPrefix(string(response), "+OK") {
			s.signalZSetKey(string(args[2]))
		}
	case "RENAMENX":
		if len(args) > 2 && string(response) == ":1\r\n" {
			s.signalZSetKey(string(args[2]))
		}
	case "COPY":
		if len(args) > 2 && string(response) == ":1\r\n" && s.store.Type(string(args[2])) == "zset" {
			s.signalZSetKey(string(args[2]))
		}
	}
}
