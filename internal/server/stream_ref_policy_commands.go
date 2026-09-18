package server

import (
	"errors"
	"fmt"
	"snugkv/internal/engine"
	"strconv"
	"strings"
)

func init() {
	commandTable["XDELEX"] = commandInfo{5, 0, 1, 1, 1, true}
	commandTable["XACKDEL"] = commandInfo{6, 0, 1, 1, 1, true}
}

// XADD and XTRIM are routed here as well so KEEPREF/DELREF/ACKED behavior is
// centralized rather than split across the legacy/default command path.
func isStreamRefPolicyCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.ToUpper(string(args[0])) {
	case "XADD", "XTRIM", "XDELEX", "XACKDEL":
		return true
	default:
		return false
	}
}

func parseStreamRefPolicy(arg []byte) (engine.StreamRefPolicy, bool) {
	switch strings.ToUpper(string(arg)) {
	case "KEEPREF":
		return engine.StreamRefKeep, true
	case "DELREF":
		return engine.StreamRefDelete, true
	case "ACKED":
		return engine.StreamRefAcked, true
	default:
		return engine.StreamRefKeep, false
	}
}

func parseXAddWithRefPolicy(args [][]byte) (string, []engine.StreamField, engine.StreamAddOptions, engine.StreamRefPolicy, error) {
	options := engine.StreamAddOptions{}
	policy := engine.StreamRefKeep
	seenPolicy := false
	i := 2
	for i < len(args) {
		if parsed, ok := parseStreamRefPolicy(args[i]); ok {
			if seenPolicy {
				return "", nil, options, policy, errors.New("ERR syntax error")
			}
			policy = parsed
			seenPolicy = true
			i++
			continue
		}
		switch strings.ToUpper(string(args[i])) {
		case "NOMKSTREAM":
			if options.NoMkStream {
				return "", nil, options, policy, errors.New("ERR syntax error")
			}
			options.NoMkStream = true
			i++
		case "MAXLEN", "MINID":
			strategy := strings.ToUpper(string(args[i]))
			if options.HasMaxLen || options.HasMinID {
				return "", nil, options, policy, errors.New("ERR syntax error")
			}
			i++
			if i < len(args) && (string(args[i]) == "~" || string(args[i]) == "=") {
				i++
			}
			if i >= len(args) {
				return "", nil, options, policy, errors.New("ERR syntax error")
			}
			if strategy == "MAXLEN" {
				maxLen, err := parseNonNegativeInt(args[i])
				if err != nil {
					return "", nil, options, policy, err
				}
				options.HasMaxLen = true
				options.MaxLen = maxLen
			} else {
				minID, err := parseStreamMinID(args[i])
				if err != nil {
					return "", nil, options, policy, err
				}
				options.HasMinID = true
				options.MinID = minID
			}
			i++
			if i < len(args) && strings.EqualFold(string(args[i]), "LIMIT") {
				if i+1 >= len(args) {
					return "", nil, options, policy, errors.New("ERR syntax error")
				}
				limit, err := parseNonNegativeInt(args[i+1])
				if err != nil {
					return "", nil, options, policy, err
				}
				options.Limit = limit
				i += 2
			}
		default:
			goto id
		}
	}

id:
	if i >= len(args) {
		return "", nil, options, policy, errors.New("ERR syntax error")
	}
	id := string(args[i])
	i++
	if i >= len(args) || (len(args)-i)%2 != 0 {
		return "", nil, options, policy, errors.New("ERR wrong number of arguments for 'xadd' command")
	}
	fields := make([]engine.StreamField, 0, (len(args)-i)/2)
	for ; i < len(args); i += 2 {
		fields = append(fields, engine.StreamField{
			Field: append([]byte(nil), args[i]...),
			Value: append([]byte(nil), args[i+1]...),
		})
	}
	return id, fields, options, policy, nil
}

