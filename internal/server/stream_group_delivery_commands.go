package server

import (
	"errors"
	"math"
	"snugkv/internal/engine"
	"strconv"
	"strings"
	"time"
)

type xreadGroupRequest struct {
	group    string
	consumer string
	keys     []string
	cursors  []engine.StreamGroupReadCursor
	count    int
	block    time.Duration
	hasBlock bool
	noAck    bool
}

func init() {
	commandTable["XREADGROUP"] = commandInfo{7, 0, 0, 0, 0, true}
	commandTable["XACK"] = commandInfo{4, 0, 1, 1, 1, true}
	commandTable["XPENDING"] = commandInfo{3, 0, 1, 1, 1, false}
}

func isXReadGroupCommand(args [][]byte) bool {
	return len(args) > 0 && strings.EqualFold(string(args[0]), "XREADGROUP")
}

func isStreamGroupDeliveryCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.ToUpper(string(args[0])) {
	case "XREADGROUP", "XACK", "XPENDING":
		return true
	default:
		return false
	}
}

func parseGroupReadCursor(text string) (engine.StreamGroupReadCursor, error) {
	if text == ">" {
		return engine.StreamGroupReadCursor{New: true}, nil
	}
	cursor, err := engine.ParseStreamReadCursor(text)
	if err != nil || cursor.Latest {
		if err != nil {
			return engine.StreamGroupReadCursor{}, err
		}
		return engine.StreamGroupReadCursor{}, errors.New("ERR Invalid stream ID specified as stream command argument")
	}
	return engine.StreamGroupReadCursor{ID: cursor.ID}, nil
}

func parseXReadGroup(args [][]byte) (xreadGroupRequest, error) {
	request := xreadGroupRequest{count: int(^uint(0) >> 1)}
	if len(args) < 7 || !strings.EqualFold(string(args[0]), "XREADGROUP") || !strings.EqualFold(string(args[1]), "GROUP") {
		return request, errors.New("ERR wrong number of arguments for 'xreadgroup' command")
	}
	request.group = string(args[2])
	request.consumer = string(args[3])
	if request.group == "" || request.consumer == "" {
		return request, errors.New("ERR consumer group name and consumer name cannot be empty")
	}
	seenCount := false
	i := 4
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
		case "NOACK":
			if request.noAck {
				return request, errors.New("ERR syntax error")
			}
			request.noAck = true
			i++
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
		return request, errors.New("ERR Unbalanced XREADGROUP list of streams: for each stream key an ID must be specified")
	}
	streamCount := remaining / 2
	request.keys = make([]string, streamCount)
	request.cursors = make([]engine.StreamGroupReadCursor, streamCount)
	for n := 0; n < streamCount; n++ {
		request.keys[n] = string(args[i+n])
	}
	for n := 0; n < streamCount; n++ {
		cursor, err := parseGroupReadCursor(string(args[i+streamCount+n]))
		if err != nil {
			return request, err
		}
		request.cursors[n] = cursor
	}
	return request, nil
}

func xreadGroupCanBlock(request xreadGroupRequest) bool {
	if !request.hasBlock {
		return false
	}
	for _, cursor := range request.cursors {
		if cursor.New {
			return true
		}
	}
	return false
}

func streamGroupEntriesResponse(entries []engine.StreamEntry) []byte {
	response := make([][]byte, 0, len(entries))
	for _, item := range entries {
		var fields []byte
		if item.Fields == nil {
			fields = []byte("*-1\r\n")
		} else {
			parts := make([][]byte, 0, len(item.Fields)*2)
			for _, pair := range item.Fields {
				parts = append(parts, formatBulkString(pair.Field), formatBulkString(pair.Value))
			}
			fields = array(parts...)
		}
		response = append(response, array(formatBulkString([]byte(item.ID.String())), fields))
	}
	return array(response...)
}

func streamGroupReadResponse(results []engine.StreamReadResult) []byte {
	if len(results) == 0 {
		return []byte("*-1\r\n")
	}
	streams := make([][]byte, 0, len(results))
	for _, result := range results {
		streams = append(streams, array(formatBulkString([]byte(result.Key)), streamGroupEntriesResponse(result.Entries)))
	}
	return array(streams...)
}

