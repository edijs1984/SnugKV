package server

import (
	"errors"
	"fmt"
	"strings"

	"snugkv/internal/engine"
)

type evalScriptMetadata struct {
	source             string
	body               string
	flagged            bool
	noWrites           bool
	allowOom           bool
	allowStale         bool
	noCluster          bool
	allowCrossSlotKeys bool
}

func parseEvalScriptMetadata(source string) (evalScriptMetadata, error) {
	meta := evalScriptMetadata{
		source: source,
		body:   source,
	}
	if !strings.HasPrefix(source, "#!") {
		return meta, nil
	}

	meta.flagged = true
	line := source
	meta.body = ""
	if newline := strings.IndexByte(source, '\n'); newline >= 0 {
		line = strings.TrimSuffix(source[:newline], "\r")
		meta.body = source[newline+1:]
	}

	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 || !strings.EqualFold(fields[0], "lua") {
		engineName := ""
		if len(fields) > 0 {
			engineName = fields[0]
		}
		return evalScriptMetadata{}, fmt.Errorf(
			"ERR Unexpected engine in script shebang: %s",
			engineName,
		)
	}

	for _, field := range fields[1:] {
		if !strings.HasPrefix(field, "flags=") {
			return evalScriptMetadata{}, fmt.Errorf(
				"ERR Unknown script shebang option: %s",
				field,
			)
		}

		rawFlags := strings.TrimPrefix(field, "flags=")
		if rawFlags == "" {
			continue
		}
		for _, rawFlag := range strings.Split(rawFlags, ",") {
			flag := strings.ToLower(strings.TrimSpace(rawFlag))
			switch flag {
			case "no-writes":
				meta.noWrites = true
			case "allow-oom":
				meta.allowOom = true
			case "allow-stale":
				meta.allowStale = true
			case "no-cluster":
				meta.noCluster = true
			case "allow-cross-slot-keys":
				meta.allowCrossSlotKeys = true
			default:
				return evalScriptMetadata{}, fmt.Errorf(
					"ERR Unexpected flag in script shebang: %s",
					rawFlag,
				)
			}
		}
	}

	return meta, nil
}

func (s *Server) scriptAlreadyOOM() bool {
	memory := s.store.Memory()
	return memory.MaxBytes > 0 && memory.AccountedBytes > memory.MaxBytes
}

func (s *Server) rejectFlaggedScriptInvocationOOM(meta evalScriptMetadata) error {
	if !meta.flagged || meta.allowOom || meta.noWrites {
		return nil
	}
	if s.scriptAlreadyOOM() {
		return engine.ErrOOM
	}
	return nil
}

func validateReadOnlyEvalMetadata(meta evalScriptMetadata) error {
	if meta.flagged && !meta.noWrites {
		return errors.New("ERR Can not execute a script with write flag using *_ro command.")
	}
	return nil
}

// withFlaggedScriptMemoryAdmission applies Redis's invocation-based policy for
// Eval scripts with shebang metadata. Once a write-capable flagged script is
// admitted, nested commands are allowed to finish even if the script grows past
// maxmemory. allow-oom changes only whether an already-OOM invocation is
// admitted. no-writes scripts do not need a memory bypass because their nested
// write path is forbidden.
func (s *Server) withFlaggedScriptMemoryAdmission(
	meta evalScriptMetadata,
	run func() ([]byte, error),
) ([]byte, error) {
	if !meta.flagged || meta.noWrites {
		return run()
	}

	maxMemory := s.store.MaxMemory()
	if maxMemory == 0 {
		return run()
	}

	s.store.SetMaxMemory(0)
	defer s.store.SetMaxMemory(maxMemory)
	return run()
}

type legacyScriptOOMState struct {
	startedOOM      bool
	writeAdmitted   bool
	bypassActive    bool
	savedMaxMemory  uint64
}

func newLegacyScriptOOMState(s *Server) *legacyScriptOOMState {
	return &legacyScriptOOMState{
		startedOOM: s.scriptAlreadyOOM(),
	}
}

func (state *legacyScriptOOMState) afterSuccessfulCommand(
	s *Server,
	args [][]byte,
) {
	if state == nil ||
		!state.startedOOM ||
		state.writeAdmitted ||
		!scriptCommandWritesOrReplicates(args) {

		return
	}

	// Legacy Eval scripts use Redis's pre-shebang behavior: while already OOM,
	// the first write command is the admission boundary. If that command is
	// allowed and succeeds (for example DEL), later writes in the same script
	// are allowed even if they carry denyoom.
	state.writeAdmitted = true
	state.savedMaxMemory = s.store.MaxMemory()
	if state.savedMaxMemory > 0 {
		s.store.SetMaxMemory(0)
		state.bypassActive = true
	}
}

func (state *legacyScriptOOMState) restore(s *Server) {
	if state != nil && state.bypassActive {
		s.store.SetMaxMemory(state.savedMaxMemory)
		state.bypassActive = false
	}
}
