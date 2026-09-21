package engine

import (
	"bytes"
	"fmt"
	"snugkv/internal/codec"
	"snugkv/internal/codec/jsonshape"
	"sync/atomic"
	"time"
)

type Candidate struct {
	Key                     string
	Version                 uint64
	Value                   []byte
	EncodedBytes            int
	AdditionalMetadataBytes int
	LastRewrite             time.Time
	LastWrite               time.Time
	Heat                    string
	expiresAt               stamp
}

func (c Candidate) RequiresOptimizationMetadata() bool {
	return structuredJSONCandidate(c.Value)
}

// OptimizationEligible performs the cheap read-only eligibility check before
// the optimizer reserves scratch/bandwidth resources. Native container values
// have their own packed representations and are intentionally excluded from
// the generic scalar/compression optimizer.
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
	if !ok || sh.expired(key, e, now) || isNativeContainerType(e.valueType) {
		return 0, false
	}

	// LZ4/Zstd records are terminal for the normal background pass. Keeping
	// compression rewrites metadata-free avoids a permanent 24-byte sidecar
	// per compressed key. A subsequent foreground write publishes a fresh
	// representation and may enqueue the key again.
	if (e.codecID == codec.LZ4 || e.codecID == codec.Zstandard) && e.entryMeta == nil {
		return 0, false
	}

	meta := e.entryMeta
	if heat(meta, now) == "write-heavy" {
		return 0, false
	}

	if meta != nil && !meta.lastRewrite.IsZero() &&
		now.Sub(meta.lastRewrite.Time()) < rewriteInterval {
		return 0, false
	}

	if meta != nil && !meta.lastOptimize.IsZero() &&
		now.Sub(meta.lastOptimize.Time()) < attemptInterval {
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
	if !ok || sh.expired(key, e, now) || isNativeContainerType(e.valueType) {
		return false
	}

	if (e.codecID == codec.LZ4 || e.codecID == codec.Zstandard) && e.entryMeta == nil {
		return false
	}

	meta := e.entryMeta
	if heat(meta, now) == "write-heavy" {
		return false
	}

	if meta != nil && !meta.lastRewrite.IsZero() &&
		now.Sub(meta.lastRewrite.Time()) < rewriteInterval {
		return false
	}

	if meta != nil && !meta.lastOptimize.IsZero() &&
		now.Sub(meta.lastOptimize.Time()) < attemptInterval {
		return false
	}

	if meta == nil {
		// Keep metadata sparse. A successful Rewrite will allocate and account
		// metadata as part of the published replacement.
		return true
	}
	meta.lastOptimize = activityStampOf(now)
	sh.set(key, e)

	return true
}

func (s *Store) Candidate(key string, maxBytes int) (Candidate, bool) {
	return s.CandidateInto(key, maxBytes, nil)
}

