package engine

import (
	"bytes"
	"snugkv/internal/codec"
	"snugkv/internal/codec/jsonshape"
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

// OptimizationEligible performs the cheap read-only eligibility check before
// the optimizer reserves scratch/bandwidth resources. It deliberately does
// not update lastOptimize: a temporary reserve failure is not an optimization
// attempt and must not put the key into cooldown.
func (s *Store) OptimizationEligible(
	key string,
	rewriteInterval time.Duration,
	attemptInterval time.Duration,
) (int, bool) {
	sh := s.shardFor(key)

	sh.mu.RLock()
	defer sh.mu.RUnlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || e.expired(now) {
		return 0, false
	}

	if heat(e, now) == "write-heavy" {
		return 0, false
	}

	if !e.lastRewrite.IsZero() &&
		now.Sub(e.lastRewrite.Time()) < rewriteInterval {
		return 0, false
	}

	if !e.lastOptimize.IsZero() &&
		now.Sub(e.lastOptimize.Time()) < attemptInterval {
		return 0, false
	}

	return int(e.rawLength), true
}

// MarkOptimizationAttempt atomically rechecks eligibility and records the
// attempt after optimizer resources have already been reserved. This prevents
// duplicate queued samples from starting expensive work on the same key.
func (s *Store) MarkOptimizationAttempt(
	key string,
	rewriteInterval time.Duration,
	attemptInterval time.Duration,
) bool {
	sh := s.shardFor(key)

	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if !ok || e.expired(now) {
		return false
	}

	if heat(e, now) == "write-heavy" {
		return false
	}

	if !e.lastRewrite.IsZero() &&
		now.Sub(e.lastRewrite.Time()) < rewriteInterval {
		return false
	}

	if !e.lastOptimize.IsZero() &&
		now.Sub(e.lastOptimize.Time()) < attemptInterval {
		return false
	}

	e.lastOptimize = stampOf(now)
	sh.set(key, e)

	return true
}

func (s *Store) Candidate(key string, maxBytes int) (Candidate, bool) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) || int(e.rawLength) > maxBytes {
		return Candidate{}, false
	}
	return Candidate{key, e.version, s.decode(sh, e), len(sh.encoded(e)), e.lastRewrite.Time(), heat(e, s.now())}, true
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
	e, ok := sh.get(key)
	if !ok || e.expired(s.now()) {
		return "", false
	}
	if !s.encoding {
		return "disabled", true
	}
	return heat(e, s.now()), true
}
func (s *Store) ensureShapeStore(sh *shard) *jsonshape.Store {
	if !s.shapeEncoding {
		return nil
	}

	sh.mu.Lock()
	defer sh.mu.Unlock()

	if sh.shapes != nil {
		return sh.shapes
	}

	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()

	next := s.memory.used + shapeStoreBaseBytes
	if s.memory.max > 0 && next > s.memory.max {
		return nil
	}

	sh.shapes = jsonshape.New(16<<10, 8)
	s.memory.used = next
	s.memory.schemas += shapeStoreBaseBytes

	return sh.shapes
}

