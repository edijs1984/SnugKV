package server

import (
	"errors"
	"fmt"
	"math"
	"snugkv/internal/engine"
	"snugkv/internal/optimizer"
	"snugkv/internal/persistence"
	"snugkv/internal/stats"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Server struct {
	acl              *ACL
	aclLog           *ACLLog
	eviction         string
	metrics          *stats.Registry
	metricsEnabled   uint32
	optimizer        *optimizer.Optimizer
	store            *engine.Store
	commands         uint64
	journal          Journal
	durableMu        sync.RWMutex
	durabilityFailed bool
	watchSessions    atomic.Int32

	// executionACLUsername / executionACLArgs are valid only while durableMu is
	// held. TCP and transaction execution populate them so dynamic command
	// access (Lua/Functions and SORT BY/GET) can enforce the invoking user's
	// ACL rule set. Direct in-process Execute calls leave the context empty.
	executionACLUsername string
	executionACLArgs     [][]byte

	configAppendFsync    string
	configACLFile        string
	configAppendOnly     bool
	configGetMaxClients  func() int
	configSetMaxClients  func(int)
	configMu             sync.RWMutex
	configSetAppendFsync func(string) error
	configRewrite        func() error
}

func New(store *engine.Store) *Server {
	return &Server{
		store:   store,
		metrics: stats.New(),
		acl:     NewACL(),
		aclLog:  NewACLLog(),
	}
}

type commandInfo struct {
	min, max, first, last, step int
	write                       bool
}

var commandTable = map[string]commandInfo{
	"SNUG.AOFREWRITE": {1, 1, 0, 0, 0, false},
	"SNUG.COMPACT":    {1, 2, 0, 0, 0, false},
	"SNUG.ENCODING":   {2, 2, 1, 1, 1, false},
	"SNUG.CANDIDATES": {2, 2, 1, 1, 1, false},
	"SNUG.TYPE":       {2, 2, 1, 1, 1, false},
	"SNUG.MEMORY":     {2, 2, 1, 1, 1, false},
	"SNUG.SHAPES":     {1, 2, 0, 0, 0, false},
	"SNUG.STATS":      {1, 1, 0, 0, 0, false},
	"SNUG.POLICY":     {2, 2, 1, 1, 1, false},
	"AUTH":            {1, 3, 0, 0, 0, false},
	"ACL":             {1, 0, 0, 0, 0, false},
	"PING":            {1, 2, 0, 0, 0, false}, "ECHO": {2, 2, 0, 0, 0, false}, "QUIT": {1, 1, 0, 0, 0, false},
	"SELECT": {2, 2, 0, 0, 0, false}, "HELLO": {1, 0, 0, 0, 0, false}, "INFO": {1, 2, 0, 0, 0, false},
	"DBSIZE": {1, 1, 0, 0, 0, false}, "COMMAND": {1, 0, 0, 0, 0, false},
	"CONFIG":      {2, 0, 0, 0, 0, false},
	"CLIENT":      {2, 0, 0, 0, 0, false},
	"SCAN":        {2, 0, 0, 0, 0, false},
	"KEYS":        {2, 2, 0, 0, 0, false},
	"RANDOMKEY":   {1, 1, 0, 0, 0, false},
	"RENAME":      {3, 3, 1, 2, 1, true},
	"RENAMENX":    {3, 3, 1, 2, 1, true},
	"TOUCH":       {2, 0, 1, -1, 1, false},
	"EXPIREAT":    {3, 4, 1, 1, 1, true},
	"PEXPIREAT":   {3, 4, 1, 1, 1, true},
	"EXPIRETIME":  {2, 2, 1, 1, 1, false},
	"PEXPIRETIME": {2, 2, 1, 1, 1, false},
	"SET":         {3, 0, 1, 1, 1, true}, "GET": {2, 2, 1, 1, 1, false}, "MGET": {2, 0, 1, -1, 1, false},
	"DEL": {2, 0, 1, -1, 1, true}, "EXISTS": {2, 0, 1, -1, 1, false}, "GETSET": {3, 3, 1, 1, 1, true},
	"GETDEL": {2, 2, 1, 1, 1, true},
	"GETEX":  {2, 4, 1, 1, 1, true},
	"SETNX":  {3, 3, 1, 1, 1, true},
	"SETEX":  {4, 4, 1, 1, 1, true},
	"PSETEX": {4, 4, 1, 1, 1, true},
	"MSET":   {3, 0, 1, -1, 2, true},
	"MSETNX": {3, 0, 1, -1, 2, true},
	"INCR":   {2, 2, 1, 1, 1, true}, "DECR": {2, 2, 1, 1, 1, true}, "INCRBY": {3, 3, 1, 1, 1, true}, "DECRBY": {3, 3, 1, 1, 1, true},
	"INCRBYFLOAT": {3, 3, 1, 1, 1, true},
	"STRLEN":      {2, 2, 1, 1, 1, false}, "EXPIRE": {3, 4, 1, 1, 1, true}, "PEXPIRE": {3, 4, 1, 1, 1, true},
	"TTL": {2, 2, 1, 1, 1, false}, "PTTL": {2, 2, 1, 1, 1, false}, "PERSIST": {2, 2, 1, 1, 1, true},
	"TYPE":      {2, 2, 1, 1, 1, false},
	"FLUSHDB":   {1, 2, 0, 0, 0, true},
	"FLUSHALL":  {1, 2, 0, 0, 0, true},
	"UNLINK":    {2, 0, 1, -1, 1, true},
	"APPEND":    {3, 3, 1, 1, 1, true},
	"GETRANGE":  {4, 4, 1, 1, 1, false},
	"SETRANGE":  {4, 4, 1, 1, 1, true},
	"GETBIT":    {3, 3, 1, 1, 1, false},
	"SETBIT":    {4, 4, 1, 1, 1, true},
	"BITCOUNT":  {2, 5, 1, 1, 1, false},
	"BITPOS":    {3, 6, 1, 1, 1, false},
	"BITOP":     {4, 0, 2, -1, 1, true},
	"JSON.SET":  {4, 5, 1, 1, 1, true},
	"JSON.GET":  {2, 3, 1, 1, 1, false},
	"JSON.TYPE": {2, 3, 1, 1, 1, false},
	"JSON.DEL":       {2, 3, 1, 1, 1, true},
	"JSON.NUMINCRBY": {4, 4, 1, 1, 1, true},
	"JSON.STRLEN":    {2, 3, 1, 1, 1, false},
	"JSON.ARRLEN":    {2, 3, 1, 1, 1, false},
	"JSON.OBJLEN":    {2, 3, 1, 1, 1, false},
	"JSON.ARRAPPEND": {4, 0, 1, 1, 1, true},
	"JSON.STRAPPEND": {3, 4, 1, 1, 1, true},
	"JSON.OBJKEYS":   {2, 3, 1, 1, 1, false},
	"JSON.TOGGLE":    {3, 3, 1, 1, 1, true},
	"JSON.ARRPOP":    {2, 4, 1, 1, 1, true},
	"JSON.ARRINSERT": {5, 0, 1, 1, 1, true},
	"JSON.ARRINDEX":  {4, 6, 1, 1, 1, false},
	"JSON.CLEAR":     {2, 3, 1, 1, 1, true},
	"MEMORY":         {2, 5, 0, 0, 0, false},
}

func (s *Server) execute(args [][]byte) ([]byte, error) {
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
	key := ""
	if len(args) > 1 {
		key = string(args[1])
	}
	switch cmd {
	case "CONFIG":
		return s.executeConfig(args)

	case "MEMORY":
		subcommand := strings.ToUpper(string(args[1]))

		switch subcommand {
		case "USAGE":
			if len(args) != 3 && len(args) != 5 {
				return nil, errors.New("ERR syntax error")
			}

			if len(args) == 5 {
				if !strings.EqualFold(string(args[3]), "SAMPLES") {
					return nil, errors.New("ERR syntax error")
				}

				samples, err := strconv.ParseInt(string(args[4]), 10, 64)
				if err != nil || samples < 0 {
					return nil, errors.New("ERR syntax error")
				}

				// Accepted for Redis compatibility.
				// SnugKV currently stores values as one logical entry,
				// so sampling is not needed yet.
				_ = samples
			}

			usage, found := s.store.MemoryUsage(string(args[2]))
			if !found {
				return nullBulk(), nil
			}

			return integer(int64(usage)), nil

		default:
			return nil, errors.New("ERR unknown subcommand")
		}
	case "JSON.DEL":
		path := "$"

		if len(args) == 3 {
			path = string(args[2])
		}

		deleted, err := s.store.JSONDel(key, path)
		if err != nil {
			return nil, err
		}

		return integer(deleted), nil

	case "JSON.NUMINCRBY":
		increment, err := strconv.ParseFloat(string(args[3]), 64)
		if err != nil || math.IsNaN(increment) || math.IsInf(increment, 0) {
			return nil, errors.New("ERR value is not a valid number")
		}

		value, found, err := s.store.JSONNumIncrBy(key, string(args[2]), increment)
		if err != nil {
			return nil, err
		}
		return optionalBulk(value, found), nil

	case "JSON.STRLEN", "JSON.ARRLEN", "JSON.OBJLEN":
		path := "$"
		if len(args) == 3 {
			path = string(args[2])
		}

		var (
			length int64
			found  bool
			err    error
		)

		switch cmd {
		case "JSON.STRLEN":
			length, found, err = s.store.JSONStrLen(key, path)
		case "JSON.ARRLEN":
			length, found, err = s.store.JSONArrLen(key, path)
		default:
			length, found, err = s.store.JSONObjLen(key, path)
		}

		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		return integer(length), nil

	case "JSON.TYPE":
		path := "$"

		if len(args) == 3 {
			path = string(args[2])
		}

		jsonType, found, err := s.store.JSONType(key, path)
		if err != nil {
			return nil, err
		}

		if !found {
			return nullBulk(), nil
		}

		return formatBulkString([]byte(jsonType)), nil

	case "JSON.ARRAPPEND":
		length, found, err := s.store.JSONArrAppend(key, string(args[2]), args[3:])
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		return integer(length), nil

	case "JSON.STRAPPEND":
		path := "$"
		valueIndex := 2
		if len(args) == 4 {
			path = string(args[2])
			valueIndex = 3
		}

		length, found, err := s.store.JSONStrAppend(key, path, args[valueIndex])
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		return integer(length), nil

	case "JSON.OBJKEYS":
		path := "$"
		if len(args) == 3 {
			path = string(args[2])
		}

		keys, found, err := s.store.JSONObjKeys(key, path)
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}

		items := make([][]byte, 0, len(keys))
		for _, name := range keys {
			items = append(items, formatBulkString([]byte(name)))
		}
		return array(items...), nil

	case "JSON.TOGGLE":
		value, found, err := s.store.JSONToggle(key, string(args[2]))
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		return boolean(value), nil

	case "JSON.ARRPOP":
		path := "$"
		index := -1

		if len(args) >= 3 {
			path = string(args[2])
		}
		if len(args) == 4 {
			parsed, err := strconv.Atoi(string(args[3]))
			if err != nil {
				return nil, errors.New("ERR value is not an integer or out of range")
			}
			index = parsed
		}

		value, found, err := s.store.JSONArrPop(key, path, index)
		if err != nil {
			return nil, err
		}
		if !found || value == nil {
			return nullBulk(), nil
		}
		return formatBulkString(value), nil

	case "JSON.ARRINSERT":
		index, err := strconv.Atoi(string(args[3]))
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}

		length, found, err := s.store.JSONArrInsert(
			key,
			string(args[2]),
			index,
			args[4:],
		)
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		return integer(length), nil

	case "JSON.ARRINDEX":
		var start *int
		var stop *int

		if len(args) >= 5 {
			v, err := strconv.Atoi(string(args[4]))
			if err != nil {
				return nil, errors.New("ERR value is not an integer or out of range")
			}
			start = &v
		}
		if len(args) == 6 {
			v, err := strconv.Atoi(string(args[5]))
			if err != nil {
				return nil, errors.New("ERR value is not an integer or out of range")
			}
			stop = &v
		}

		index, found, err := s.store.JSONArrIndex(
			key,
			string(args[2]),
			args[3],
			start,
			stop,
		)
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		return integer(index), nil

	case "JSON.CLEAR":
		path := "$"
		if len(args) == 3 {
			path = string(args[2])
		}

		cleared, err := s.store.JSONClear(key, path)
		if err != nil {
			return nil, err
		}
		return integer(cleared), nil
	case "JSON.SET":
		path := string(args[2])

		nx := false
		xx := false

		if len(args) == 5 {
			switch strings.ToUpper(string(args[4])) {
			case "NX":
				nx = true
			case "XX":
				xx = true
			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		applied, err := s.store.JSONSet(key, path, args[3], nx, xx)
		if err != nil {
			return nil, err
		}

		if !applied {
			return nullBulk(), nil
		}

		return []byte("+OK\r\n"), nil

	case "JSON.GET":
		path := "$"

		if len(args) == 3 {
			path = string(args[2])
		}

		value, found, err := s.store.JSONGet(key, path)
		if err != nil {
			return nil, err
		}

		return optionalBulk(value, found), nil

	case "APPEND":
		length, err := s.store.Append(key, args[2])
		if err != nil {
			return nil, err
		}

		return integer(int64(length)), nil

	case "GETRANGE":
		start, err := parseInt64(args[2])
		if err != nil {
			return nil, err
		}

		end, err := parseInt64(args[3])
		if err != nil {
			return nil, err
		}

		value := s.store.GetRange(key, start, end)

		return formatBulkString(value), nil

	case "SETRANGE":
		offset, err := parseInt64(args[2])
		if err != nil {
			return nil, err
		}

		length, err := s.store.SetRange(key, offset, args[3])
		if err != nil {
			return nil, err
		}

		return integer(int64(length)), nil

	case "GETBIT":
		offset, err := parseInt64(args[2])
		if err != nil {
			return nil, errors.New(
				"ERR bit offset is not an integer or out of range",
			)
		}

		bit, err := s.store.GetBit(key, offset)
		if err != nil {
			return nil, err
		}

		return integer(bit), nil

	case "SETBIT":
		offset, err := parseInt64(args[2])
		if err != nil {
			return nil, errors.New(
				"ERR bit offset is not an integer or out of range",
			)
		}

		bitValue, err := strconv.Atoi(string(args[3]))
		if err != nil || (bitValue != 0 && bitValue != 1) {
			return nil, errors.New(
				"ERR bit is not an integer or out of range",
			)
		}

		oldBit, err := s.store.SetBit(
			key,
			offset,
			bitValue,
		)
		if err != nil {
			return nil, err
		}

		return integer(oldBit), nil

	case "BITCOUNT":
		if len(args) == 3 {
			return nil, errors.New("ERR syntax error")
		}

		var start *int64
		var end *int64
		bitMode := false

		if len(args) >= 4 {
			startValue, err := parseInt64(args[2])
			if err != nil {
				return nil, errors.New("ERR value is not an integer or out of range")
			}

			endValue, err := parseInt64(args[3])
			if err != nil {
				return nil, errors.New("ERR value is not an integer or out of range")
			}

			start = &startValue
			end = &endValue
		}

		if len(args) == 5 {
			unit := strings.ToUpper(string(args[4]))

			switch unit {
			case "BYTE":
			case "BIT":
				bitMode = true
			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		return integer(
			s.store.BitCount(
				key,
				start,
				end,
				bitMode,
			),
		), nil

	case "BITOP":
		op := strings.ToUpper(string(args[1]))
		destination := string(args[2])

		switch op {
		case "AND", "OR", "XOR", "NOT",
			"DIFF", "DIFF1", "ANDOR", "ONE":
		default:
			return nil, errors.New("ERR syntax error")
		}

		if op == "NOT" && len(args) != 4 {
			return nil, errors.New(
				"ERR BITOP NOT must be called with a single source key",
			)
		}

		sourceKeys := make([]string, 0, len(args)-3)

		for _, arg := range args[3:] {
			sourceKeys = append(sourceKeys, string(arg))
		}

		length, err := s.store.BitOp(
			op,
			destination,
			sourceKeys,
		)
		if err != nil {
			return nil, err
		}

		return integer(int64(length)), nil

	case "BITPOS":
		bitValue, err := strconv.Atoi(string(args[2]))
		if err != nil || (bitValue != 0 && bitValue != 1) {
			return nil, errors.New(
				"ERR bit must be 0 or 1",
			)
		}

		var start *int64
		var end *int64
		bitMode := false

		if len(args) >= 4 {
			v, err := parseInt64(args[3])
			if err != nil {
				return nil, errors.New(
					"ERR value is not an integer or out of range",
				)
			}

			start = &v
		}

		if len(args) >= 5 {
			v, err := parseInt64(args[4])
			if err != nil {
				return nil, errors.New(
					"ERR value is not an integer or out of range",
				)
			}

			end = &v
		}

		if len(args) == 6 {
			unit := strings.ToUpper(string(args[5]))

			switch unit {
			case "BYTE":
			case "BIT":
				bitMode = true
			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		position, err := s.store.BitPos(
			key,
			bitValue,
			start,
			end,
			bitMode,
		)
		if err != nil {
			return nil, err
		}

		return integer(position), nil

	case "GETDEL":
		value, found := s.store.GetDel(key)

		return optionalBulk(value, found), nil
	case "GETEX":
		var expireAt *time.Time
		persist := false

		if len(args) > 2 {
			option := strings.ToUpper(string(args[2]))

			switch option {
			case "PERSIST":
				if len(args) != 3 {
					return nil, errors.New("ERR syntax error")
				}

				persist = true

			case "EX", "PX", "EXAT", "PXAT":
				if len(args) != 4 {
					return nil, errors.New("ERR syntax error")
				}

				n, err := parseInt64(args[3])
				if err != nil {
					return nil, err
				}

				var when time.Time

				switch option {
				case "EX":
					if n <= 0 {
						return nil, errors.New("ERR invalid expire time in 'getex' command")
					}

					if n > math.MaxInt64/int64(time.Second) {
						return nil, errors.New("ERR invalid expire time in 'getex' command")
					}

					when = time.Now().Add(time.Duration(n) * time.Second)

				case "PX":
					if n <= 0 {
						return nil, errors.New("ERR invalid expire time in 'getex' command")
					}

					if n > math.MaxInt64/int64(time.Millisecond) {
						return nil, errors.New("ERR invalid expire time in 'getex' command")
					}

					when = time.Now().Add(time.Duration(n) * time.Millisecond)

				case "EXAT":
					when = time.Unix(n, 0)

				case "PXAT":
					when = time.UnixMilli(n)
				}

				expireAt = &when

			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		value, found := s.store.GetEx(key, expireAt, persist)

		return optionalBulk(value, found), nil

	case "SNUG.AOFREWRITE":
		writer, ok := s.journal.(interface {
			Rewrite([]persistence.Record) error
		})
		if !ok {
			return nil, errors.New("ERR AOF is disabled")
		}
		if err := writer.Rewrite(s.store.Export(nil)); err != nil {
			return nil, errors.New("ERR AOF rewrite failed")
		}
		return []byte("+OK\r\n"), nil
	case "SNUG.COMPACT":
		if s.optimizer == nil {
			return nil, errors.New("ERR optimizer is disabled")
		}
		if key == "" || strings.EqualFold(key, "ALL") {
			s.optimizer.Sample(256)
			s.store.Compact(64 << 20)
			return []byte("+QUEUED\r\n"), nil
		}
		if !s.optimizer.Queue(key) {
			return nil, errors.New("ERR optimizer queue is full")
		}
		return []byte("+QUEUED\r\n"), nil
	case "SNUG.TYPE":
		valueType, found := s.store.ValueTypeOf(key)
		if !found {
			return nullBulk(), nil
		}
		return formatBulkString([]byte(valueType.String())), nil

	case "SNUG.CANDIDATES":
		report, found := s.store.CandidateDiagnostics(key)
		if !found {
			return nullBulk(), nil
		}

		var b strings.Builder

		fmt.Fprintf(
			&b,
			"logical_bytes:%d\ncurrent_codec:%s\ncurrent_bytes:%d\nheat_class:%s\n",
			report.LogicalBytes,
			report.CurrentName,
			report.CurrentBytes,
			report.Heat,
		)

		for _, candidate := range report.Candidates {
			fmt.Fprintf(
				&b,
				"candidate:%s bytes:%d eligible:%t reason:%s\n",
				candidate.Name,
				candidate.Bytes,
				candidate.Eligible,
				candidate.Reason,
			)
		}

		fmt.Fprintf(
			&b,
			"winner:%s\nwinner_bytes:%d\n",
			report.WinnerName,
			report.WinnerBytes,
		)

		return formatBulkString([]byte(b.String())), nil

	case "SNUG.ENCODING", "SNUG.MEMORY", "SNUG.POLICY":
		name, raw, encoded, found := s.store.Encoding(key)
		if !found {
			return nullBulk(), nil
		}
		if cmd == "SNUG.ENCODING" {
			return formatBulkString([]byte(name)), nil
		}
		if cmd == "SNUG.MEMORY" {
			return array(formatBulkString([]byte("logical_bytes")), integer(int64(raw)), formatBulkString([]byte("encoded_bytes")), integer(int64(encoded))), nil
		}
		heat, _ := s.store.Policy(key)
		return formatBulkString([]byte(fmt.Sprintf("heat_class:%s\ncurrent_codec:%s\nraw_bytes:%d\nencoded_bytes:%d\nreason:smallest verified eligible cheap representation\n", heat, name, raw, encoded))), nil
	case "SNUG.SHAPES":
		limit := 200

		if len(args) == 2 {
			n, err := strconv.Atoi(string(args[1]))
			if err != nil || n <= 0 || n > 10000 {
				return nil, errors.New("ERR limit must be between 1 and 10000")
			}
			limit = n
		}

		shapes, total := s.store.JSONShapes(limit)

		var b strings.Builder

		fmt.Fprintf(
			&b,
			"total_shapes:%d\nreturned:%d\n",
			total,
			len(shapes),
		)

		for _, shape := range shapes {
			key := shape.Key

			// Shape keys may be large. Keep the command readable while
			// preserving enough of the template for identification.
			if len(key) > 200 {
				key = key[:200] + "..."
			}

			fmt.Fprintf(
				&b,
				"refs:%d bytes:%d key:%q\n",
				shape.Refs,
				shape.Bytes,
				key,
			)
		}

		return formatBulkString([]byte(b.String())), nil

	case "SNUG.STATS":
		m := s.store.Memory()
		arenaWaste := uint64(0)
		if m.ArenaBytes > m.ArenaPayloadBytes {
			arenaWaste = m.ArenaBytes - m.ArenaPayloadBytes
		}

		arenaInternalWaste := uint64(0)
		if m.ArenaLiveBlockBytes > m.ArenaPayloadBytes {
			arenaInternalWaste = m.ArenaLiveBlockBytes - m.ArenaPayloadBytes
		}

		arenaDeadWaste := uint64(0)
		if m.ArenaBytes > m.ArenaLiveBlockBytes {
			arenaDeadWaste = m.ArenaBytes - m.ArenaLiveBlockBytes
		}

		var optimizerQueued, optimizerRewritten, optimizerSkipped uint64
		var optimizerStale, optimizerDropped uint64
		var optimizerQueueDepth, optimizerQueueCapacity int
		if s.optimizer != nil {
			stats := s.optimizer.Stats()
			optimizerQueued = stats.Queued
			optimizerRewritten = stats.Rewritten
			optimizerSkipped = stats.Skipped
			optimizerStale = stats.Stale
			optimizerDropped = stats.Dropped
			optimizerQueueDepth = stats.QueueDepth
			optimizerQueueCapacity = stats.QueueCapacity
		}

		return formatBulkString([]byte(fmt.Sprintf(
			"accounted_bytes:%d\n"+
				"index_reserved_bytes:%d\n"+
				"entry_bytes:%d\n"+
				"meta_bytes:%d\n"+
				"arena_bytes:%d\n"+
				"arena_payload_bytes:%d\n"+
				"arena_live_block_bytes:%d\n"+
				"arena_internal_waste_bytes:%d\n"+
				"arena_dead_waste_bytes:%d\n"+
				"arena_waste_bytes:%d\n"+
				"schema_reserved_bytes:%d\n"+
				"max_memory:%d\n"+
				"optimizer_queued:%d\n"+
				"optimizer_rewritten:%d\n"+
				"optimizer_skipped:%d\n"+
				"optimizer_stale:%d\n"+
				"optimizer_dropped:%d\n"+
				"optimizer_queue_depth:%d\n"+
				"optimizer_queue_capacity:%d\n",
			m.AccountedBytes,
			m.IndexReservedBytes,
			m.EntryBytes,
			m.MetaBytes,
			m.ArenaBytes,
			m.ArenaPayloadBytes,
			m.ArenaLiveBlockBytes,
			arenaInternalWaste,
			arenaDeadWaste,
			arenaWaste,
			m.SchemaBytes,
			m.MaxBytes,
			optimizerQueued,
			optimizerRewritten,
			optimizerSkipped,
			optimizerStale,
			optimizerDropped,
			optimizerQueueDepth,
			optimizerQueueCapacity,
		))), nil
	case "PING":
		if len(args) == 2 {
			return formatBulkString(args[1]), nil
		}
		return []byte("+PONG\r\n"), nil
	case "ECHO":
		return formatBulkString(args[1]), nil
	case "QUIT":
		return []byte("+OK\r\n"), nil
	case "SELECT":
		if key != "0" {
			return nil, errors.New("ERR DB index is out of range")
		}
		return []byte("+OK\r\n"), nil
	case "HELLO":
		if len(args) == 1 {
			return helloReply(2, 0), nil
		}

		protocol, err := strconv.Atoi(string(args[1]))
		if err != nil {
			return nil, errors.New(
				"ERR Protocol version is not an integer or out of range",
			)
		}

		if protocol != 2 {
			return nil, errors.New(
				"NOPROTO unsupported protocol version",
			)
		}

		return helloReply(2, 0), nil
	case "SET":
		options, err := setOptions(args[3:])
		if err != nil {
			return nil, err
		}

		applied, previous, hadPrevious, err :=
			s.store.SetWithOptions(key, args[2], options)

		if err != nil {
			return nil, err
		}

		if applied {
			// Only queue values that can benefit from background work.
			// Cheap scalar codecs already run synchronously in the engine.
			if s.optimizer != nil && s.store.ShouldQueueOptimization(args[2]) {
				s.optimizer.NoteForegroundWrite()
				s.optimizer.Queue(key)
			}
		}

		// Redis SET ... GET returns the previous value regardless of
		// whether NX/XX allowed the write to happen.
		if options.Get {
			return optionalBulk(previous, hadPrevious), nil
		}

		if !applied {
			return nullBulk(), nil
		}

		return []byte("+OK\r\n"), nil
	case "TYPE":
		return []byte("+" + s.store.Type(key) + "\r\n"), nil
	case "SETNX":
		applied, err := s.store.SetConditional(key, args[2], engine.SetOptions{NX: true})
		return boolean(applied), err

	case "SETEX", "PSETEX":
		n, err := parseInt64(args[2])
		if err != nil || n <= 0 {
			return nil, fmt.Errorf(
				"ERR invalid expire time in '%s' command",
				strings.ToLower(cmd),
			)
		}

		unit := time.Second
		if cmd == "PSETEX" {
			unit = time.Millisecond
		}

		if n > math.MaxInt64/int64(unit) {
			return nil, fmt.Errorf(
				"ERR invalid expire time in '%s' command",
				strings.ToLower(cmd),
			)
		}

		_, err = s.store.SetConditional(
			key,
			args[3],
			engine.SetOptions{TTL: time.Duration(n) * unit},
		)
		if err != nil {
			return nil, err
		}

		return []byte("+OK\r\n"), nil
	case "GET", "STRLEN":
		v, found := s.store.Get(key)
		if cmd == "STRLEN" {
			return integer(int64(len(v))), nil
		}
		return optionalBulk(v, found), nil
	case "GETSET":
		v, found, err := s.store.GetSet(key, args[2])
		return optionalBulk(v, found), err
	case "MGET":
		values, found := s.store.MGet(keys(args[1:]))
		items := make([][]byte, len(values))
		for i, v := range values {
			items[i] = optionalBulk(v, found[i])
		}
		return array(items...), nil
	case "MSET":
		if len(args)%2 != 1 {
			return nil, errors.New("ERR wrong number of arguments for 'mset' command")
		}
		var names []string
		var values [][]byte
		for i := 1; i < len(args); i += 2 {
			names = append(names, string(args[i]))
			values = append(values, args[i+1])
		}
		err := s.store.MSet(names, values)
		return []byte("+OK\r\n"), err
	case "MSETNX":
		if len(args)%2 != 1 {
			return nil, errors.New("ERR wrong number of arguments for 'msetnx' command")
		}

		names := make([]string, 0, (len(args)-1)/2)
		values := make([][]byte, 0, (len(args)-1)/2)

		for i := 1; i < len(args); i += 2 {
			names = append(names, string(args[i]))
			values = append(values, args[i+1])
		}

		applied, err := s.store.MSetNX(names, values)
		if err != nil {
			return nil, err
		}

		return boolean(applied), nil

	case "EXISTS":
		return integer(s.store.Exists(keys(args[1:]))), nil
	case "DEL", "UNLINK":
		keys := make([]string, 0, len(args)-1)

		for _, arg := range args[1:] {
			keys = append(keys, string(arg))
		}

		deleted := s.store.DeleteMany(keys)

		return integer(deleted), nil

	case "INCR", "INCRBY", "DECR", "DECRBY":
		delta := int64(1)
		var err error
		if len(args) == 3 {
			delta, err = parseInt64(args[2])
			if err != nil {
				return nil, err
			}
		}
		var n int64
		if cmd == "DECR" || cmd == "DECRBY" {
			n, err = s.store.Sub(key, delta)
		} else {
			n, err = s.store.Add(key, delta)
		}
		return integer(n), err
	case "TTL", "PTTL":
		return integer(s.store.TTL(key, cmd == "PTTL")), nil
	case "EXPIRE", "PEXPIRE":
		n, err := parseInt64(args[2])
		if err != nil {
			return nil, err
		}

		unit := time.Second
		if cmd == "PEXPIRE" {
			unit = time.Millisecond
		}

		if n > math.MaxInt64/int64(unit) {
			return nil, errors.New("ERR invalid expire time")
		}

		condition := ""

		if len(args) == 4 {
			condition = strings.ToUpper(string(args[3]))

			switch condition {
			case "NX", "XX", "GT", "LT":
			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		return boolean(
			s.store.ExpireConditional(
				key,
				time.Duration(n)*unit,
				condition,
			),
		), nil
	case "PERSIST":
		return boolean(s.store.Persist(key)), nil

	case "RENAME", "RENAMENX":
		source := string(args[1])
		destination := string(args[2])

		renamed, err := s.store.Rename(
			source,
			destination,
			cmd == "RENAMENX",
		)

		if err != nil {
			return nil, err
		}

		if cmd == "RENAMENX" {
			return boolean(renamed), nil
		}

		return []byte("+OK\r\n"), nil

	case "SCAN":
		cursor, err := strconv.ParseUint(string(args[1]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR invalid cursor")
		}

		count := 10
		pattern := "*"

		for i := 2; i < len(args); {
			option := strings.ToUpper(string(args[i]))

			switch option {
			case "COUNT":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}

				n, err := strconv.Atoi(string(args[i+1]))
				if err != nil || n <= 0 {
					return nil, errors.New("ERR syntax error")
				}

				// Protect SnugKV from ridiculous COUNT values.
				if n > 10000 {
					n = 10000
				}

				count = n
				i += 2

			case "MATCH":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}

				pattern = string(args[i+1])
				i += 2

			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		nextCursor, foundKeys := s.store.Scan(cursor, count, pattern)

		items := make([][]byte, 0, len(foundKeys))

		for _, foundKey := range foundKeys {
			items = append(items, formatBulkString([]byte(foundKey)))
		}

		return array(
			formatBulkString([]byte(strconv.FormatUint(nextCursor, 10))),
			array(items...),
		), nil

	case "KEYS":
		foundKeys := s.store.Keys(string(args[1]))

		items := make([][]byte, 0, len(foundKeys))

		for _, foundKey := range foundKeys {
			items = append(items, formatBulkString([]byte(foundKey)))
		}

		return array(items...), nil

	case "RANDOMKEY":
		key, found := s.store.RandomKey()
		if !found {
			return nullBulk(), nil
		}

		return formatBulkString([]byte(key)), nil

	case "DBSIZE":
		return integer(int64(s.store.Stats().Keys)), nil

	case "INFO":
		section := strings.ToLower(key)
		if section != "" && section != "all" && section != "default" && section != "server" && section != "memory" && section != "stats" && section != "keyspace" {
			return formatBulkString(nil), nil
		}
		st := s.store.Stats()
		out := ""
		if section == "" || section == "all" || section == "default" || section == "server" {
			out += "# Server\r\nsnugkv_version:0.1.0\r\n"
		}
		if section == "" || section == "all" || section == "default" || section == "memory" {
			m := s.store.Memory()
			used := m.AccountedBytes

			out += fmt.Sprintf(
				"# Memory\r\n"+
					"used_memory:%d\r\n"+
					"used_memory_human:%s\r\n"+
					"used_memory_peak:%d\r\n"+
					"used_memory_peak_human:%s\r\n"+
					"used_memory_dataset:%d\r\n"+
					"used_memory_overhead:%d\r\n"+
					"maxmemory:%d\r\n"+
					"maxmemory_human:%s\r\n"+
					"maxmemory_policy:noeviction\r\n"+
					"logical_key_bytes:%d\r\n"+
					"logical_value_bytes:%d\r\n"+
					"index_reserved_bytes:%d\r\n"+
					"arena_bytes:%d\r\n"+
					"schema_reserved_bytes:%d\r\n",
				used,
				formatBytes(used),
				used,
				formatBytes(used),
				st.KeyBytes+st.ValueBytes,
				m.IndexReservedBytes+m.EntryBytes+m.SchemaBytes,
				m.MaxBytes,
				formatBytes(m.MaxBytes),
				st.KeyBytes,
				st.ValueBytes,
				m.IndexReservedBytes,
				m.ArenaBytes,
				m.SchemaBytes,
			)
		}
		if section == "" || section == "all" || section == "default" || section == "stats" {
			out += fmt.Sprintf("# Stats\r\ntotal_commands_processed:%d\r\n", atomic.LoadUint64(&s.commands))
		}
		if section == "" || section == "all" || section == "default" || section == "keyspace" {
			out += fmt.Sprintf("# Keyspace\r\ndb0:keys=%d\r\n", st.Keys)
		}
		return formatBulkString([]byte(out)), nil

	case "FLUSHDB", "FLUSHALL":
		if len(args) == 2 {
			option := strings.ToUpper(string(args[1]))

			if option != "SYNC" && option != "ASYNC" {
				return nil, errors.New("ERR syntax error")
			}

			// SnugKV currently has one database.
			// ASYNC is accepted for Redis compatibility but reset is
			// currently performed synchronously.
		}

		s.store.FlushDB()
		return []byte("+OK\r\n"), nil

	case "TOUCH":
		keys := make([]string, 0, len(args)-1)

		for _, arg := range args[1:] {
			keys = append(keys, string(arg))
		}

		return integer(int64(s.store.Touch(keys))), nil

	case "INCRBYFLOAT":
		increment, err := strconv.ParseFloat(string(args[2]), 64)

		if err != nil || math.IsNaN(increment) || math.IsInf(increment, 0) {
			return nil, errors.New("ERR value is not a valid float")
		}

		result, err := s.store.AddFloat(key, increment)
		if err != nil {
			return nil, err
		}

		return formatBulkString([]byte(result)), nil

	case "EXPIREAT", "PEXPIREAT":
		timestamp, err := parseInt64(args[2])
		if err != nil {
			return nil, err
		}

		var when time.Time

		if cmd == "PEXPIREAT" {
			when = time.UnixMilli(timestamp)
		} else {
			when = time.Unix(timestamp, 0)
		}

		condition := ""

		if len(args) == 4 {
			condition = strings.ToUpper(string(args[3]))

			switch condition {
			case "NX", "XX", "GT", "LT":
			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		return boolean(
			s.store.ExpireAtConditional(key, when, condition),
		), nil

	case "EXPIRETIME", "PEXPIRETIME":
		return integer(
			s.store.ExpireTime(key, cmd == "PEXPIRETIME"),
		), nil

	case "COMMAND":
		commandEntry := func(name string) []byte {
			return commandInfoReply(name)
		}

		if len(args) == 1 {
			names := make([]string, 0, len(commandTable))

			for name := range commandTable {
				names = append(names, name)
			}

			sort.Strings(names)

			entries := make([][]byte, 0, len(names))
			for _, name := range names {
				entries = append(entries, commandEntry(name))
			}

			return array(entries...), nil
		}

		subcommand := strings.ToUpper(string(args[1]))

		switch subcommand {
		case "COUNT":
			if len(args) != 2 {
				return nil, errors.New("ERR wrong number of arguments for 'command|count' command")
			}

			return integer(int64(len(commandTable))), nil

		case "INFO":
			if len(args) == 2 {
				names := make([]string, 0, len(commandTable))

				for name := range commandTable {
					names = append(names, name)
				}

				sort.Strings(names)

				entries := make(
					[][]byte,
					0,
					len(names),
				)

				for _, name := range names {
					entries = append(
						entries,
						commandEntry(name),
					)
				}

				return array(entries...), nil
			}

			entries := make(
				[][]byte,
				0,
				len(args)-2,
			)

			for _, arg := range args[2:] {
				name := strings.ToUpper(
					string(arg),
				)

				if !commandInfoSupported(name) {
					entries = append(
						entries,
						nullBulk(),
					)
					continue
				}

				entries = append(
					entries,
					commandEntry(name),
				)
			}

			return array(entries...), nil

		case "DOCS":
			return commandDocsReply(args[2:]), nil

		case "GETKEYS":
			if len(args) < 3 {
				return nil, errors.New(
					"ERR wrong number of arguments for 'command|getkeys' command",
				)
			}

			refs, err := commandKeys(args[2:])
			if err != nil {
				return nil, err
			}

			return commandGetKeysReply(refs), nil

		case "GETKEYSANDFLAGS":
			if len(args) < 3 {
				return nil, errors.New(
					"ERR wrong number of arguments for 'command|getkeysandflags' command",
				)
			}

			refs, err := commandKeys(args[2:])
			if err != nil {
				return nil, err
			}

			return commandGetKeysAndFlagsReply(refs), nil

		default:
			return nil, errors.New("ERR unknown subcommand")
		}
	}
	return nil, errors.New("ERR command unavailable")
}
func setOptions(args [][]byte) (engine.SetOptions, error) {
	var o engine.SetOptions

	expirationSeen := false
	conditionSeen := false
	getSeen := false

	for i := 0; i < len(args); i++ {
		option := strings.ToUpper(string(args[i]))

		switch option {
		case "NX", "XX":
			if conditionSeen {
				return o, errors.New("ERR syntax error")
			}

			conditionSeen = true
			o.NX = option == "NX"
			o.XX = option == "XX"

		case "GET":
			if getSeen {
				return o, errors.New("ERR syntax error")
			}

			getSeen = true
			o.Get = true

		case "KEEPTTL":
			if expirationSeen {
				return o, errors.New("ERR syntax error")
			}

			expirationSeen = true
			o.KeepTTL = true

		case "EX", "PX":
			if expirationSeen || i+1 >= len(args) {
				return o, errors.New("ERR syntax error")
			}

			expirationSeen = true
			i++

			n, err := parseInt64(args[i])
			if err != nil {
				return o, errors.New("ERR invalid expire time in 'set' command")
			}

			unit := time.Second
			if option == "PX" {
				unit = time.Millisecond
			}

			if n <= 0 || n > math.MaxInt64/int64(unit) {
				return o, errors.New("ERR invalid expire time in 'set' command")
			}

			o.TTL = time.Duration(n) * unit

		case "EXAT", "PXAT":
			if expirationSeen || i+1 >= len(args) {
				return o, errors.New("ERR syntax error")
			}

			expirationSeen = true
			i++

			n, err := parseInt64(args[i])
			if err != nil || n <= 0 {
				return o, errors.New("ERR invalid expire time in 'set' command")
			}

			o.HasExpireAt = true

			if option == "PXAT" {
				o.ExpireAt = time.UnixMilli(n)
			} else {
				o.ExpireAt = time.Unix(n, 0)
			}

		default:
			return o, errors.New("ERR syntax error")
		}
	}

	return o, nil
}

func keys(args [][]byte) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = string(arg)
	}
	return out
}
func integer(n int64) []byte { return []byte(fmt.Sprintf(":%d\r\n", n)) }
func boolean(v bool) []byte {
	if v {
		return integer(1)
	}
	return integer(0)
}
func nullBulk() []byte { return []byte("$-1\r\n") }
func optionalBulk(v []byte, found bool) []byte {
	if !found {
		return nullBulk()
	}
	return formatBulkString(v)
}
func formatBulkString(v []byte) []byte {
	out := make([]byte, 0, len(v)+32)
	out = append(out, '$')
	out = strconv.AppendInt(out, int64(len(v)), 10)
	out = append(out, 13, 10)
	out = append(out, v...)
	return append(out, 13, 10)
}
func array(items ...[]byte) []byte {
	out := []byte(fmt.Sprintf("*%d\r\n", len(items)))
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}
func parseInt64(v []byte) (int64, error) {
	n, err := strconv.ParseInt(string(v), 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != string(v) {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
	return n, nil
}

func formatBytes(n uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)

	switch {
	case n >= gb:
		return fmt.Sprintf("%.2fG", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.2fM", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.2fK", float64(n)/kb)
	default:
		return fmt.Sprintf("%dB", n)
	}
}