// CandidateInto snapshots a candidate into caller-owned scratch when possible.
// The returned Value remains valid until the caller reuses or mutates dst.
// Candidate remains the ownership-preserving convenience wrapper for callers
// that do not provide scratch.
func (s *Store) CandidateInto(key string, maxBytes int, dst []byte) (Candidate, bool) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) || isNativeContainerType(e.valueType) || int(e.rawLength) > maxBytes {
		return Candidate{}, false
	}
	meta := e.entryMeta
	var lastRewrite, lastWrite time.Time
	additionalMetadataBytes := 0
	if meta != nil {
		lastRewrite = meta.lastRewrite.Time()
		lastWrite = meta.lastWrite.Time()
	} else {
		additionalMetadataBytes = int(entryMetaBytes)
	}
	return Candidate{
		Key:                     key,
		Version:                 e.ref.Generation(),
		Value:                   s.decodeInto(sh, e, dst),
		EncodedBytes:            len(sh.encoded(e)),
		AdditionalMetadataBytes: additionalMetadataBytes,
		LastRewrite:             lastRewrite,
		LastWrite:               lastWrite,
		Heat:                    heat(meta, s.now()),
		expiresAt:               sh.expirationAt(key, e),
	}, true
}
func heat(meta *entryMeta, now time.Time) string {
	if meta == nil {
		return "warm"
	}
	if now.Sub(meta.lastWrite.Time()) < time.Minute && meta.writes >= 10 {
		return "write-heavy"
	}
	if now.Sub(meta.lastAccess.Time()) < time.Minute && meta.reads >= 100 {
		return "hot"
	}
	if now.Sub(meta.lastAccess.Time()) > 5*time.Minute && now.Sub(meta.lastWrite.Time()) > 5*time.Minute {
		return "cold"
	}
	return "warm"
}
func (s *Store) Policy(key string) (string, bool) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return "", false
	}
	if !s.encoding {
		return "disabled", true
	}
	switch e.valueType {
	case TypeHash:
		return "hash-native", true
	case TypeSet:
		return "set-native", true
	}
	return heat(e.entryMeta, s.now()), true
}
func (s *Store) ensureShapeStoreLocked(sh *shard) *jsonshape.Store {
	if !s.shapeEncoding {
		return nil
	}

	if sh.shapes != nil {
		return sh.shapes
	}

	shapes := s.ensureGlobalShapeStore()
	if shapes == nil {
		return nil
	}

	// Alias only. Ownership belongs to Store.shapeCatalog.
	sh.shapes = shapes

	return shapes
}

func (s *Store) ensureShapeStore(sh *shard) *jsonshape.Store {
	if !s.shapeEncoding {
		return nil
	}

	sh.mu.Lock()
	defer sh.mu.Unlock()

	return s.ensureShapeStoreLocked(sh)
}

type OptimizationClass uint8

const (
	OptimizationNone OptimizationClass = iota
	OptimizationJSON
	OptimizationCompress
)

// OptimizationClassForValue performs the cheapest possible write-time
// classification. Specialized scalar codecs (integer/UUID/timestamp/float/bool)
// already run synchronously in makeEntry, so they never need background work.
// Native containers bypass this path entirely.
func (s *Store) OptimizationClassForValue(value []byte) OptimizationClass {
	if !s.encoding {
		return OptimizationNone
	}

	if s.shapeEncoding && structuredJSONCandidate(value) {
		return OptimizationJSON
	}

	if s.compression && len(value) >= 256 && !codec.AlreadyCompressed(value) {
		return OptimizationCompress
	}

	return OptimizationNone
}

