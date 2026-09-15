package engine

import "testing"

func TestWritesTrainJSONShapeAdmission(t *testing.T) {
	store, err := NewWithOptions(Options{
		Shards:        1,
		Encoding:      true,
		ShapeEncoding: true,
		Compression:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	value := []byte(`{"id":123,"enabled":true,"name":"same-shape"}`)

	// Threshold is eight observations.
	for i := 0; i < 8; i++ {
		key := string(rune('a' + i))

		applied, _, _, err := store.SetWithOptions(
			key,
			value,
			SetOptions{},
		)
		if err != nil {
			t.Fatal(err)
		}
		if !applied {
			t.Fatal("write was not applied")
		}
	}

	report, ok := store.CandidateDiagnostics("a")
	if !ok {
		t.Fatal("missing candidate report")
	}

	foundShape := false

	for _, candidate := range report.Candidates {
		if candidate.Name == "json-shape" && candidate.Eligible {
			foundShape = true
			break
		}
	}

	if !foundShape {
		t.Fatal("real writes did not admit JSON shape")
	}

	// Diagnostics are read-only: repeated calls should not be required
	// for admission and must not mutate shape state.
	for i := 0; i < 10; i++ {
		if _, ok := store.CandidateDiagnostics("a"); !ok {
			t.Fatal("candidate diagnostics failed")
		}
	}
}
