package server

import (
	"errors"
	"math"
	"snugkv/internal/engine"
	"strconv"
	"strings"
	"time"
)

type xreadRequest struct {
	keys     []string
	cursors  []engine.StreamReadCursor
	count    int
	block    time.Duration
	hasBlock bool
}

func init() {
	info := commandInfo{4, 0, 0, 0, 0, false}
	streamCommands["XREAD"] = info
	commandTable["XREAD"] = info
}

func parseXRead(args [][]byte) (xreadRequest, error) {
	request := xreadRequest{count: int(^uint(0) >> 1)}
	if len(args) < 4 || !strings.EqualFold(string(args[0]), "XREAD") {
		return request, errors.New("ERR wrong number of arguments for 'xread' command")
	}
	seenCount := false
	i := 1
	for i < len(args) && !strings.EqualFold(string(args[i]), "STREAMS") {
		switch strings.ToUpper(string(args[i])) {
		case "COUNT":
			if seenCount || i+1 >= len(args) {
				return request, errors.New("ERR syntax error")
			}
			n, err := strconv.ParseInt(string(args[i+1]), 10, 64)
			if err != nil || n <= 0 || int64(int(n)) != n {
				return request, errors.New("ERR value is not an integer or out of range")
			}
			request.count = int(n)
			seenCount = true
			i += 2
		case "BLOCK":
			if request.hasBlock || i+1 >= len(args) {
				return request, errors.New("ERR syntax error")
			}
			ms, err := strconv.ParseInt(string(args[i+1]), 10, 64)
			if err != nil || ms < 0 || ms > math.MaxInt64/int64(time.Millisecond) {
				return request, errors.New("ERR timeout is not an integer or out of range")
			}
			request.hasBlock = true
			if ms > 0 {
				request.block = time.Duration(ms) * time.Millisecond
			}
			i += 2
		default:
			return request, errors.New("ERR syntax error")
		}
	}
	if i >= len(args) || !strings.EqualFold(string(args[i]), "STREAMS") {
		return request, errors.New("ERR syntax error")
	}
	i++
	remaining := len(args) - i
	if remaining < 2 || remaining%2 != 0 {
		return request, errors.New("ERR Unbalanced XREAD list of streams: for each stream key an ID or '$' must be specified")
	}
	streamCount := remaining / 2
	request.keys = make([]string, streamCount)
	request.cursors = make([]engine.StreamReadCursor, streamCount)
	for n := 0; n < streamCount; n++ {
		request.keys[n] = string(args[i+n])
	}
	for n := 0; n < streamCount; n++ {
		cursor, err := engine.ParseStreamReadCursor(string(args[i+streamCount+n]))
		if err != nil {
			return request, err
		}
		request.cursors[n] = cursor
	}
	return request, nil
}

func streamReadResponse(results []engine.StreamReadResult) []byte {
	if len(results) == 0 {
		return []byte("*-1\r\n")
	}
	streams := make([][]byte, 0, len(results))
	for _, result := range results {
		streams = append(streams, array(
			formatBulkString([]byte(result.Key)),
			streamEntriesResponse(result.Entries),
		))
	}
	return array(streams...)
}

func (s *Server) executeXRead(args [][]byte) ([]byte, error) {
	request, err := parseXRead(args)
	if err != nil {
		return nil, err
	}
	resolved, err := s.store.ResolveStreamReadCursors(request.keys, request.cursors)
	if err != nil {
		return nil, err
	}
	results, err := s.store.StreamReadAfter(request.keys, resolved, request.count)
	if err != nil {
		return nil, err
	}
	return streamReadResponse(results), nil
}

func isXReadCommand(args [][]byte) bool {
	return len(args) > 0 && strings.EqualFold(string(args[0]), "XREAD")
}

func isBlockingStreamCommand(args [][]byte) bool {
	if isXReadGroupCommand(args) {
		request, err := parseXReadGroup(args)
		return err == nil && xreadGroupCanBlock(request)
	}
	if !isXReadCommand(args) {
		return false
	}
	for i := 1; i < len(args); i++ {
		if strings.EqualFold(string(args[i]), "STREAMS") {
			return false
		}
		if strings.EqualFold(string(args[i]), "BLOCK") {
			return true
		}
	}
	return false
}
