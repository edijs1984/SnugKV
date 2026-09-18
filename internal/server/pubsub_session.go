package server

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

func pubSubConfirmation(kind string, target []byte, nullTarget bool, count int) []byte {
	targetReply := nullBulk()
	if !nullTarget {
		targetReply = formatBulkString(target)
	}
	return array(
		formatBulkString([]byte(kind)),
		targetReply,
		integer(int64(count)),
	)
}

func sortedSubscriptionNames(values map[string]struct{}) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (session *pubSubSession) subscribe(values [][]byte, pattern bool) error {
	h := session.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if session.closed {
		return errors.New("ERR client connection is closed")
	}

	kind := "subscribe"
	registry := h.channels
	owned := session.channels
	if pattern {
		kind = "psubscribe"
		registry = h.patterns
		owned = session.patterns
	}

	response := make([]byte, 0)
	for _, value := range values {
		name := string(value)
		if _, exists := owned[name]; !exists {
			owned[name] = struct{}{}
			set := registry[name]
			if set == nil {
				set = make(map[*pubSubSession]struct{})
				registry[name] = set
			}
			set[session] = struct{}{}
		}
		response = append(response, pubSubConfirmation(kind, value, false, session.subscriptionCountLocked())...)
	}
	return session.send(response)
}

func (session *pubSubSession) subscribeShard(values [][]byte) error {
	h := session.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if session.closed {
		return errors.New("ERR client connection is closed")
	}

	response := make([]byte, 0)
	for _, value := range values {
		name := string(value)
		if _, exists := session.shardChannels[name]; !exists {
			session.shardChannels[name] = struct{}{}
			set := h.shardChannels[name]
			if set == nil {
				set = make(map[*pubSubSession]struct{})
				h.shardChannels[name] = set
			}
			set[session] = struct{}{}
		}
		response = append(response, pubSubConfirmation("ssubscribe", value, false, session.shardSubscriptionCountLocked())...)
	}
	return session.send(response)
}

func (session *pubSubSession) unsubscribe(values [][]byte, pattern bool) error {
	h := session.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if session.closed {
		return errors.New("ERR client connection is closed")
	}

	kind := "unsubscribe"
	registry := h.channels
	owned := session.channels
	if pattern {
		kind = "punsubscribe"
		registry = h.patterns
		owned = session.patterns
	}

	if len(values) == 0 && len(owned) == 0 {
		return session.send(pubSubConfirmation(kind, nil, true, session.subscriptionCountLocked()))
	}
	if len(values) == 0 {
		names := sortedSubscriptionNames(owned)
		values = make([][]byte, 0, len(names))
		for _, name := range names {
			values = append(values, []byte(name))
		}
	}

	response := make([]byte, 0)
	for _, value := range values {
		name := string(value)
		if _, exists := owned[name]; exists {
			delete(owned, name)
			set := registry[name]
			delete(set, session)
			if len(set) == 0 {
				delete(registry, name)
			}
		}
		response = append(response, pubSubConfirmation(kind, value, false, session.subscriptionCountLocked())...)
	}
	return session.send(response)
}

func (session *pubSubSession) unsubscribeShard(values [][]byte) error {
	h := session.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if session.closed {
		return errors.New("ERR client connection is closed")
	}

	if len(values) == 0 && len(session.shardChannels) == 0 {
		return session.send(pubSubConfirmation("sunsubscribe", nil, true, session.shardSubscriptionCountLocked()))
	}
	if len(values) == 0 {
		names := sortedSubscriptionNames(session.shardChannels)
		values = make([][]byte, 0, len(names))
		for _, name := range names {
			values = append(values, []byte(name))
		}
	}

	response := make([]byte, 0)
	for _, value := range values {
		name := string(value)
		if _, exists := session.shardChannels[name]; exists {
			delete(session.shardChannels, name)
			set := h.shardChannels[name]
			delete(set, session)
			if len(set) == 0 {
				delete(h.shardChannels, name)
			}
		}
		response = append(response, pubSubConfirmation("sunsubscribe", value, false, session.shardSubscriptionCountLocked())...)
	}
	return session.send(response)
}

