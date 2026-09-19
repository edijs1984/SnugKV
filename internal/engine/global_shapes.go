package engine

import (
	"snugkv/internal/codec/jsonshape"
	"sync"
	"sync/atomic"
)

// This is a logical budget, not 32 MiB allocated up-front.
// At ~2 KiB/schema it is enough for many thousands of shapes.
const globalShapeBudget = 32 << 20

type globalShapeCatalog struct {
	mu  sync.Mutex
	ptr atomic.Pointer[jsonshape.Store]
}

func (s *Store) loadShapeStore() *jsonshape.Store {
	return s.shapeCatalog.ptr.Load()
}

// ensureGlobalShapeStore creates exactly one JSON-shape catalog for
// the whole engine, regardless of how many data shards exist.
func (s *Store) ensureGlobalShapeStore() *jsonshape.Store {
	if !s.shapeEncoding {
		return nil
	}

	if shapes := s.shapeCatalog.ptr.Load(); shapes != nil {
		return shapes
	}

	s.shapeCatalog.mu.Lock()
	defer s.shapeCatalog.mu.Unlock()

	if shapes := s.shapeCatalog.ptr.Load(); shapes != nil {
		return shapes
	}

	s.memory.mu.Lock()

	next := s.memory.used + shapeStoreBaseBytes
	if max := s.memory.max.Load(); max > 0 && next > max {
		s.memory.mu.Unlock()
		return nil
	}

	shapes := jsonshape.New(globalShapeBudget, 4)

	s.memory.used = next
	s.memory.schemas += shapeStoreBaseBytes

	s.memory.mu.Unlock()

	s.shapeCatalog.ptr.Store(shapes)

	return shapes
}

// dropGlobalShapeStoreLocked is called while all shard locks are held.
// FLUSHDB should remove learned optimization metadata as well as keys.
func (s *Store) dropGlobalShapeStoreLocked() {
	s.shapeCatalog.mu.Lock()
	defer s.shapeCatalog.mu.Unlock()

	if s.shapeCatalog.ptr.Load() == nil {
		for i := range s.shards {
			s.shards[i].shapes = nil
		}
		return
	}

	s.shapeCatalog.ptr.Store(nil)

	for i := range s.shards {
		s.shards[i].shapes = nil
	}

	s.memory.mu.Lock()

	if s.memory.used >= shapeStoreBaseBytes {
		s.memory.used -= shapeStoreBaseBytes
	} else {
		s.memory.used = 0
	}

	if s.memory.schemas >= shapeStoreBaseBytes {
		s.memory.schemas -= shapeStoreBaseBytes
	} else {
		s.memory.schemas = 0
	}

	s.memory.mu.Unlock()
}
