package server

import "snugkv/internal/engine"

func functionInvocationOOMAllowed(fn *registeredFunction) bool {
	return fn != nil && (fn.allowOom || fn.noWrites)
}

func (s *Server) rejectFunctionInvocationOOM(fn *registeredFunction) error {
	if fn == nil || functionInvocationOOMAllowed(fn) {
		return nil
	}

	memory := s.store.Memory()
	if memory.MaxBytes > 0 && memory.AccountedBytes > memory.MaxBytes {
		return engine.ErrOOM
	}

	return nil
}

// withFunctionOOMBypass runs one allow-oom function invocation with the engine's
// max-memory admission disabled. FCALL execution already holds durableMu for the
// complete invocation, so ordinary command execution cannot observe or use this
// temporary admission state. The configured limit is restored before FCALL
// returns.
func (s *Server) withFunctionOOMBypass(
	fn *registeredFunction,
	run func() ([]byte, error),
) ([]byte, error) {
	if fn == nil || !fn.allowOom {
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
