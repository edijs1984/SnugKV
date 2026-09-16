package engine

import "testing"

func TestStructuralMemoryReportsFixedShardLayout(t *testing.T) {
	s, err := NewWithShards(256)
	if err != nil {
		t.Fatal(err)
	}

	m := s.StructuralMemory()
	if m.ShardStructBytes == 0 {
		t.Fatalf("zero shard structural bytes: %+v", m)
	}
	if m.IndexTableStructBytes != 0 {
		t.Fatalf("embedded index reported separate allocation: %+v", m)
	}
	if m.PerShardBytes != m.ShardStructBytes {
		t.Fatalf("per-shard bytes = %d, shard bytes = %d", m.PerShardBytes, m.ShardStructBytes)
	}
	if m.TotalBytes != 256*m.PerShardBytes {
		t.Fatalf("total bytes = %d, want %d", m.TotalBytes, 256*m.PerShardBytes)
	}
	if m.LegacyBaselineBytes != 256*512 {
		t.Fatalf("legacy baseline = %d, want %d", m.LegacyBaselineBytes, 256*512)
	}
	if m.TotalBytes >= m.LegacyBaselineBytes {
		t.Fatalf("static layout %d no longer beats legacy baseline %d", m.TotalBytes, m.LegacyBaselineBytes)
	}

	accounted := s.Memory()
	if accounted.AccountedBytes != m.TotalBytes {
		t.Fatalf("empty store accounted bytes = %d, structural = %d", accounted.AccountedBytes, m.TotalBytes)
	}
	// Structural bytes remain in the legacy index-reservation bucket for now.
	// A later diagnostics-only change can split that bucket without changing
	// max_memory behavior again.
	if accounted.IndexReservedBytes != m.TotalBytes {
		t.Fatalf("empty store index bucket = %d, structural = %d", accounted.IndexReservedBytes, m.TotalBytes)
	}
}

func TestEmbeddedIndexShrinksFixedLayout(t *testing.T) {
	s, err := NewWithShards(256)
	if err != nil {
		t.Fatal(err)
	}

	m := s.StructuralMemory()
	// Before embedding, the measured fixed layout was 208 bytes per shard:
	// a 168-byte shard plus a separately allocated 40-byte index.Table.
	if m.PerShardBytes >= 208 {
		t.Fatalf("embedded layout = %d bytes/shard, want < 208", m.PerShardBytes)
	}
}

func TestStructuralMemoryDefinesMinimumMaxMemory(t *testing.T) {
	base := structuralMemoryBytes(256)
	if _, err := NewWithOptions(Options{Shards: 256, MaxMemory: base - 1}); err != ErrOOM {
		t.Fatalf("max_memory below structural baseline: got %v, want %v", err, ErrOOM)
	}
	if _, err := NewWithOptions(Options{Shards: 256, MaxMemory: base}); err != nil {
		t.Fatalf("max_memory at structural baseline rejected: %v", err)
	}
}
