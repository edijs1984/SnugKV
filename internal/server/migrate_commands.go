package server

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"reflect"
	"snugkv/internal/persistence"
	"strconv"
	"strings"
	"time"
)

var migrateCommands = map[string]commandInfo{
	"MIGRATE": {6, 0, 3, 3, 1, true},
}

func init() {
	for name, info := range migrateCommands {
		commandTable[name] = info
	}
}

func isMigrateCommand(args [][]byte) bool {
	return len(args) > 0 && strings.EqualFold(string(args[0]), "MIGRATE")
}

type migrateOptions struct {
	copy     bool
	replace  bool
	username []byte
	password []byte
	keys     [][]byte
}

func parseMigrateOptions(args [][]byte) (migrateOptions, error) {
	var options migrateOptions
	if len(args) < 6 {
		return options, errors.New("ERR wrong number of arguments for 'migrate' command")
	}

	for i := 6; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "COPY":
			options.copy = true
		case "REPLACE":
			options.replace = true
		case "AUTH":
			if i+1 >= len(args) {
				return migrateOptions{}, errors.New("ERR syntax error")
			}
			i++
			options.username = nil
			options.password = append([]byte(nil), args[i]...)
		case "AUTH2":
			if i+2 >= len(args) {
				return migrateOptions{}, errors.New("ERR syntax error")
			}
			options.username = append([]byte(nil), args[i+1]...)
			options.password = append([]byte(nil), args[i+2]...)
			i += 2
		case "KEYS":
			if len(args[3]) != 0 {
				return migrateOptions{}, errors.New("ERR When using MIGRATE KEYS option, the key argument must be set to the empty string")
			}
			options.keys = append([][]byte(nil), args[i+1:]...)
			return options, nil
		default:
			return migrateOptions{}, errors.New("ERR syntax error")
		}
	}

	if options.keys == nil {
		options.keys = [][]byte{args[3]}
	}
	return options, nil
}

func parseMigrateLong(arg []byte) (int64, error) {
	n, err := strconv.ParseInt(string(arg), 10, 64)
	if err != nil {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
	return n, nil
}

func migrateSourceKeys(args [][]byte) ([]string, migrateOptions, error) {
	options, err := parseMigrateOptions(args)
	if err != nil {
		return nil, migrateOptions{}, err
	}
	keys := make([]string, 0, len(options.keys))
	for _, key := range options.keys {
		keys = append(keys, string(key))
	}
	return keys, options, nil
}

type migrateItem struct {
	key     []byte
	payload []byte
	ttlMS   int64
}

func appendRESPBulk(dst *bytes.Buffer, value []byte) {
	fmt.Fprintf(dst, "$%d\r\n", len(value))
	dst.Write(value)
	dst.WriteString("\r\n")
}

func appendRESPCommand(dst *bytes.Buffer, parts ...[]byte) {
	fmt.Fprintf(dst, "*%d\r\n", len(parts))
	for _, part := range parts {
		appendRESPBulk(dst, part)
	}
}

func readMigrateLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) < 3 || !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("invalid target response")
	}
	return strings.TrimSuffix(line, "\r\n"), nil
}

func migrateTargetError(line string) error {
	if len(line) > 0 && line[0] == '-' {
		line = line[1:]
	}
	return errors.New("ERR Target instance replied with error: " + line)
}

func recordsEqual(a, b []persistence.Record) bool {
	return reflect.DeepEqual(a, b)
}

func (s *Server) prepareMigrateItems(keys [][]byte) ([]migrateItem, error) {
	items := make([]migrateItem, 0, len(keys))
	nowMS := time.Now().UnixMilli()

	for _, rawKey := range keys {
		key := string(rawKey)
		valueType, found := s.store.ValueTypeOf(key)
		if !found {
			continue
		}
		payload, err := s.dumpKey(key, valueType)
		if err != nil {
			return nil, err
		}
		if payload == nil {
			continue
		}

		records := s.store.Export([]string{key})
		if len(records) != 1 || records[0].Deleted {
			continue
		}

		ttl := int64(0)
		if records[0].ExpiresAtMS != 0 {
			ttl = records[0].ExpiresAtMS - nowMS
			if ttl < 0 {
				continue
			}
			if ttl < 1 {
				ttl = 1
			}
		}

		items = append(items, migrateItem{
			key: append([]byte(nil), rawKey...),
			payload: payload,
			ttlMS: ttl,
		})
	}

	return items, nil
}