// ShouldQueueOptimization is the write-time admission gate used by SET paths.
func (s *Store) ShouldQueueOptimization(value []byte) bool {
	return s.OptimizationClassForValue(value) != OptimizationNone
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

// ObserveJSONShape records a real write observation.
//
// Schema admission should be driven by actual writes, not optimizer retries or
// diagnostic commands. Repeated same-shape JSON values therefore mature the
// shape store naturally as they are written.
func (s *Store) observeJSONShapeLocked(sh *shard, value []byte) {
	if !s.shapeEncoding || !structuredJSONCandidate(value) {
		return
	}

	shapes := s.ensureShapeStoreLocked(sh)
	if shapes == nil {
		return
	}

	// Candidate() is intentionally the mutating admission path.
	// Real writes train the schema store.
	_, _, _ = shapes.Candidate(value)
}

// ObserveJSONShape is the externally safe form for callers that do not
// already hold the shard lock.
func (s *Store) ObserveJSONShape(key string, value []byte) {
	if !s.shapeEncoding || !structuredJSONCandidate(value) {
		return
	}

	sh := s.shardFor(key)

	sh.mu.Lock()
	defer sh.mu.Unlock()

	s.observeJSONShapeLocked(sh, value)
}

// JSONShapeWarmupPending reports whether a recently-written structured JSON
// value is still waiting for its schema to be admitted.
func (s *Store) JSONShapeWarmupPending(candidate Candidate, window time.Duration) bool {
	if !s.shapeEncoding ||
		!structuredJSONCandidate(candidate.Value) ||
		candidate.LastWrite.IsZero() ||
		time.Since(candidate.LastWrite) >= window {
		return false
	}

	sh := s.shardFor(candidate.Key)
	shapes := s.ensureShapeStore(sh)
	if shapes == nil {
		return false
	}

	_, _, ready := shapes.Lookup(candidate.Value)
	return !ready
}

func (s *Store) EncodeCandidate(candidate Candidate) codec.Record {
	// Candidate owns an immutable snapshot already. Values longer than 36 bytes
	// cannot use a synchronous scalar codec, so borrow that snapshot as the raw
	// fallback instead of cloning it again.
	best := codec.Record{ID: codec.Raw, RawLength: len(candidate.Value), Data: candidate.Value}
	if len(candidate.Value) <= 36 {
		best = s.codecs.Encode(candidate.Value)
	}
	sh := s.shardFor(candidate.Key)
	if structuredJSONCandidate(candidate.Value) {
		shapes := s.ensureShapeStore(sh)
		if shapes != nil {
			schema, slots, ok := shapes.Lookup(candidate.Value)
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
		compressed := s.codecs.EncodeGeneralBorrowed(candidate.Value, candidate.Heat == "cold")
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
			status := shapes.Admission(candidate.Value)
			schema, slots, shapeOK := shapes.Lookup(candidate.Value)

			if !shapeOK {
				report.Candidates = append(report.Candidates, CandidateDiagnostic{
					Name:     "json-shape",
					Bytes:    0,
					Eligible: false,
					Reason: fmt.Sprintf(
						"not admitted observed:%d threshold:%d schemas:%d used:%d",
						status.Observed,
						status.Threshold,
						status.SchemaCount,
						status.UsedBytes,
					),
				})
			}

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

	winner := s.EncodeCandidate(candidate)

	report.WinnerName = s.codecs.Name(winner.ID)
	report.WinnerBytes = len(winner.Data)

	return report, true
}

// Rewrite commits only the exact arena generation and TTL observed. It verifies
// logical bytes, keeps access metadata, and rejects stale optimizer jobs.
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
		sh.expired(candidate.Key, e, s.now()) ||
		isNativeContainerType(e.valueType) ||
		e.ref.Generation() != candidate.Version ||
		sh.expirationAt(candidate.Key, e) != candidate.expiresAt ||
		len(record.Data) >= len(sh.encoded(e)) {
		return false
	}

	if !bytes.Equal(s.decode(sh, e), candidate.Value) {
		return false
	}

	prepared := preparedEntry{
		entry:     e,
		expiresAt: candidate.expiresAt,
		data:      bytes.Clone(record.Data),
	}
	prepared.entryMeta = cloneEntryMeta(e.entryMeta)
	prepared.codecID = record.ID

	// JSON values retain optimizer metadata because an early compression
	// rewrite may later be replaced by an admitted shared JSON shape. Plain
	// non-JSON compression is terminal for the normal optimizer pass, so it
	// does not need the 24-byte sidecar.
	if record.Schema != nil || candidate.RequiresOptimizationMetadata() {
		meta := prepared.ensureMeta()
		if record.Schema != nil {
			meta.schemaID = record.Schema.ID
		} else {
			meta.schemaID = 0
		}
		meta.lastRewrite = activityStampOf(s.now())
	} else if prepared.entryMeta != nil {
		prepared.entryMeta.schemaID = 0
		prepared.entryMeta.lastRewrite = activityStampOf(s.now())
	}

	prepared.rawLength = uint32(record.RawLength)

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
		if n > 16 {
			n = 16
		}
		keys, next := sh.data.Sample(sh.sampleOffset, 64, n)
		sh.sampleOffset = next
		out = append(out, keys...)
		sh.mu.Unlock()

	}
	return out
}
