package jsonshape

import "testing"

func TestLookupDoesNotTrainShape(t *testing.T) {
	store := New(16<<10, 3)
	value := []byte(`{"id":123,"enabled":true,"name":"test"}`)

	for i := 0; i < 10; i++ {
		if _, _, ok := store.Lookup(value); ok {
			t.Fatal("Lookup unexpectedly admitted schema")
		}
	}

	// Two real observations are not enough.
	for i := 0; i < 2; i++ {
		if _, _, ok := store.Candidate(value); ok {
			t.Fatal("schema admitted before threshold")
		}
	}

	if _, _, ok := store.Lookup(value); ok {
		t.Fatal("Lookup changed admission state")
	}

	// Third real observation admits it.
	if _, _, ok := store.Candidate(value); !ok {
		t.Fatal("schema was not admitted at threshold")
	}

	if _, _, ok := store.Lookup(value); !ok {
		t.Fatal("admitted schema not visible through Lookup")
	}
}