func (s *Server) executeMigrate(args [][]byte) ([]byte, error) {
	if len(args) < 6 {
		return nil, errors.New("ERR wrong number of arguments for 'migrate' command")
	}

	options, err := parseMigrateOptions(args)
	if err != nil {
		return nil, err
	}

	db, err := parseMigrateLong(args[4])
	if err != nil {
		return nil, err
	}
	timeoutMS, err := parseMigrateLong(args[5])
	if err != nil {
		return nil, err
	}
	if timeoutMS <= 0 {
		timeoutMS = 1000
	}

	items, err := s.prepareMigrateItems(options.keys)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []byte("+NOKEY\r\n"), nil
	}

	host := string(args[1])
	port := string(args[2])
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeoutMS > int64(^uint64(0)>>1)/int64(time.Millisecond) {
		timeout = time.Duration(^uint64(0) >> 1)
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return nil, errors.New("IOERR error or timeout connecting to the client")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	var request bytes.Buffer
	authExpected := false
	if options.password != nil {
		authExpected = true
		if options.username != nil {
			appendRESPCommand(&request, []byte("AUTH"), options.username, options.password)
		} else {
			appendRESPCommand(&request, []byte("AUTH"), options.password)
		}
	}

	appendRESPCommand(&request, []byte("SELECT"), []byte(strconv.FormatInt(db, 10)))

	for _, item := range items {
		parts := [][]byte{
			[]byte("RESTORE"),
			item.key,
			[]byte(strconv.FormatInt(item.ttlMS, 10)),
			item.payload,
		}
		if options.replace {
			parts = append(parts, []byte("REPLACE"))
		}
		appendRESPCommand(&request, parts...)
	}

	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return nil, errors.New("IOERR error or timeout writing to target instance")
	}
	payload := request.Bytes()
	for len(payload) > 0 {
		n, writeErr := conn.Write(payload)
		if writeErr != nil || n <= 0 {
			return nil, errors.New("IOERR error or timeout writing to target instance")
		}
		payload = payload[n:]
	}

	reader := bufio.NewReader(conn)
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, errors.New("IOERR error or timeout reading to target instance")
	}

	var authLine string
	if authExpected {
		authLine, err = readMigrateLine(reader)
		if err != nil {
			return nil, errors.New("IOERR error or timeout reading to target instance")
		}
	}

	selectLine, err := readMigrateLine(reader)
	if err != nil {
		return nil, errors.New("IOERR error or timeout reading to target instance")
	}

	var firstTargetErr error
	for _, item := range items {
		restoreLine, readErr := readMigrateLine(reader)
		if readErr != nil {
			if firstTargetErr != nil {
				return nil, firstTargetErr
			}
			return nil, errors.New("IOERR error or timeout reading to target instance")
		}

		var targetErr error
		if authExpected && strings.HasPrefix(authLine, "-") {
			targetErr = migrateTargetError(authLine)
		} else if strings.HasPrefix(selectLine, "-") {
			targetErr = migrateTargetError(selectLine)
		} else if strings.HasPrefix(restoreLine, "-") {
			targetErr = migrateTargetError(restoreLine)
		}

		if targetErr != nil {
			if firstTargetErr == nil {
				firstTargetErr = targetErr
			}
			continue
		}

		if !options.copy {
			s.store.DeleteMany([]string{string(item.key)})
		}
	}

	if firstTargetErr != nil {
		return nil, firstTargetErr
	}
	return []byte("+OK\r\n"), nil
}

func (s *Server) executeMigrateDurableLocked(args [][]byte) ([]byte, error) {
	keys, options, parseErr := migrateSourceKeys(args)
	if parseErr != nil {
		return s.executeMigrate(args)
	}

	if options.copy || s.journal == nil {
		return s.executeMigrate(args)
	}

	before := s.store.Export(keys)
	result, runErr := s.executeMigrate(args)
	after := s.store.Export(keys)

	if recordsEqual(before, after) {
		return result, runErr
	}

	if s.durabilityFailed {
		if rollbackErr := s.store.Restore(before, true); rollbackErr != nil {
			return nil, errors.New("ERR persistence and rollback failed")
		}
		return nil, errors.New("ERR persistence is unavailable; restart after repairing storage")
	}

	if err := s.journal.Append(after); err != nil {
		s.durabilityFailed = true
		if rollbackErr := s.store.Restore(before, true); rollbackErr != nil {
			return nil, errors.New("ERR persistence and rollback failed")
		}
		return nil, errors.New("ERR persistence append failed")
	}

	return result, runErr
}
