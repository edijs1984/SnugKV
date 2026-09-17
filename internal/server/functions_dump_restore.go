package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"sort"
	"snugkv/internal/persistence"
	"strings"
)

const functionDumpMagic = "SNUGF001"

type functionDumpPayload struct {
	Libraries []string `json:"libraries"`
}

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

func encodeFunctionDump(codes []string) ([]byte, error) {
	payload, err := json.Marshal(functionDumpPayload{Libraries: codes})
	if err != nil {
		return nil, err
	}
	if len(payload) > persistence.MaxFrameBytes {
		return nil, errors.New("ERR function dump payload exceeds limit")
	}

	out := make([]byte, 0, len(functionDumpMagic)+4+len(payload)+4)
	out = append(out, functionDumpMagic...)
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(payload)))
	out = append(out, n[:]...)
	out = append(out, payload...)
	binary.LittleEndian.PutUint32(n[:], crc32.ChecksumIEEE(out))
	out = append(out, n[:]...)
	return out, nil
}

func decodeFunctionDump(data []byte) ([]string, error) {
	min := len(functionDumpMagic) + 8
	if len(data) < min || !bytes.Equal(data[:len(functionDumpMagic)], []byte(functionDumpMagic)) {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}
	length := int(binary.LittleEndian.Uint32(data[len(functionDumpMagic) : len(functionDumpMagic)+4]))
	payloadStart := len(functionDumpMagic) + 4
	payloadEnd := payloadStart + length
	if payloadEnd+4 != len(data) {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}
	want := binary.LittleEndian.Uint32(data[payloadEnd:])
	if crc32.ChecksumIEEE(data[:payloadEnd]) != want {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}
	var decoded functionDumpPayload
	dec := json.NewDecoder(bytes.NewReader(data[payloadStart:payloadEnd]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		return nil, errors.New("ERR DUMP payload version or checksum are wrong")
	}
	return decoded.Libraries, nil
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
			return errors.New("ERR Library already exists")
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
			return nil, true, errors.New("ERR syntax error")
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
