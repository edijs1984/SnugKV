package jsonshape

import "sort"

// SchemaInfo is a read-only diagnostic view of one learned JSON shape.
type SchemaInfo struct {
	Key   string
	Refs  int
	Bytes int
}

// SchemaSnapshot returns the currently admitted schemas.
// It does not train, admit, evict, retain, or release anything.
func (s *Store) SchemaSnapshot() []SchemaInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]SchemaInfo, 0, len(s.schemas))

	for key, schema := range s.schemas {
		out = append(out, SchemaInfo{
			Key:   key,
			Refs:  schema.refs,
			Bytes: schema.Bytes,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Key < out[j].Key
	})

	return out
}
