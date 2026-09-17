package engine

import "testing"

func TestMemoryAdmissionModes(t *testing.T) {
	s, err := NewWithOptions(Options{
		Shards:    1,
		MaxMemory: 4096,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	s.memory.mu.Lock()
	s.memory.used = 4000

	if !s.exceedsMemoryLimitLocked(5000, enforceMemoryLimit) {
		s.memory.mu.Unlock()
		t.Fatal("enforceMemoryLimit should reject memory above max")
	}

	if s.exceedsMemoryLimitLocked(5000, allowOverMemoryLimit) {
		s.memory.mu.Unlock()
		t.Fatal("allowOverMemoryLimit should permit memory above max")
	}

	if s.exceedsMemoryLimitLocked(4096, enforceMemoryLimit) {
		s.memory.mu.Unlock()
		t.Fatal("exact max_memory boundary should be allowed")
	}

	s.memory.mu.Unlock()
}