func structuredJSONCandidate(src []byte) bool {
	for _, b := range src {
		switch b {
		case ' ', '\n', '\r', '\t':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}

	return false
}

func (s *Store) EncodeCandidate(candidate Candidate) codec.Record {
	best := s.codecs.Encode(candidate.Value)
	sh := s.shardFor(candidate.Key)
	if structuredJSONCandidate(candidate.Value) {
		shapes := s.ensureShapeStore(sh)
		if shapes != nil {
			schema, slots, ok := shapes.Candidate(candidate.Value)
			if ok {
				data := shapes.EncodeSlots(slots)
				if len(data)+16 < len(best.Data) {
					decoded, err := jsonshape.Decode(schema, data, len(candidate.Value))
					if err == nil && bytes.Equal(decoded, candidate.Value) {
						best = codec.Record{ID: 5, RawLength: len(candidate.Value), Data: data, Schema: schema}
					}
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

type CandidateDiagnostic struct {
	Name     string
	Bytes    int
	Eligible bool
	Reason   string
}

type CandidateDiagnosticReport struct {
	CurrentName  string
	CurrentBytes int
	LogicalBytes int
	WinnerName   string
	WinnerBytes  int
	Heat         string
	Candidates   []CandidateDiagnostic
}

func (s *Store) CandidateDiagnostics(key string) (CandidateDiagnosticReport, bool) {
	currentName, rawBytes, currentBytes, found := s.Encoding(key)
	if !found {
		return CandidateDiagnosticReport{}, false
	}

	candidate, ok := s.Candidate(key, rawBytes)
	if !ok {
		return CandidateDiagnosticReport{}, false
	}

	report := CandidateDiagnosticReport{
		CurrentName:  currentName,
		CurrentBytes: currentBytes,
		LogicalBytes: rawBytes,
		Heat:         candidate.Heat,
		Candidates: []CandidateDiagnostic{
			{
				Name:     "raw",
				Bytes:    len(candidate.Value),
				Eligible: true,
				Reason:   "always available",
			},
		},
	}

	// Scalar/specialized representation winner, if one applies.
	scalar := s.codecs.Encode(candidate.Value)
	if scalar.ID != codec.Raw {
		report.Candidates = append(report.Candidates, CandidateDiagnostic{
			Name:     s.codecs.Name(scalar.ID),
			Bytes:    len(scalar.Data),
			Eligible: true,
			Reason:   "specialized lossless representation",
		})
	}

	// JSON shape candidate.
	if structuredJSONCandidate(candidate.Value) {
		sh := s.shardFor(candidate.Key)
		shapes := s.ensureShapeStore(sh)

		if shapes != nil {
			schema, slots, shapeOK := shapes.Candidate(candidate.Value)
			if shapeOK {
				data := shapes.EncodeSlots(slots)

				decoded, err := jsonshape.Decode(
					schema,
					data,
					len(candidate.Value),
				)

				if err == nil && bytes.Equal(decoded, candidate.Value) {
					report.Candidates = append(
						report.Candidates,
						CandidateDiagnostic{
							Name:     "json-shape",
							Bytes:    len(data),
							Eligible: true,
							Reason:   "valid shared JSON shape",
						},
					)
				}
			}
		}
	}

	// Physical compression candidates.
	for _, c := range s.codecs.CompressionCandidates(candidate.Value) {
		eligible := s.compression
		reason := "compression enabled"

		if !s.compression {
			eligible = false
			reason = "compression disabled"
		} else if candidate.Heat == "hot" {
			eligible = false
			reason = "hot values are not compressed"
		} else if candidate.Heat == "write-heavy" {
			eligible = false
			reason = "write-heavy values are not compressed"
		} else if c.ID == codec.Zstandard && candidate.Heat != "cold" {
			eligible = false
			reason = "zstd is cold-only"
		}

		report.Candidates = append(
			report.Candidates,
			CandidateDiagnostic{
				Name:     c.Name,
				Bytes:    c.Bytes,
				Eligible: eligible,
				Reason:   reason,
			},
		)
	}

	// Use the real optimizer selection path for the winner so diagnostics
	// cannot disagree with production behavior.
	winner := s.EncodeCandidate(candidate)

	report.WinnerName = s.codecs.Name(winner.ID)
	report.WinnerBytes = len(winner.Data)

	return report, true
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
	e, ok := sh.get(candidate.Key)
	if !ok ||
		e.expired(s.now()) ||
		e.version != candidate.Version ||
		len(record.Data) >= len(sh.encoded(e)) {
		return false
	}

	if !bytes.Equal(s.decode(sh, e), candidate.Value) {
		return false
	}

	prepared := preparedEntry{
		entry: e,
		data:  bytes.Clone(record.Data),
	}

	prepared.codecID = record.ID
	prepared.schema = record.Schema
	prepared.rawLength = uint32(record.RawLength)
	prepared.lastRewrite = stampOf(s.now())

	return s.publish(sh, candidate.Key, prepared) == nil
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