func (s *Server) executeXReadGroup(args [][]byte) ([]byte, error) {
	request, err := parseXReadGroup(args)
	if err != nil {
		return nil, err
	}
	results, err := s.store.StreamGroupRead(request.keys, request.group, request.consumer, request.cursors, request.count, request.noAck)
	if err != nil {
		return nil, err
	}
	return streamGroupReadResponse(results), nil
}

func parsePendingBound(text string, start bool) (engine.StreamRangeBound, error) {
	return engine.ParseStreamRangeBound(text, start)
}

func (s *Server) executeXPending(args [][]byte) ([]byte, error) {
	if len(args) == 3 {
		summary, err := s.store.StreamGroupPendingSummary(string(args[1]), string(args[2]))
		if err != nil {
			return nil, err
		}
		var minReply, maxReply []byte
		if summary.MinID == nil {
			minReply, maxReply = nullBulk(), nullBulk()
		} else {
			minReply = formatBulkString([]byte(summary.MinID.String()))
			maxReply = formatBulkString([]byte(summary.MaxID.String()))
		}
		consumers := make([][]byte, 0, len(summary.Consumers))
		for _, item := range summary.Consumers {
			consumers = append(consumers, array(formatBulkString([]byte(item.Name)), formatBulkString([]byte(strconv.FormatInt(item.Count, 10)))))
		}
		return array(integer(summary.Count), minReply, maxReply, array(consumers...)), nil
	}

	i := 3
	minIdle := time.Duration(0)
	if i < len(args) && strings.EqualFold(string(args[i]), "IDLE") {
		if i+1 >= len(args) {
			return nil, errors.New("ERR syntax error")
		}
		ms, err := strconv.ParseInt(string(args[i+1]), 10, 64)
		if err != nil || ms < 0 || ms > math.MaxInt64/int64(time.Millisecond) {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		minIdle = time.Duration(ms) * time.Millisecond
		i += 2
	}
	if len(args)-i < 3 || len(args)-i > 4 {
		return nil, errors.New("ERR syntax error")
	}
	start, err := parsePendingBound(string(args[i]), true)
	if err != nil {
		return nil, err
	}
	end, err := parsePendingBound(string(args[i+1]), false)
	if err != nil {
		return nil, err
	}
	count64, err := strconv.ParseInt(string(args[i+2]), 10, 64)
	if err != nil || count64 <= 0 || int64(int(count64)) != count64 {
		return nil, errors.New("ERR value is not an integer or out of range")
	}
	consumer := ""
	if len(args)-i == 4 {
		consumer = string(args[i+3])
	}
	items, err := s.store.StreamGroupPendingRange(string(args[1]), string(args[2]), start, end, int(count64), consumer, minIdle)
	if err != nil {
		return nil, err
	}
	rows := make([][]byte, 0, len(items))
	for _, item := range items {
		consumerReply := nullBulk()
		if item.Consumer != "" {
			consumerReply = formatBulkString([]byte(item.Consumer))
		}
		rows = append(rows, array(
			formatBulkString([]byte(item.ID.String())),
			consumerReply,
			integer(item.IdleMillis),
			integer(int64(item.Deliveries)),
		))
	}
	return array(rows...), nil
}

func (s *Server) executeStreamGroupDelivery(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	switch strings.ToUpper(string(args[0])) {
	case "XREADGROUP":
		return s.executeXReadGroup(args)
	case "XACK":
		if len(args) < 4 {
			return nil, errors.New("ERR wrong number of arguments for 'xack' command")
		}
		ids := make([]engine.StreamID, 0, len(args)-3)
		for _, raw := range args[3:] {
			id, err := engine.ParseStreamID(string(raw))
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		acked, err := s.store.StreamGroupAck(string(args[1]), string(args[2]), ids)
		if err != nil {
			return nil, err
		}
		return integer(acked), nil
	case "XPENDING":
		if len(args) < 3 {
			return nil, errors.New("ERR wrong number of arguments for 'xpending' command")
		}
		return s.executeXPending(args)
	default:
		return nil, errors.New("ERR unsupported stream consumer-group command")
	}
}

func streamGroupReadKeys(args [][]byte) []string {
	request, err := parseXReadGroup(args)
	if err != nil {
		return nil
	}
	return request.keys
}
