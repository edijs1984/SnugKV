package server

import (
	"encoding/binary"
	"errors"
	"sort"
	"snugkv/internal/persistence"
	"strconv"
	"strings"
)

const (
	functionRDBOpcodeFunction2 = byte(245)
	functionRDBVersion         = uint16(12)

	rdbEncInt8  = 0
	rdbEncInt16 = 1
	rdbEncInt32 = 2
	rdbEncLZF   = 3
)

func (r *functionRegistry) libraryCodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.libraries))
	for name := range r.libraries {
		names = append(names, name)
	}
	sort.Strings(names)
	codes := make([]string, 0, len(names))
	for _, name := range names {
		codes = append(codes, r.libraries[name].code)
	}
	return codes
}

func appendRDBLen(dst []byte, n uint64) []byte {
	switch {
	case n < 1<<6:
		return append(dst, byte(n))
	case n < 1<<14:
		return append(dst, byte((n>>8)|0x40), byte(n))
	case n <= uint64(^uint32(0)):
		dst = append(dst, 0x80)
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], uint32(n))
		return append(dst, buf[:]...)
	default:
		dst = append(dst, 0x81)
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], n)
		return append(dst, buf[:]...)
	}
}

func lzfCompressRedis(src []byte, outLimit int) ([]byte, bool) {
	const (
		hlog   = 16
		hsize  = 1 << hlog
		maxLit = 1 << 5
		maxOff = 1 << 13
		maxRef = (1 << 8) + (1 << 3)
	)
	if len(src) == 0 || outLimit <= 0 {
		return nil, false
	}

	// Redis builds liblzf with VERY_FAST=1. A zero-initialized offset table
	// gives the same valid-match behavior while remaining deterministic in Go.
	htab := make([]int, hsize)
	for i := range htab {
		htab[i] = -1
	}

	out := make([]byte, 1, outLimit)
	lit := 0
	ip := 0
	if len(src) < 2 {
		for ip < len(src) {
			if len(out) >= outLimit {
				return nil, false
			}
			lit++
			out = append(out, src[ip])
			ip++
		}
		out[0] = byte(lit - 1)
		return out, true
	}

	hval := (int(src[0]) << 8) | int(src[1])
	for ip < len(src)-2 {
		hval = (hval << 8) | int(src[ip+2])
		idx := (((hval >> (24 - hlog)) - hval*5) & (hsize - 1))
		ref := htab[idx]
		htab[idx] = ip

		off := 0
		match := false
		if ref >= 0 && ref < ip {
			off = ip - ref - 1
			if off < maxOff && ref > 0 &&
				ref+2 < len(src) &&
				src[ref] == src[ip] &&
				src[ref+1] == src[ip+1] &&
				src[ref+2] == src[ip+2] {
				match = true
			}
		}

		if match {
			length := 2
			maxLen := len(src) - ip - length
			if maxLen > maxRef {
				maxLen = maxRef
			}
			for length < maxLen && src[ref+length] == src[ip+length] {
				length++
			}
			length -= 2

			needed := 3
			if length < 7 {
				needed = 2
			}
			if len(out)-boolInt(lit == 0)+needed+1 > outLimit {
				return nil, false
			}

			out[len(out)-lit-1] = byte(lit - 1)
			if lit == 0 {
				out = out[:len(out)-1]
			}

			if length < 7 {
				out = append(out, byte((off>>8)+(length<<5)))
			} else {
				out = append(out, byte((off>>8)+(7<<5)), byte(length-7))
			}
			out = append(out, byte(off))
			lit = 0
			out = append(out, 0)

			ip += length + 2
			if ip >= len(src)-2 {
				break
			}

			// VERY_FAST=1 path from Redis liblzf.
			ip--
			hval = (int(src[ip]) << 8) | int(src[ip+1])
			hval = (hval << 8) | int(src[ip+2])
			idx = (((hval >> (24 - hlog)) - hval*5) & (hsize - 1))
			htab[idx] = ip
			ip++
			hval = (hval << 8) | int(src[ip+2])
			idx = (((hval >> (24 - hlog)) - hval*5) & (hsize - 1))
			htab[idx] = ip
			ip++
			continue
		}

		if len(out) >= outLimit {
			return nil, false
		}
		lit++
		out = append(out, src[ip])
		ip++
		if lit == maxLit {
			out[len(out)-lit-1] = byte(lit - 1)
			lit = 0
			out = append(out, 0)
		}
	}

	if len(out)+3 > outLimit {
		return nil, false
	}
	for ip < len(src) {
		lit++
		out = append(out, src[ip])
		ip++
		if lit == maxLit {
			out[len(out)-lit-1] = byte(lit - 1)
			lit = 0
			out = append(out, 0)
		}
	}
	out[len(out)-lit-1] = byte(lit - 1)
	if lit == 0 {
		out = out[:len(out)-1]
	}
	if len(out) > outLimit {
		return nil, false
	}
	return out, true
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func appendRDBRawString(dst []byte, value []byte) []byte {
	if len(value) > 20 {
		if compressed, ok := lzfCompressRedis(value, len(value)-4); ok {
			dst = append(dst, 0xC0|rdbEncLZF)
			dst = appendRDBLen(dst, uint64(len(compressed)))
			dst = appendRDBLen(dst, uint64(len(value)))
			return append(dst, compressed...)
		}
	}
	dst = appendRDBLen(dst, uint64(len(value)))
	return append(dst, value...)
}

func redisCRC64(data []byte) uint64 {
	const reflectedPoly = uint64(0x95ac9329ac4bc9b5)
	var table [256]uint64
	for i := range table {
		crc := uint64(i)
		for bit := 0; bit < 8; bit++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ reflectedPoly
			} else {
				crc >>= 1
			}
		}
		table[i] = crc
	}

	var crc uint64
	for _, b := range data {
		crc = table[byte(crc)^b] ^ (crc >> 8)
	}
	return crc
}

