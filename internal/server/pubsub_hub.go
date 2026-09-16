package server

import (
	"snugkv/internal/engine"
	"sort"
	"sync"
)

type pubSubHub struct {
	mu            sync.Mutex
	channels      map[string]map[*pubSubSession]struct{}
	patterns      map[string]map[*pubSubSession]struct{}
	shardChannels map[string]map[*pubSubSession]struct{}
}

type pubSubSession struct {
	hub           *pubSubHub
	channels      map[string]struct{}
	patterns      map[string]struct{}
	shardChannels map[string]struct{}
	send          func([]byte) error
	closed        bool
}

var pubSubHubs sync.Map // map[*Server]*pubSubHub

func pubSubHubForServer(s *Server) *pubSubHub {
	if existing, ok := pubSubHubs.Load(s); ok {
		return existing.(*pubSubHub)
	}
	created := &pubSubHub{
		channels:      make(map[string]map[*pubSubSession]struct{}),
		patterns:      make(map[string]map[*pubSubSession]struct{}),
		shardChannels: make(map[string]map[*pubSubSession]struct{}),
	}
	actual, _ := pubSubHubs.LoadOrStore(s, created)
	return actual.(*pubSubHub)
}

func newPubSubSession(s *Server, send func([]byte) error) *pubSubSession {
	return &pubSubSession{
		hub:           pubSubHubForServer(s),
		channels:      make(map[string]struct{}),
		patterns:      make(map[string]struct{}),
		shardChannels: make(map[string]struct{}),
		send:          send,
	}
}

func (h *pubSubHub) removeSessionLocked(session *pubSubSession) {
	if session.closed {
		return
	}
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
	session.closed = true
}

func (session *pubSubSession) close() {
	h := session.hub
	h.mu.Lock()
	h.removeSessionLocked(session)
	h.mu.Unlock()
}

func (session *pubSubSession) subscriptionCountLocked() int {
	return len(session.channels) + len(session.patterns)
}

func (session *pubSubSession) shardSubscriptionCountLocked() int {
	return len(session.shardChannels)
}

func (session *pubSubSession) active() bool {
	h := session.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	return !session.closed && (session.subscriptionCountLocked() > 0 || session.shardSubscriptionCountLocked() > 0)
}

func (h *pubSubHub) publish(channel, message []byte) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	var receivers int64
	failed := make(map[*pubSubSession]struct{})
	messageFrame := array(
		formatBulkString([]byte("message")),
		formatBulkString(channel),
		formatBulkString(message),
	)
	for session := range h.channels[string(channel)] {
		receivers++
		if err := session.send(messageFrame); err != nil {
			failed[session] = struct{}{}
		}
	}

	patterns := make([]string, 0, len(h.patterns))
	for pattern := range h.patterns {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	for _, pattern := range patterns {
		if !engine.RedisGlobMatch([]byte(pattern), channel) {
			continue
		}
		frame := array(
			formatBulkString([]byte("pmessage")),
			formatBulkString([]byte(pattern)),
			formatBulkString(channel),
			formatBulkString(message),
		)
		for session := range h.patterns[pattern] {
			receivers++
			if err := session.send(frame); err != nil {
				failed[session] = struct{}{}
			}
		}
	}
	for session := range failed {
		h.removeSessionLocked(session)
	}
	return receivers
}

func (h *pubSubHub) publishShard(channel, message []byte) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	frame := array(
		formatBulkString([]byte("smessage")),
		formatBulkString(channel),
		formatBulkString(message),
	)
	var receivers int64
	failed := make(map[*pubSubSession]struct{})
	for session := range h.shardChannels[string(channel)] {
		receivers++
		if err := session.send(frame); err != nil {
			failed[session] = struct{}{}
		}
	}
	for session := range failed {
		h.removeSessionLocked(session)
	}
	return receivers
}

func channelNamesMatching(registry map[string]map[*pubSubSession]struct{}, pattern []byte, hasPattern bool) [][]byte {
	names := make([]string, 0, len(registry))
	for channel, subscribers := range registry {
		if len(subscribers) == 0 {
			continue
		}
		if hasPattern && !engine.RedisGlobMatch(pattern, []byte(channel)) {
			continue
		}
		names = append(names, channel)
	}
	sort.Strings(names)
	out := make([][]byte, 0, len(names))
	for _, name := range names {
		out = append(out, []byte(name))
	}
	return out
}

func (h *pubSubHub) channelsMatching(pattern []byte, hasPattern bool) [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return channelNamesMatching(h.channels, pattern, hasPattern)
}

func (h *pubSubHub) shardChannelsMatching(pattern []byte, hasPattern bool) [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return channelNamesMatching(h.shardChannels, pattern, hasPattern)
}

func (h *pubSubHub) subscriberCount(channel []byte) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return int64(len(h.channels[string(channel)]))
}

func (h *pubSubHub) shardSubscriberCount(channel []byte) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return int64(len(h.shardChannels[string(channel)]))
}

func (h *pubSubHub) patternCount() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return int64(len(h.patterns))
}