func (session *pubSubSession) clearLocked() {
	h := session.hub
	for channel := range session.channels {
		set := h.channels[channel]
		delete(set, session)
		if len(set) == 0 {
			delete(h.channels, channel)
		}
	}
	for pattern := range session.patterns {
		set := h.patterns[pattern]
		delete(set, session)
		if len(set) == 0 {
			delete(h.patterns, pattern)
		}
	}
	for channel := range session.shardChannels {
		set := h.shardChannels[channel]
		delete(set, session)
		if len(set) == 0 {
			delete(h.shardChannels, channel)
		}
	}
	session.channels = make(map[string]struct{})
	session.patterns = make(map[string]struct{})
	session.shardChannels = make(map[string]struct{})
}

func (session *pubSubSession) reset() error {
	h := session.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if session.closed {
		return errors.New("ERR client connection is closed")
	}
	session.clearLocked()
	return session.send([]byte("+RESET\r\n"))
}

func (session *pubSubSession) subscribedPing(payload []byte) error {
	h := session.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	return session.send(array(
		formatBulkString([]byte("pong")),
		formatBulkString(payload),
	))
}

func (session *pubSubSession) subscribedQuit() error {
	h := session.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if session.closed {
		return errors.New("ERR client connection is closed")
	}
	session.clearLocked()
	return session.send([]byte("+OK\r\n"))
}

func (session *pubSubSession) handleCommand(args [][]byte) (handled, quit bool, err error) {
	if len(args) == 0 {
		return false, false, nil
	}
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "SUBSCRIBE", "PSUBSCRIBE":
		if len(args) < 2 {
			return true, false, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
		}
		return true, false, session.subscribe(args[1:], cmd == "PSUBSCRIBE")
	case "SSUBSCRIBE":
		if len(args) < 2 {
			return true, false, errors.New("ERR wrong number of arguments for 'ssubscribe' command")
		}
		return true, false, session.subscribeShard(args[1:])
	case "UNSUBSCRIBE", "PUNSUBSCRIBE":
		return true, false, session.unsubscribe(args[1:], cmd == "PUNSUBSCRIBE")
	case "SUNSUBSCRIBE":
		return true, false, session.unsubscribeShard(args[1:])
	case "RESET":
		if len(args) != 1 {
			return true, false, errors.New("ERR wrong number of arguments for 'reset' command")
		}
		return true, false, session.reset()
	case "PING":
		if !session.active() {
			return false, false, nil
		}
		if len(args) > 2 {
			return true, false, errors.New("ERR wrong number of arguments for 'ping' command")
		}
		var payload []byte
		if len(args) == 2 {
			payload = args[1]
		}
		return true, false, session.subscribedPing(payload)
	case "QUIT":
		if !session.active() {
			return false, false, nil
		}
		if len(args) != 1 {
			return true, false, errors.New("ERR wrong number of arguments for 'quit' command")
		}
		return true, true, session.subscribedQuit()
	}

	if session.active() {
		return true, false, fmt.Errorf("ERR Can't execute '%s': only (P|S)SUBSCRIBE / (P|S)UNSUBSCRIBE / PING / QUIT / RESET are allowed in this context", strings.ToLower(cmd))
	}
	return false, false, nil
}

func (s *Server) executePubSubConnectionCommand(session *pubSubSession, args [][]byte) (handled, quit bool, err error) {
	start := time.Now()
	handled, quit, err = session.handleCommand(args)
	if !handled {
		return handled, quit, err
	}
	atomic.AddUint64(&s.commands, 1)
	name := "unknown"
	if len(args) > 0 {
		candidate := strings.ToUpper(string(args[0]))
		if _, ok := commandTable[candidate]; ok {
			name = candidate
		}
	}
	s.metrics.Observe(name, time.Since(start), err != nil)
	return handled, quit, err
}

func isPubSubConnectionCommand(
	args [][]byte,
) bool {
	if len(args) == 0 {
		return false
	}

	switch strings.ToUpper(
		string(args[0]),
	) {
	case "SUBSCRIBE",
		"UNSUBSCRIBE",
		"PSUBSCRIBE",
		"PUNSUBSCRIBE",
		"SSUBSCRIBE",
		"SUNSUBSCRIBE",
		"RESET":
		return true
	default:
		return false
	}
}