func encodeFunctionDump(codes []string) ([]byte, error) {
	out := make([]byte, 0)
	for _, code := range codes {
		out = append(out, functionRDBOpcodeFunction2)
		out = appendRDBRawString(out, []byte(code))
		if len(out) > persistence.MaxFrameBytes {
			return nil, errors.New("ERR function dump payload exceeds limit")
		}
	}

	var version [2]byte
	binary.LittleEndian.PutUint16(version[:], functionRDBVersion)
	out = append(out, version[:]...)

	var checksum [8]byte
	binary.LittleEndian.PutUint64(checksum[:], redisCRC64(out))
	out = append(out, checksum[:]...)

	if len(out) > persistence.MaxFrameBytes {
		return nil, errors.New("ERR function dump payload exceeds limit")
	}
	return out, nil
}

func readRDBLen(data []byte, pos *int) (uint64, bool, error) {
	if *pos >= len(data) {
		return 0, false, errors.New("truncated length")
	}
	first := data[*pos]
	*pos++

	switch first >> 6 {
	case 0:
		return uint64(first & 0x3f), false, nil
	case 1:
		if *pos >= len(data) {
			return 0, false, errors.New("truncated 14-bit length")
		}
		n := (uint64(first&0x3f) << 8) | uint64(data[*pos])
		*pos++
		return n, false, nil
	case 2:
		switch first {
		case 0x80:
			if len(data)-*pos < 4 {
				return 0, false, errors.New("truncated 32-bit length")
			}
			n := binary.BigEndian.Uint32(data[*pos : *pos+4])
			*pos += 4
			return uint64(n), false, nil
		case 0x81:
			if len(data)-*pos < 8 {
				return 0, false, errors.New("truncated 64-bit length")
			}
			n := binary.BigEndian.Uint64(data[*pos : *pos+8])
			*pos += 8
			return n, false, nil
		default:
			return 0, false, errors.New("invalid length encoding")
		}
	case 3:
		return uint64(first & 0x3f), true, nil
	default:
		return 0, false, errors.New("invalid length encoding")
	}
}