func parseXTrimWithRefPolicy(args [][]byte) (string, int, engine.StreamID, int, engine.StreamRefPolicy, error) {
	if len(args) < 4 {
		return "", 0, engine.StreamID{}, 0, engine.StreamRefKeep, errors.New("ERR wrong number of arguments for 'xtrim' command")
	}
	strategy := strings.ToUpper(string(args[2]))
	if strategy != "MAXLEN" && strategy != "MINID" {
		return "", 0, engine.StreamID{}, 0, engine.StreamRefKeep, errors.New("ERR syntax error")
	}
	i := 3
	approximate := false
	if i < len(args) && (string(args[i]) == "~" || string(args[i]) == "=") {
		approximate = string(args[i]) == "~"
		i++
	}
	if i >= len(args) {
		return "", 0, engine.StreamID{}, 0, engine.StreamRefKeep, errors.New("ERR syntax error")
	}
	maxLen := 0
	minID := engine.StreamID{}
	var err error
	if strategy == "MAXLEN" {
		rawMaxLen := string(args[i])

		if n, parseErr := strconv.ParseInt(rawMaxLen, 10, 64); parseErr == nil && n < 0 {
			return "", 0, engine.StreamID{}, 0, engine.StreamRefKeep,
				errors.New("ERR The MAXLEN argument must be >= 0.")
		}

		maxLen, err = parseNonNegativeInt(args[i])
	} else {
		minID, err = parseStreamMinID(args[i])
	}
	if err != nil {
		return "", 0, engine.StreamID{}, 0, engine.StreamRefKeep, err
	}
	i++
	limit := 0
	policy := engine.StreamRefKeep
	seenLimit := false
	seenPolicy := false
	for i < len(args) {
		if parsed, ok := parseStreamRefPolicy(args[i]); ok {
			if seenPolicy {
				return "", 0, engine.StreamID{}, 0, policy, errors.New("ERR syntax error")
			}
			policy = parsed
			seenPolicy = true
			i++
			continue
		}
		if strings.EqualFold(string(args[i]), "LIMIT") {
			if !approximate {
				return "", 0, engine.StreamID{}, 0, policy, errors.New("ERR syntax error, LIMIT cannot be used without the special ~ option")
			}
			if seenLimit || i+1 >= len(args) {
				return "", 0, engine.StreamID{}, 0, policy, errors.New("ERR syntax error")
			}
			limit, err = parseNonNegativeInt(args[i+1])
			if err != nil {
				return "", 0, engine.StreamID{}, 0, policy, err
			}
			seenLimit = true
			i += 2
			continue
		}
		return "", 0, engine.StreamID{}, 0, policy, errors.New("ERR syntax error")
	}
	return strategy, maxLen, minID, limit, policy, nil
}

func parseStreamIDSBlock(args [][]byte, start int) ([]engine.StreamID, engine.StreamRefPolicy, error) {
	policy := engine.StreamRefKeep
	seenPolicy := false
	seenIDS := false
	ids := []engine.StreamID{}
	for i := start; i < len(args); {
		if parsed, ok := parseStreamRefPolicy(args[i]); ok {
			if seenPolicy {
				return nil, policy, errors.New("ERR syntax error")
			}
			policy = parsed
			seenPolicy = true
			i++
			continue
		}
		if !strings.EqualFold(string(args[i]), "IDS") || seenIDS || i+1 >= len(args) {
			return nil, policy, errors.New("ERR syntax error")
		}
		count64, err := strconv.ParseInt(string(args[i+1]), 10, 64)
		if err != nil || count64 <= 0 || int64(int(count64)) != count64 {
			return nil, policy, errors.New("ERR value is not an integer or out of range")
		}
		count := int(count64)
		i += 2
		if count > len(args)-i {
			return nil, policy, errors.New("ERR syntax error")
		}
		ids = make([]engine.StreamID, 0, count)
		for n := 0; n < count; n++ {
			id, err := engine.ParseStreamID(string(args[i+n]))
			if err != nil {
				return nil, policy, err
			}
			ids = append(ids, id)
		}
		i += count
		seenIDS = true
	}
	if !seenIDS || len(ids) == 0 {
		return nil, policy, errors.New("ERR syntax error")
	}
	return ids, policy, nil
}

func streamDeletionStatusesResponse(statuses []int64) []byte {
	items := make([][]byte, len(statuses))
	for i, status := range statuses {
		items[i] = integer(status)
	}
	return array(items...)
}

func (s *Server) executeStreamRefPolicy(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := commandTable[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	switch cmd {
	case "XADD":
		idSpec, fields, options, policy, err := parseXAddWithRefPolicy(args)
		if err != nil {
			return nil, err
		}
		id, applied, err := s.store.StreamAddWithPolicy(string(args[1]), idSpec, fields, options, policy)
		if err != nil {
			return nil, err
		}
		if !applied {
			return nullBulk(), nil
		}
		return formatBulkString([]byte(id.String())), nil

	case "XTRIM":
		strategy, maxLen, minID, limit, policy, err := parseXTrimWithRefPolicy(args)
		if err != nil {
			return nil, err
		}
		var trimmed int64
		if strategy == "MAXLEN" {
			trimmed, err = s.store.StreamTrimMaxLenWithPolicy(string(args[1]), maxLen, limit, policy)
		} else {
			trimmed, err = s.store.StreamTrimMinIDWithPolicy(string(args[1]), minID, limit, policy)
		}
		if err != nil {
			return nil, err
		}
		return integer(trimmed), nil

	case "XDELEX":
		ids, policy, err := parseStreamIDSBlock(args, 2)
		if err != nil {
			return nil, err
		}
		statuses, err := s.store.StreamDeleteEx(string(args[1]), ids, policy)
		if err != nil {
			return nil, err
		}
		return streamDeletionStatusesResponse(statuses), nil

	case "XACKDEL":
		ids, policy, err := parseStreamIDSBlock(args, 3)
		if err != nil {
			return nil, err
		}
		statuses, err := s.store.StreamAckDelete(string(args[1]), string(args[2]), ids, policy)
		if err != nil {
			return nil, err
		}
		return streamDeletionStatusesResponse(statuses), nil
	}
	return nil, errors.New("ERR unsupported stream reference command")
}
