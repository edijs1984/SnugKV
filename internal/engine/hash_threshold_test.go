package engine

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

// TestHashAcrossIndexThreshold drives one hash well past the point where it
// switches from the packed layout to the indexed layout, mixing single-field
// and batched writes, overwrites and deletes, and checks it against a map.
func TestHashAcrossIndexThreshold(t *testing.T) {
	store := New()
	rng := rand.New(rand.NewSource(7))
	model := map[string]string{}
	const key = "threshold"
	check := func(step int) {
		t.Helper()
		if n, err := store.HashLen(key); err != nil || int(n) != len(model) {
			t.Fatalf("step %d: len=%d err=%v want %d", step, n, err, len(model))
		}
		all, err := store.HashGetAll(key)
		if err != nil || len(all) != len(model) {
			t.Fatalf("step %d: getall len=%d err=%v want %d", step, len(all), err, len(model))
		}
		for _, p := range all {
			if model[string(p.Field)] != string(p.Value) {
				t.Fatalf("step %d: field %q mismatch", step, p.Field)
			}
		}
	}
	for step := 0; step < 3000; step++ {
		f := []byte(fmt.Sprintf("f:%04d", rng.Intn(400)))
		switch op := rng.Intn(10); {
		case op < 6:
			v := []byte(fmt.Sprintf("v%d-%d", step, rng.Intn(1000)))
			if _, err := store.HashSet(key, [][]byte{f}, [][]byte{v}); err != nil {
				t.Fatal(err)
			}
			model[string(f)] = string(v)
		case op < 8:
			n := 1 + rng.Intn(40)
			fs, vs := make([][]byte, 0, n), make([][]byte, 0, n)
			for i := 0; i < n; i++ {
				ff := []byte(fmt.Sprintf("f:%04d", rng.Intn(400)))
				vv := []byte(fmt.Sprintf("b%d-%d", step, i))
				fs, vs = append(fs, ff), append(vs, vv)
				model[string(ff)] = string(vv)
			}
			if _, err := store.HashSetResults(key, fs, vs); err != nil {
				t.Fatal(err)
			}
		default:
			if _, err := store.HashDel(key, [][]byte{f}); err != nil {
				t.Fatal(err)
			}
			delete(model, string(f))
		}
		if step%50 == 0 || len(model) == 127 || len(model) == 128 || len(model) == 129 {
			if len(model) > 0 {
				check(step)
			}
		}
		if got, found, _ := store.HashGet(key, f); found != (model[string(f)] != "") || (found && !bytes.Equal(got, []byte(model[string(f)]))) {
			t.Fatalf("step %d: HGET %q found=%v", step, f, found)
		}
	}
}