func lzfDecompress(src []byte, outLen int) ([]byte, error) {
	if outLen < 0 || outLen > persistence.MaxFrameBytes {
		return nil, errors.New("invalid LZF output length")
	}
	out := make([]byte, 0, outLen)
	for ip := 0; ip < len(src); {
		ctrl := int(src[ip])
		ip++
		if ctrl < 1<<5 {
			literal := ctrl + 1
			if ip+literal > len(src) || len(out)+literal > outLen {
				return nil, errors.New("invalid LZF literal run")
			}
			out = append(out, src[ip:ip+literal]...)
			ip += literal
			continue
		}

		length := ctrl >> 5
		refOffset := (ctrl & 0x1f) << 8
		if length == 7 {
			if ip >= len(src) {
				return nil, errors.New("invalid LZF extended length")
			}
			length += int(src[ip])
			ip++
		}
		if ip >= len(src) {
			return nil, errors.New("invalid LZF back reference")
		}
		refOffset += int(src[ip])
		ip++
		ref := len(out) - refOffset - 1
		length += 2
		if ref < 0 || len(out)+length > outLen {
			return nil, errors.New("invalid LZF back reference")
		}
		for i := 0; i < length; i++ {
			out = append(out, out[ref+i])
		}
	}
	if len(out) != outLen {
		return nil, errors.New("invalid LZF output length")
	}
	return out, nil
}

func decodeRDBString(data []byte, pos *int) ([]byte, error) {
	length, encoded, err := readRDBLen(data, pos)
	if err != nil {
		return nil, err
	}
	if !encoded {
		if length > uint64(persistence.MaxFrameBytes) || length > uint64(len(data)-*pos) {
			return nil, errors.New("invalid string length")
		}
		value := append([]byte(nil), data[*pos:*pos+int(length)]...)
		*pos += int(length)
		return value, nil
	}

	switch length {
	case rdbEncInt8:
		if *pos >= len(data) {
			return nil, errors.New("truncated int8")
		}
		v := int8(data[*pos])
		*pos++
		return []byte(strconv.FormatInt(int64(v), 10)), nil
	case rdbEncInt16:
		if len(data)-*pos < 2 {
			return nil, errors.New("truncated int16")
		}
		v := int16(binary.LittleEndian.Uint16(data[*pos : *pos+2]))
		*pos += 2
		return []byte(strconv.FormatInt(int64(v), 10)), nil
	case rdbEncInt32:
		if len(data)-*pos < 4 {
			return nil, errors.New("truncated int32")
		}
		v := int32(binary.LittleEndian.Uint32(data[*pos : *pos+4]))
		*pos += 4
		return []byte(strconv.FormatInt(int64(v), 10)), nil
	case rdbEncLZF:
		compressedLen, compressed, err := readRDBLen(data, pos)
		if err != nil || compressed {
			return nil, errors.New("invalid LZF compressed length")
		}
		originalLen, originalEncoded, err := readRDBLen(data, pos)
		if err != nil || originalEncoded {
			return nil, errors.New("invalid LZF original length")
		}
		if compressedLen > uint64(len(data)-*pos) || originalLen > uint64(persistence.MaxFrameBytes) {
			return nil, errors.New("invalid LZF payload length")
		}
		compressedData := data[*pos : *pos+int(compressedLen)]
		*pos += int(compressedLen)
		return lzfDecompress(compressedData, int(originalLen))
	default:
		return nil, errors.New("unsupported RDB string encoding")
	}
}

func decodeFunctionDump(data []byte) ([]string, error) {
	if len(data) < 10 || len(data) > persistence.MaxFrameBytes {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}

	trailer := len(data) - 10
	version := binary.LittleEndian.Uint16(data[trailer : trailer+2])
	if version == 0 || version > functionRDBVersion {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}
	wantChecksum := binary.LittleEndian.Uint64(data[trailer+2:])
	if redisCRC64(data[:trailer+2]) != wantChecksum {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}

	codes := make([]string, 0)
	pos := 0
	for pos < trailer {
		if data[pos] != functionRDBOpcodeFunction2 {
			return nil, errors.New("ERR DUMP payload version or checksum are wrong")
		}
		pos++
		code, err := decodeRDBString(data[:trailer], &pos)
		if err != nil {
			return nil, errors.New("ERR DUMP payload version or checksum are wrong")
		}
		codes = append(codes, string(code))
	}
	if pos != trailer {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}
	return codes, nil
}

