package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"testing"
)

func TestRealisticBenchmarkValueProfiles(t *testing.T) {
	for _, tc := range []struct {
		shape string
		size  int
	}{
		{"session-json", 384},
		{"api-json", 768},
		{"text", 256},
		{"compressed", 256},
	} {
		got := benchmarkValue(tc.shape, tc.size, 42, 1)
		if len(got) != tc.size {
			t.Fatalf("%s len=%d want=%d", tc.shape, len(got), tc.size)
		}
	}
}

func TestRealisticJSONProfilesAreValid(t *testing.T) {
	for _, tc := range []struct {
		shape string
		size  int
	}{
		{"session-json", 384},
		{"api-json", 768},
	} {
		got := benchmarkValue(tc.shape, tc.size, 42, 1)
		if !json.Valid(got) {
			t.Fatalf("%s produced invalid JSON: %q", tc.shape, got)
		}
	}
}

func TestCounterProfileIsCanonicalTenByteInteger(t *testing.T) {
	got := benchmarkValue("counter", 10, 42, 1)
	if len(got) != 10 {
		t.Fatalf("len=%d want=10", len(got))
	}
	if string(got) != "1000000042" {
		t.Fatalf("counter=%q", got)
	}
	if _, err := strconv.ParseInt(string(got), 10, 64); err != nil {
		t.Fatalf("counter is not integer: %v", err)
	}
}

func TestUUIDProfileIsCanonical(t *testing.T) {
	got := benchmarkValue("uuid", 36, 42, 1)
	if len(got) != 36 {
		t.Fatalf("len=%d want=36", len(got))
	}
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if !re.Match(got) {
		t.Fatalf("uuid=%q", got)
	}
}

func TestCompressedProfileHasGzipSignature(t *testing.T) {
	got := benchmarkValue("compressed", 256, 42, 1)
	if got[0] != 0x1f || got[1] != 0x8b {
		t.Fatalf("signature=%x %x", got[0], got[1])
	}
}
