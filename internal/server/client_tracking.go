package server

import (
	"errors"
	"strings"
)

type clientTrackingMode uint8

const (
	clientTrackingDefault clientTrackingMode = iota
	clientTrackingOptIn
	clientTrackingOptOut
)

type clientTrackingState struct {
	enabled  bool
	bcast    bool
	noLoop   bool
	mode     clientTrackingMode
	prefixes []string
	keys     map[string]struct{}

	// cacheOverride is consumed by the next ordinary command:
	//  1 => track it (OPTIN + CACHING YES)
	// -1 => do not track it (OPTOUT + CACHING NO)
	cacheOverride int
}

func (c *clientSession) trackingSnapshot() clientTrackingState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	state := c.tracking
	state.prefixes = append([]string(nil), c.tracking.prefixes...)
	state.keys = make(map[string]struct{}, len(c.tracking.keys))
	for key := range c.tracking.keys {
		state.keys[key] = struct{}{}
	}
	return state
}

func (c *clientSession) configureTracking(
	enabled bool,
	bcast bool,
	noLoop bool,
	mode clientTrackingMode,
	prefixes []string,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !enabled {
		c.tracking = clientTrackingState{}
		return
	}

	c.tracking.enabled = true
	c.tracking.bcast = bcast
	c.tracking.noLoop = noLoop
	c.tracking.mode = mode
	c.tracking.prefixes = append([]string(nil), prefixes...)
	c.tracking.keys = make(map[string]struct{})
	c.tracking.cacheOverride = 0
}

func (c *clientSession) setTrackingCacheOverride(value int) {
	c.mu.Lock()
	c.tracking.cacheOverride = value
	c.mu.Unlock()
}

func (c *clientSession) consumeTrackingDecision() (clientTrackingState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.tracking
	state.prefixes = append([]string(nil), c.tracking.prefixes...)
	state.keys = nil

	if !c.tracking.enabled || c.tracking.bcast {
		c.tracking.cacheOverride = 0
		return state, false
	}

	track := false
	switch c.tracking.mode {
	case clientTrackingDefault:
		track = true
	case clientTrackingOptIn:
		track = c.tracking.cacheOverride == 1
	case clientTrackingOptOut:
		track = c.tracking.cacheOverride != -1
	}

	c.tracking.cacheOverride = 0
	return state, track
}

func (c *clientSession) rememberTrackedKeys(keys []string) {
	if len(keys) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.tracking.enabled || c.tracking.bcast {
		return
	}
	if c.tracking.keys == nil {
		c.tracking.keys = make(map[string]struct{})
	}
	for _, key := range keys {
		c.tracking.keys[key] = struct{}{}
	}
}

func (c *clientSession) consumeInvalidation(key string, writerID uint64) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.tracking.enabled {
		return false, false
	}

	if c.tracking.bcast {
		match := len(c.tracking.prefixes) == 0
		for _, prefix := range c.tracking.prefixes {
			if strings.HasPrefix(key, prefix) {
				match = true
				break
			}
		}
		if !match {
			return false, false
		}
		if c.tracking.noLoop && c.id == writerID {
			return false, false
		}
		return true, true
	}

	if c.tracking.keys == nil {
		return false, false
	}
	if _, ok := c.tracking.keys[key]; !ok {
		return false, false
	}

	// Redis invalidates/removes the tracked key even when NOLOOP suppresses
	// delivery for the writer itself.
	delete(c.tracking.keys, key)

	if c.tracking.noLoop && c.id == writerID {
		return false, true
	}
	return true, true
}

