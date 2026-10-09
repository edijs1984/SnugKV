package jsonvalue

import (
	"reflect"
	"testing"
)

func TestLocateMatchesMatchesInTheSameOrder(t *testing.T) {
	root, _ := Parse([]byte(`{"a":{"arr":[1,2,{"arr":[9]}]},"b":{"arr":[3]},"c":[{"arr":[4]},{"x":1}]}`))
	for _, path := range []string{"$..arr", "$.*.arr", "$.c[*].arr", "$.c[0:2]", "$..*", "$.a.arr[?(@ > 1)]", "$.c[0,1]", "$.nope"} {
		want, err := Matches(root, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		locs, err := Locate(root, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(locs) != len(want) {
			t.Fatalf("%s: %d locations, %d matches", path, len(locs), len(want))
		}
		for i, loc := range locs {
			got, ok := GetAt(root, loc)
			if !ok || !reflect.DeepEqual(got, want[i]) {
				t.Fatalf("%s #%d: GetAt = %v, %v; want %v", path, i, got, ok, want[i])
			}
		}
	}
}

func TestSetAtReplacesOnlyTheLocation(t *testing.T) {
	root, _ := Parse([]byte(`{"a":[1,2],"b":{"c":3}}`))
	locs, _ := Locate(root, "$.b.c")
	updated, ok := SetAt(root, locs[0], float64(7))
	if !ok {
		t.Fatal("SetAt failed")
	}
	got, _ := Encode(updated)
	if string(got) != `{"a":[1,2],"b":{"c":7}}` {
		t.Fatalf("got %s", got)
	}
}
