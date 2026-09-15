package engine

import "sort"

// JSONShapeInfo describes one UNIQUE globally cached JSON shape.
type JSONShapeInfo struct {
	Key   string
	Refs  int
	Bytes int
}

// JSONShapes returns unique schemas from the global shape catalog.
func (s *Store) JSONShapes(limit int) ([]JSONShapeInfo, int) {
	if limit <= 0 {
		limit = 200
	}

	shapes := s.loadShapeStore()
	if shapes == nil {
		return nil, 0
	}

	schemas := shapes.SchemaSnapshot()
	total := len(schemas)

	sort.Slice(schemas, func(i, j int) bool {
		return schemas[i].Key < schemas[j].Key
	})

	if len(schemas) > limit {
		schemas = schemas[:limit]
	}

	out := make([]JSONShapeInfo, 0, len(schemas))

	for _, schema := range schemas {
		out = append(out, JSONShapeInfo{
			Key:   schema.Key,
			Refs:  schema.Refs,
			Bytes: schema.Bytes,
		})
	}

	return out, total
}