func (s *TCPServer) executeClientTracking(
	session *clientSession,
	args [][]byte,
) (bool, []byte, error) {
	if len(args) < 2 || !strings.EqualFold(string(args[0]), "CLIENT") {
		return false, nil, nil
	}

	switch strings.ToUpper(string(args[1])) {
	case "GETREDIR":
		if len(args) != 2 {
			return true, nil, errors.New(
				"ERR wrong number of arguments for 'client|getredir' command",
			)
		}
		return true, integer(-1), nil

	case "CACHING":
		if len(args) != 3 {
			return true, nil, errors.New(
				"ERR wrong number of arguments for 'client|caching' command",
			)
		}

		state := session.trackingSnapshot()
		if !state.enabled ||
			(state.mode != clientTrackingOptIn &&
				state.mode != clientTrackingOptOut) {
			return true, nil, errors.New(
				"ERR CLIENT CACHING can be called only when the client is in tracking mode with OPTIN or OPTOUT mode enabled",
			)
		}

		option := strings.ToUpper(string(args[2]))
		switch state.mode {
		case clientTrackingOptIn:
			if option != "YES" {
				return true, nil, errors.New(
					"ERR CLIENT CACHING YES is only valid when tracking is enabled in OPTIN mode",
				)
			}
			session.setTrackingCacheOverride(1)
		case clientTrackingOptOut:
			if option != "NO" {
				return true, nil, errors.New(
					"ERR CLIENT CACHING NO is only valid when tracking is enabled in OPTOUT mode",
				)
			}
			session.setTrackingCacheOverride(-1)
		}

		return true, []byte("+OK\r\n"), nil

	case "TRACKING":
		if len(args) < 3 {
			return true, nil, errors.New(
				"ERR wrong number of arguments for 'client|tracking' command",
			)
		}

		switch strings.ToUpper(string(args[2])) {
		case "OFF":
			if len(args) != 3 {
				return true, nil, errors.New("ERR syntax error")
			}
			session.configureTracking(false, false, false, clientTrackingDefault, nil)
			return true, []byte("+OK\r\n"), nil

		case "ON":
		default:
			return true, nil, errors.New("ERR syntax error")
		}

		var (
			bcast    bool
			noLoop   bool
			optIn    bool
			optOut   bool
			prefixes []string
		)

		for i := 3; i < len(args); i++ {
			option := strings.ToUpper(string(args[i]))
			switch option {
			case "BCAST":
				bcast = true
			case "NOLOOP":
				noLoop = true
			case "OPTIN":
				optIn = true
			case "OPTOUT":
				optOut = true
			case "PREFIX":
				if i+1 >= len(args) {
					return true, nil, errors.New("ERR syntax error")
				}
				i++
				prefixes = append(prefixes, string(args[i]))
			case "REDIRECT":
				return true, nil, errors.New(
					"ERR REDIRECT is not supported yet",
				)
			default:
				return true, nil, errors.New("ERR syntax error")
			}
		}

		if optIn && optOut {
			return true, nil, errors.New(
				"ERR You can't specify both OPTIN mode and OPTOUT mode",
			)
		}
		if bcast && (optIn || optOut) {
			return true, nil, errors.New(
				"ERR OPTIN and OPTOUT are not compatible with BCAST",
			)
		}
		if len(prefixes) > 0 && !bcast {
			return true, nil, errors.New(
				"ERR PREFIX option requires BCAST mode to be enabled",
			)
		}

		mode := clientTrackingDefault
		if optIn {
			mode = clientTrackingOptIn
		} else if optOut {
			mode = clientTrackingOptOut
		}

		session.configureTracking(true, bcast, noLoop, mode, prefixes)
		return true, []byte("+OK\r\n"), nil
	}

	return false, nil, nil
}

func trackingReadKeys(args [][]byte) []string {
	refs, err := commandKeys(args)
	if err != nil {
		return nil
	}

	keys := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		read := false
		for _, flag := range ref.flags {
			if flag == "RO" || flag == "access" {
				read = true
				break
			}
		}
		if !read {
			continue
		}
		key := string(ref.value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func trackingWriteKeys(args [][]byte) []string {
	refs, err := commandKeys(args)
	if err != nil {
		return nil
	}

	keys := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		write := false
		for _, flag := range ref.flags {
			switch flag {
			case "OW", "RW", "RM", "update", "insert", "delete":
				write = true
			}
		}
		if !write {
			continue
		}
		key := string(ref.value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func trackingInvalidationPush(keys []string) []byte {
	items := make([][]byte, 0, len(keys))
	for _, key := range keys {
		items = append(items, formatBulkString([]byte(key)))
	}

	out := []byte(">2\r\n$10\r\ninvalidate\r\n")
	out = append(out, array(items...)...)
	return out
}

func (s *TCPServer) trackCommandRead(
	session *clientSession,
	args [][]byte,
) {
	_, track := session.consumeTrackingDecision()
	if !track {
		return
	}

	keys := trackingReadKeys(args)
	session.rememberTrackedKeys(keys)
}

func (s *TCPServer) invalidateTrackingKeys(
	writer *clientSession,
	args [][]byte,
) {
	keys := trackingWriteKeys(args)
	if len(keys) == 0 {
		return
	}

	s.mu.Lock()
	clients := make([]*clientSession, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()

	for _, client := range clients {
		toSend := make([]string, 0, len(keys))
		for _, key := range keys {
			deliver, _ := client.consumeInvalidation(key, writer.id)
			if deliver {
				toSend = append(toSend, key)
			}
		}
		if len(toSend) == 0 {
			continue
		}

		client.mu.RLock()
		push := client.trackingPush
		protocol := client.protocol
		client.mu.RUnlock()

		if protocol != 3 || push == nil {
			continue
		}
		_ = push(trackingInvalidationPush(toSend))
	}
}
