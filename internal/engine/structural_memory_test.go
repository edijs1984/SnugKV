package engine

import "testing"

func TestStructuralMemoryReportsFixedShardLayout(t *testing.T) {
	s, err := NewWithShards(256)
	if err != nil {
		t.Fatal(err)
	}

	m := s.StructuralMemory()
	if m.ShardStructBytes == 0 || m.IndexTableStructBytes == 0 {
		t.Fatalf("zero structural component: %+v", m)
	}
	if m.PerShardBytes != m.ShardStructBytes+m.IndexTableStructBytes {
		t.Fatalf("per-shard bytes = %d, components = %d + %d", m.PerShardBytes, m.ShardStructBytes, m.IndexTableStructBytes)
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
}
