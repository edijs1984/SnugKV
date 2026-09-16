package server

import (
	"errors"
	"snugkv/internal/engine"
	"strconv"
	"strings"
	"time"
)

var streamClaimCommands = map[string]commandInfo{
	"XCLAIM":     {6, 0, 1, 1, 1, true},
	"XAUTOCLAIM": {6, 0, 1, 1, 1, true},
}

func init() {
	for name, info := range streamClaimCommands {
		commandTable[name] = info
	}
}

func isStreamClaimCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := streamClaimCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func parseNonNegativeMillis(arg []byte) (int64, error) {
	value, err := strconv.ParseInt(string(arg), 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
	return value, nil
}

func parseClaimStreamID(text string) (engine.StreamID, error) {
	if strings.Contains(text, "-") {
		return engine.ParseStreamID(text)
	}
	ms, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return engine.StreamID{}, errors.New("ERR Invalid stream ID specified as stream command argument")
	}
	return engine.StreamID{Millis: ms}, nil
}

func streamIDsResponse(ids []engine.StreamID) []byte {
	parts := make([][]byte, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, formatBulkString([]byte(id.String())))
	}
	return array(parts...)
}

func parseXClaim(args [][]byte) (string, string, string, time.Duration, []engine.StreamID, engine.StreamClaimOptions, error) {
	var options engine.StreamClaimOptions
	if len(args) < 6 {
		return "", "", "", 0, nil, options, errors.New("ERR wrong number of arguments for 'xclaim' command")
	}
	key, group, consumer := string(args[1]), string(args[2]), string(args[3])
	minIdleMS, err := parseNonNegativeMillis(args[4])
	if err != nil {
		return "", "", "", 0, nil, options, err
	}

	i := 5
	ids := make([]engine.StreamID, 0)
	for i < len(args) {
		token := strings.ToUpper(string(args[i]))
		if token == "IDLE" || token == "TIME" || token == "RETRYCOUNT" || token == "FORCE" || token == "JUSTID" || token == "LASTID" {
			break
		}
		id, err := engine.ParseStreamID(string(args[i]))
		if err != nil {
			return "", "", "", 0, nil, options, err
		}
		ids = append(ids, id)
		i++
	}
	if len(ids) == 0 {
		return "", "", "", 0, nil, options, errors.New("ERR wrong number of arguments for 'xclaim' command")
	}

	for i < len(args) {
		switch strings.ToUpper(string(args[i])) {
		case "IDLE":
			if options.IdleMillis != nil || options.TimeMillis != nil || i+1 >= len(args) {
				return "", "", "", 0, nil, options, errors.New("ERR syntax error")
			}
			value, err := parseNonNegativeMillis(args[i+1])
			if err != nil {
				return "", "", "", 0, nil, options, err
			}
			options.IdleMillis = &value
			i += 2
		case "TIME":
			if options.TimeMillis != nil || options.IdleMillis != nil || i+1 >= len(args) {
				return "", "", "", 0, nil, options, errors.New("ERR syntax error")
			}
			value, err := parseNonNegativeMillis(args[i+1])
			if err != nil {
				return "", "", "", 0, nil, options, err
			}
			options.TimeMillis = &value
			i += 2
		case "RETRYCOUNT":
			if options.RetryCount != nil || i+1 >= len(args) {
				return "", "", "", 0, nil, options, errors.New("ERR syntax error")
			}
			value, err := strconv.ParseUint(string(args[i+1]), 10, 64)
			if err != nil {
				return "", "", "", 0, nil, options, errors.New("ERR value is not an integer or out of range")
			}
			options.RetryCount = &value
			i += 2
		case "FORCE":
			if options.Force {
				return "", "", "", 0, nil, options, errors.New("ERR syntax error")
			}
			options.Force = true
			i++
		case "JUSTID":
			if options.JustID {
				return "", "", "", 0, nil, options, errors.New("ERR syntax error")
			}
			options.JustID = true
			i++
		case "LASTID":
			if options.LastID != nil || i+1 >= len(args) {
				return "", "", "", 0, nil, options, errors.New("ERR syntax error")
			}
			id, err := parseClaimStreamID(string(args[i+1]))
			if err != nil {
				return "", "", "", 0, nil, options, err
			}
			options.LastID = &id
			i += 2
		default:
			return "", "", "", 0, nil, options, errors.New("ERR syntax error")
		}
	}
	return key, group, consumer, time.Duration(minIdleMS) * time.Millisecond, ids, options, nil
}

func (s *Server) executeXClaim(args [][]byte) ([]byte, error) {
	key, group, consumer, minIdle, ids, options, err := parseXClaim(args)
	if err != nil {
		return nil, err
	}
	entries, claimedIDs, err := s.store.StreamGroupClaim(key, group, consumer, minIdle, ids, options)
	if err != nil {
		return nil, err
	}
	if options.JustID {
		return streamIDsResponse(claimedIDs), nil
	}
	return streamEntriesResponse(entries), nil
}

func parseXAutoClaim(args [][]byte) (string, string, string, time.Duration, engine.StreamID, int, bool, error) {
	if len(args) < 6 {
		return "", "", "", 0, engine.StreamID{}, 0, false, errors.New("ERR wrong number of arguments for 'xautoclaim' command")
	}
	key, group, consumer := string(args[1]), string(args[2]), string(args[3])
	minIdleMS, err := parseNonNegativeMillis(args[4])
	if err != nil {
		return "", "", "", 0, engine.StreamID{}, 0, false, err
	}
	start, err := parseClaimStreamID(string(args[5]))
	if err != nil {
		return "", "", "", 0, engine.StreamID{}, 0, false, err
	}
	count := 100
	justID := false
	for i := 6; i < len(args); {
		switch strings.ToUpper(string(args[i])) {
		case "COUNT":
			if i+1 >= len(args) {
				return "", "", "", 0, engine.StreamID{}, 0, false, errors.New("ERR syntax error")
			}
			value, err := strconv.ParseInt(string(args[i+1]), 10, 64)
			if err != nil || value <= 0 || int64(int(value)) != value {
				return "", "", "", 0, engine.StreamID{}, 0, false, errors.New("ERR value is not an integer or out of range")
			}
			count = int(value)
			i += 2
		case "JUSTID":
			if justID {
				return "", "", "", 0, engine.StreamID{}, 0, false, errors.New("ERR syntax error")
			}
			justID = true
			i++
		default:
			return "", "", "", 0, engine.StreamID{}, 0, false, errors.New("ERR syntax error")
		}
	}
	return key, group, consumer, time.Duration(minIdleMS) * time.Millisecond, start, count, justID, nil
}

func (s *Server) executeXAutoClaim(args [][]byte) ([]byte, error) {
	key, group, consumer, minIdle, start, count, justID, err := parseXAutoClaim(args)
	if err != nil {
		return nil, err
	}
	result, err := s.store.StreamGroupAutoClaim(key, group, consumer, minIdle, start, count, justID)
	if err != nil {
		return nil, err
	}
	claimed := streamEntriesResponse(result.Entries)
	if justID {
		claimed = streamIDsResponse(result.IDs)
	}
	return array(
		formatBulkString([]byte(result.Next.String())),
		claimed,
		streamIDsResponse(result.DeletedIDs),
	), nil
}

func (s *Server) executeStreamClaim(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	switch strings.ToUpper(string(args[0])) {
	case "XCLAIM":
		return s.executeXClaim(args)
	case "XAUTOCLAIM":
		return s.executeXAutoClaim(args)
	default:
		return nil, errors.New("ERR unsupported stream claim command")
	}
}
