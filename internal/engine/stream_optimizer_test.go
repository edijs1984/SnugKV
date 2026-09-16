package engine

import (
	"testing"
	"time"
)

func TestStreamIsNeverGenericOptimizerCandidate(t *testing.T) {
	s, err := NewWithOptions(Options{
		Shards:      256,
		Encoding:    true,
		Compression: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.StreamAdd(
		"events",
		"1-0",
		[]StreamField{{Field: []byte("v"), Value: []byte("one")}},
		StreamAddOptions{},
	); err != nil {
		t.Fatal(err)
	}

	if _, ok := s.OptimizationEligible("events", 0, 0); ok {
		t.Fatal("stream must not be eligible for the generic optimizer")
	}
	if _, ok := s.Candidate("events", 1<<20); ok {
		t.Fatal("stream must not be exposed as a generic rewrite candidate")
	}

	// Optimizer cooldown passage must not change STREAM eligibility.
	s.now = func() time.Time { return time.Unix(3600, 0) }
	if _, ok := s.OptimizationEligible("events", 0, 0); ok {
		t.Fatal("stream became optimizer-eligible after cooldown")
	}
}
