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
	eviction         string
	metrics          *stats.Registry
	optimizer        *optimizer.Optimizer
	store            *engine.Store
	commands         uint64
	journal          Journal
	durableMu        sync.Mutex
	durabilityFailed bool
}

func New(store *engine.Store) *Server { return &Server{store: store, metrics: stats.New()} }

type commandInfo struct {
	min, max, first, last, step int
	write                       bool
}

var commandTable = map[string]commandInfo{
	"SNUG.AOFREWRITE": {1, 1, 0, 0, 0, false},
	"SNUG.COMPACT":    {1, 2, 0, 0, 0, false},
	"SNUG.ENCODING":   {2, 2, 1, 1, 1, false}, "SNUG.MEMORY": {2, 2, 1, 1, 1, false}, "SNUG.STATS": {1, 1, 0, 0, 0, false}, "SNUG.POLICY": {2, 2, 1, 1, 1, false},
	"PING": {1, 2, 0, 0, 0, false}, "ECHO": {2, 2, 0, 0, 0, false}, "QUIT": {1, 1, 0, 0, 0, false},
	"SELECT": {2, 2, 0, 0, 0, false}, "HELLO": {2, 2, 0, 0, 0, false}, "INFO": {1, 2, 0, 0, 0, false},
	"DBSIZE": {1, 1, 0, 0, 0, false}, "COMMAND": {1, 1, 0, 0, 0, false},
	"SCAN": {2, 0, 0, 0, 0, false},
	"KEYS": {2, 2, 0, 0, 0, false},
	"RANDOMKEY": {1, 1, 0, 0, 0, false},
	"RENAME":   {3, 3, 1, 2, 1, true},
    "RENAMENX": {3, 3, 1, 2, 1, true},
	"TOUCH": {2, 0, 1, -1, 1, false},
	"EXPIREAT":     {3, 3, 1, 1, 1, true},
    "PEXPIREAT":    {3, 3, 1, 1, 1, true},
    "EXPIRETIME":   {2, 2, 1, 1, 1, false},
    "PEXPIRETIME":  {2, 2, 1, 1, 1, false},
	"SET": {3, 0, 1, 1, 1, true}, "GET": {2, 2, 1, 1, 1, false}, "MGET": {2, 0, 1, -1, 1, false},
	"DEL": {2, 0, 1, -1, 1, true}, "EXISTS": {2, 0, 1, -1, 1, false}, "GETSET": {3, 3, 1, 1, 1, true},
	"GETDEL": {2, 2, 1, 1, 1, true},
    "GETEX":  {2, 4, 1, 1, 1, true},
	"SETNX": {3, 3, 1, 1, 1, true}, "MSET": {3, 0, 1, -1, 2, true},
	"INCR": {2, 2, 1, 1, 1, true}, "DECR": {2, 2, 1, 1, 1, true}, "INCRBY": {3, 3, 1, 1, 1, true}, "DECRBY": {3, 3, 1, 1, 1, true},
	"INCRBYFLOAT": {3, 3, 1, 1, 1, true},
	"STRLEN": {2, 2, 1, 1, 1, false}, "EXPIRE": {3, 3, 1, 1, 1, true}, "PEXPIRE": {3, 3, 1, 1, 1, true},
	"TTL": {2, 2, 1, 1, 1, false}, "PTTL": {2, 2, 1, 1, 1, false}, "PERSIST": {2, 2, 1, 1, 1, true},
	"TYPE": {2, 2, 1, 1, 1, false},
	"FLUSHDB": {1, 1, 0, 0, 0, true},
	"UNLINK": {2, 0, 1, -1, 1, true},
	"APPEND":   {3, 3, 1, 1, 1, true},
    "GETRANGE": {4, 4, 1, 1, 1, false},
    "SETRANGE": {4, 4, 1, 1, 1, true},
	"JSON.SET": {4, 4, 1, 1, 1, true},
    "JSON.GET": {2, 3, 1, 1, 1, false},
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
	case "JSON.SET":
	path := string(args[2])

	if err := s.store.JSONSet(key, path, args[3]); err != nil {
		return nil, err
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
	case "SNUG.STATS":
		m := s.store.Memory()
		return formatBulkString([]byte(fmt.Sprintf("accounted_bytes:%d\nindex_reserved_bytes:%d\nentry_bytes:%d\narena_bytes:%d\nschema_reserved_bytes:%d\nmax_memory:%d\n", m.AccountedBytes, m.IndexReservedBytes, m.EntryBytes, m.ArenaBytes, m.SchemaBytes, m.MaxBytes))), nil
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
		if key != "2" {
			return nil, errors.New("NOPROTO unsupported protocol version")
		}
		return array(formatBulkString([]byte("server")), formatBulkString([]byte("snugkv")), formatBulkString([]byte("version")), formatBulkString([]byte("0.1.0")), formatBulkString([]byte("proto")), integer(2), formatBulkString([]byte("mode")), formatBulkString([]byte("standalone")), formatBulkString([]byte("role")), formatBulkString([]byte("master"))), nil
	case "SET":
		options, err := setOptions(args[3:])
		if err != nil {
			return nil, err
		}
		applied, err := s.store.SetConditional(key, args[2], options)
		if err != nil {
			return nil, err
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
		if n <= 0 {
			return boolean(s.store.Expire(key, 0)), nil
		}
		if n > math.MaxInt64/int64(unit) {
			return nil, errors.New("ERR invalid expire time")
		}
		return boolean(s.store.Expire(key, time.Duration(n)*unit)), nil
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
			out += fmt.Sprintf("# Memory\r\nlogical_key_bytes:%d\r\nlogical_value_bytes:%d\r\nused_memory_accounted:%d\r\nindex_reserved_bytes:%d\r\nmaxmemory:%d\r\n", st.KeyBytes, st.ValueBytes, m.AccountedBytes, m.IndexReservedBytes, m.MaxBytes)
		}
		if section == "" || section == "all" || section == "default" || section == "stats" {
			out += fmt.Sprintf("# Stats\r\ntotal_commands_processed:%d\r\n", atomic.LoadUint64(&s.commands))
		}
		if section == "" || section == "all" || section == "default" || section == "keyspace" {
			out += fmt.Sprintf("# Keyspace\r\ndb0:keys=%d\r\n", st.Keys)
		}
		return formatBulkString([]byte(out)), nil
	
	case "FLUSHDB":
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

	return boolean(s.store.ExpireAt(key, when)), nil

case "EXPIRETIME", "PEXPIRETIME":
	return integer(
		s.store.ExpireTime(key, cmd == "PEXPIRETIME"),
	), nil


	case "COMMAND":
		names := make([]string, 0, len(commandTable))
		for name := range commandTable {
			names = append(names, name)
		}
		sort.Strings(names)
		var entries [][]byte
		for _, name := range names {
			c := commandTable[name]
			arity := c.min
			if c.max != c.min {
				arity = -arity
			}
			flag := "readonly"
			if c.write {
				flag = "write"
			}
			entries = append(entries, array(formatBulkString([]byte(strings.ToLower(name))), integer(int64(arity)), array(formatBulkString([]byte(flag))), integer(int64(c.first)), integer(int64(c.last)), integer(int64(c.step))))
		}
		return array(entries...), nil
	}
	return nil, errors.New("ERR command unavailable")
}
func setOptions(args [][]byte) (engine.SetOptions, error) {
	var o engine.SetOptions
	expire := false
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "NX", "XX":
			if o.NX || o.XX {
				return o, errors.New("ERR syntax error")
			}
			o.NX = strings.EqualFold(string(args[i]), "NX")
			o.XX = !o.NX
		case "EX", "PX":
			if expire || i+1 >= len(args) {
				return o, errors.New("ERR syntax error")
			}
			expire = true
			unit := time.Second
			if strings.EqualFold(string(args[i]), "PX") {
				unit = time.Millisecond
			}
			i++
			n, err := parseInt64(args[i])
			if err != nil || n <= 0 || n > math.MaxInt64/int64(unit) {
				return o, errors.New("ERR invalid expire time in 'set' command")
			}
			o.TTL = time.Duration(n) * unit
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
	out = append(out, fmt.Sprintf("$%d\r\n", len(v))...)
	out = append(out, v...)
	return append(out, '\r', '\n')
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
