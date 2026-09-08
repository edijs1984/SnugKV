package engine

import (
	"bytes"
	"morphcache/internal/codec"
	"morphcache/internal/codec/jsonshape"
	"sync/atomic"
	"time"
)

type Candidate struct {
	Key          string
	Version      uint64
	Value        []byte
	EncodedBytes int
	LastRewrite  time.Time
	Heat         string
}

func (s *Store) Candidate(key string, maxBytes int) (Candidate, bool) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.data.Get(key)
	if !ok || e.expired(s.now()) || e.rawLength > maxBytes {
		return Candidate{}, false
	}
	return Candidate{key, e.version, s.decode(e), len(e.value), e.lastRewrite.Time(), heat(e, s.now())}, true
}
func heat(e entry, now time.Time) string {
	if now.Sub(e.lastWrite.Time()) < time.Minute && e.writes >= 10 {
		return "write-heavy"
	}
	if now.Sub(e.lastAccess.Time()) < time.Minute && e.reads >= 100 {
		return "hot"
	}
	if now.Sub(e.lastAccess.Time()) > 5*time.Minute && now.Sub(e.lastWrite.Time()) > 5*time.Minute {
		return "cold"
	}
	return "warm"
}
func (s *Store) Policy(key string) (string, bool) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.data.Get(key)
	if !ok || e.expired(s.now()) {
		return "", false
	}
	if !s.encoding {
		return "disabled", true
	}
	return heat(e, s.now()), true
}
func (s *Store) EncodeCandidate(candidate Candidate) codec.Record {
	best := s.codecs.Encode(candidate.Value)
	sh := s.shardFor(candidate.Key)
	if sh.shapes != nil {
		schema, slots, ok := sh.shapes.Candidate(candidate.Value)
		if ok {
			data := sh.shapes.EncodeSlots(slots)
			if len(data)+16 < len(best.Data) {
				decoded, err := jsonshape.Decode(schema, data, len(candidate.Value))
				if err == nil && bytes.Equal(decoded, candidate.Value) {
					best = codec.Record{ID: 5, RawLength: len(candidate.Value), Data: data, Schema: schema}
				}
			}
		}
	}
	if s.compression && candidate.Heat != "hot" && candidate.Heat != "write-heavy" {
		compressed := s.codecs.EncodeGeneral(candidate.Value, candidate.Heat == "cold")
		if len(compressed.Data) < len(best.Data) {
			best = compressed
		}
	}
	return best
}

// Rewrite commits only the exact version observed. It verifies logical bytes,
// keeps TTL/access metadata, and advances the CAS version to reject other jobs.
func (s *Store) Rewrite(candidate Candidate, record codec.Record) bool {
	if !s.encoding {
		return false
	}
	decoded, err := s.codecs.Decode(record, len(candidate.Value))
	if err != nil || !bytes.Equal(decoded, candidate.Value) {
		return false
	}
	sh := s.shardFor(candidate.Key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.data.Get(candidate.Key)
	if !ok || e.expired(s.now()) || e.version != candidate.Version || len(record.Data) >= len(e.value) {
		return false
	}
	if !bytes.Equal(s.decode(e), candidate.Value) {
		return false
	}
	e.value = bytes.Clone(record.Data)
	e.codecID = record.ID
	e.schema = record.Schema
	e.rawLength = record.RawLength
	e.lastRewrite = stampOf(s.now())
	return s.publish(sh, candidate.Key, e) == nil
}

// SampleKeys takes bounded samples from rotating shards; Go map iteration avoids
// retaining an unbounded list of all keys for background work.
func (s *Store) SampleKeys(limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	start := int(atomic.AddUint64(&s.sampleCursor, 1)-1) % len(s.shards)
	for n := 0; n < len(s.shards) && len(out) < limit; n++ {
		sh := &s.shards[(start+n)%len(s.shards)]
		sh.mu.Lock()
		n := limit - len(out)
		if n > 4 {
			n = 4
		}
		keys, next := sh.data.Sample(sh.sampleOffset, 64, n)
		sh.sampleOffset = next
		out = append(out, keys...)
		sh.mu.Unlock()

	}
	return out
}
