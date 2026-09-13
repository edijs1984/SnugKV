package engine

import "sync/atomic"

type Inspection struct {
	EncodedBytes, LogicalBytes, Keys, Expired, Evicted       uint64
	Codecs                                                   map[string]uint64
	Schemas, SchemaBytes, DictionaryEntries, DictionaryBytes int
}

func (s *Store) Inspect() Inspection {
	out := Inspection{Codecs: make(map[string]uint64), Expired: atomic.LoadUint64(&s.expired), Evicted: atomic.LoadUint64(&s.evicted)}
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		now := s.now()
		for _, e := range sh.all() {
			if !e.expired(now) {
				out.Keys++
				out.EncodedBytes += uint64(len(e.value))
				out.LogicalBytes += uint64(e.rawLength)
				out.Codecs[s.codecs.Name(e.codecID)]++
			}
		}
		if sh.shapes != nil {
			n, b := sh.shapes.Stats()
			out.Schemas += n
			out.SchemaBytes += b
			n, b = sh.shapes.DictionaryStats()
			out.DictionaryEntries += n
			out.DictionaryBytes += b
		}
		sh.mu.RUnlock()
	}
	return out
}
func (s *Store) Evict(key string) bool {
	if s.Delete(key) {
		atomic.AddUint64(&s.evicted, 1)
		return true
	}
	return false
}

func (s *Store) Type(key string) string {
	sh := s.shardFor(key)

	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok {
		return "none"
	}

	if e.expired(s.now()) {
		s.remove(sh, key)
		return "none"
	}

	// At the moment all public Redis values in SnugKV are strings.
	// Internal codecs such as integer/json-shape are storage optimizations
	// and must not change Redis TYPE semantics.
	return "string"
}
