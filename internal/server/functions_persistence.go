package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var functionPersistencePaths sync.Map // map[*Server]string

func functionPersistencePath(aofPath, snapshotPath string) string {
	if aofPath != "" {
		return aofPath + ".functions"
	}
	if snapshotPath != "" {
		return snapshotPath + ".functions"
	}
	return ""
}

// ConfigureFunctionPersistence restores the function registry from its durable
// sidecar and enables atomic full-registry snapshots after function mutations.
// It is intended to run during process startup, before the listener is announced.
func (s *TCPServer) ConfigureFunctionPersistence(aofPath, snapshotPath string) error {
	path := functionPersistencePath(aofPath, snapshotPath)
	if path == "" {
		return nil
	}
	if data, err := os.ReadFile(path); err == nil {
		codes, decodeErr := decodeFunctionDump(data)
		if decodeErr != nil {
			return fmt.Errorf("function recovery: %w", decodeErr)
		}
		libs, compileErr := s.server.compileFunctionLibraries(codes)
		if compileErr != nil {
			return fmt.Errorf("function recovery: %w", compileErr)
		}
		if restoreErr := functionRegistryForServer(s.server).restoreCompiled(libs, "FLUSH"); restoreErr != nil {
			closeFunctionLibraries(libs)
			return fmt.Errorf("function recovery: %w", restoreErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("function recovery: %w", err)
	}
	functionPersistencePaths.Store(s.server, path)
	return nil
}

func writeSidecarStateAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".snug-functions-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Server) persistFunctionRegistry() error {
	value, ok := functionPersistencePaths.Load(s)
	if !ok {
		return nil
	}
	payload, err := encodeFunctionDump(functionRegistryForServer(s).libraryCodes())
	if err != nil {
		return err
	}
	return writeSidecarStateAtomic(value.(string), payload)
}

func functionAdminMutation(args [][]byte) bool {
	if len(args) < 2 || !strings.EqualFold(string(args[0]), "FUNCTION") {
		return false
	}
	switch strings.ToUpper(string(args[1])) {
	case "LOAD", "DELETE", "FLUSH", "RESTORE":
		return true
	default:
		return false
	}
}

func (s *Server) executeFunctionTopLevel(args [][]byte) ([]byte, error) {
	if scriptExecutionActive(s) {
		return nil, errors.New("ERR This Redis command is not allowed from script")
	}
	before := functionRegistryForServer(s).libraryCodes()

	var response []byte
	var err error
	if handledResponse, handled, handledErr := s.executeFunctionDumpRestore(args); handled {
		response, err = handledResponse, handledErr
	} else {
		response, err = s.executeFunctionCommand(args)
	}
	if err != nil || !functionAdminMutation(args) {
		return response, err
	}
	if err = s.persistFunctionRegistry(); err == nil {
		return response, nil
	}

	// A persistence failure must not leave the durable registry and in-memory
	// registry disagreeing. Rebuild the previous logical libraries. Persistent
	// function-local Lua variables are intentionally not part of Redis DUMP
	// semantics, so a rollback can reset that ephemeral VM-local state.
	libs, compileErr := s.compileFunctionLibraries(before)
	if compileErr != nil {
		return nil, errors.New("ERR function persistence and rollback failed")
	}
	if restoreErr := functionRegistryForServer(s).restoreCompiled(libs, "FLUSH"); restoreErr != nil {
		closeFunctionLibraries(libs)
		return nil, errors.New("ERR function persistence and rollback failed")
	}
	return nil, errors.New("ERR function persistence write failed")
}