func (s *Server) compileFunctionLibraries(codes []string) ([]*functionLibrary, error) {
	libs := make([]*functionLibrary, 0, len(codes))
	closeAll := func() {
		for _, lib := range libs {
			lib.state.Close()
		}
	}
	seenLibraries := make(map[string]struct{}, len(codes))
	seenFunctions := make(map[string]struct{})
	for _, code := range codes {
		lib, err := s.loadFunctionLibrary(code)
		if err != nil {
			closeAll()
			return nil, err
		}
		if _, exists := seenLibraries[lib.name]; exists {
			lib.state.Close()
			closeAll()
			return nil, errors.New("ERR Library already exists in payload")
		}
		seenLibraries[lib.name] = struct{}{}
		for name := range lib.functions {
			if _, exists := seenFunctions[name]; exists {
				lib.state.Close()
				closeAll()
				return nil, errors.New("ERR Function already exists in payload")
			}
			seenFunctions[name] = struct{}{}
		}
		libs = append(libs, lib)
	}
	return libs, nil
}

func closeFunctionLibraries(libs []*functionLibrary) {
	for _, lib := range libs {
		lib.state.Close()
	}
}

func (r *functionRegistry) restoreCompiled(libs []*functionLibrary, policy string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	incomingLibraries := make(map[string]*functionLibrary, len(libs))
	incomingFunctions := make(map[string]*registeredFunction)
	for _, lib := range libs {
		incomingLibraries[lib.name] = lib
		for lower, fn := range lib.functions {
			incomingFunctions[lower] = fn
		}
	}

	switch policy {
	case "FLUSH":
		for _, lib := range r.libraries {
			lib.state.Close()
		}
		r.libraries = incomingLibraries
		r.functions = incomingFunctions
		return nil
	case "APPEND", "REPLACE":
	default:
		return errors.New("ERR Wrong restore policy given, value should be either FLUSH, APPEND or REPLACE.")
	}

	for name, lib := range incomingLibraries {
		old := r.libraries[name]
		if old != nil && policy == "APPEND" {
			return errors.New("ERR Library " + name + " already exists")
		}
		for lower, fn := range lib.functions {
			if existing := r.functions[lower]; existing != nil && (old == nil || existing.library != old) {
				return errors.New("ERR Function " + fn.name + " already exists")
			}
		}
	}

	for name, lib := range incomingLibraries {
		if old := r.libraries[name]; old != nil {
			for lower := range old.functions {
				delete(r.functions, lower)
			}
			old.state.Close()
		}
		r.libraries[name] = lib
		for lower, fn := range lib.functions {
			r.functions[lower] = fn
		}
	}
	return nil
}

func (s *Server) executeFunctionDumpRestore(args [][]byte) ([]byte, bool, error) {
	if len(args) < 2 || !strings.EqualFold(string(args[0]), "FUNCTION") {
		return nil, false, nil
	}
	registry := functionRegistryForServer(s)
	switch strings.ToUpper(string(args[1])) {
	case "DUMP":
		if len(args) != 2 {
			return nil, true, errors.New("ERR wrong number of arguments for 'function|dump' command")
		}
		payload, err := encodeFunctionDump(registry.libraryCodes())
		if err != nil {
			return nil, true, err
		}
		return formatBulkString(payload), true, nil
	case "RESTORE":
		if len(args) != 3 && len(args) != 4 {
			return nil, true, errors.New("ERR wrong number of arguments for 'function|restore' command")
		}
		policy := "APPEND"
		if len(args) == 4 {
			policy = strings.ToUpper(string(args[3]))
			if policy != "APPEND" && policy != "FLUSH" && policy != "REPLACE" {
				return nil, true, errors.New("ERR Wrong restore policy given, value should be either FLUSH, APPEND or REPLACE.")
			}
		}
		codes, err := decodeFunctionDump(args[2])
		if err != nil {
			return nil, true, err
		}
		libs, err := s.compileFunctionLibraries(codes)
		if err != nil {
			return nil, true, err
		}
		if err := registry.restoreCompiled(libs, policy); err != nil {
			closeFunctionLibraries(libs)
			return nil, true, err
		}
		return []byte("+OK\r\n"), true, nil
	}
	return nil, false, nil
}
