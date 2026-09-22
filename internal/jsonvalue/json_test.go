package jsonvalue

import (
	"reflect"
	"testing"
)

func TestGetNested(t *testing.T) {
	root, err := Parse([]byte(`{
		"user": {
			"name": "Edijs"
		}
	}`))

	if err != nil {
		t.Fatal(err)
	}

	value, found, err := Get(root, "$.user.name")

	if err != nil {
		t.Fatal(err)
	}

	if !found {
		t.Fatal("path not found")
	}

	if value != "Edijs" {
		t.Fatalf("got %#v", value)
	}
}

func TestSetNested(t *testing.T) {
	root, err := Parse([]byte(`{
		"user": {
			"name": "old"
		}
	}`))

	if err != nil {
		t.Fatal(err)
	}

	root, err = Set(root, "$.user.name", "Edijs")
	if err != nil {
		t.Fatal(err)
	}

	value, found, err := Get(root, "$.user.name")

	if err != nil || !found {
		t.Fatal("path not found")
	}

	if value != "Edijs" {
		t.Fatalf("got %#v", value)
	}
}


func TestExactJSONPathArraysAndBracketMembers(t *testing.T) {
	root, err := Parse([]byte(`{"user":{"name":"Edijs","weird.key":"dot"},"items":[{"name":"a"},{"name":"b"},3]}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path string
		want any
	}{
		{"$.items[0].name", "a"},
		{"$.items[-1]", float64(3)},
		{`$["user"]["name"]`, "Edijs"},
		{`$["user"]["weird.key"]`, "dot"},
	} {
		got, found, err := Get(root, tc.path)
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", tc.path, found, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestExactJSONPathSetAndDeleteArray(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"name":"a"},{"name":"b"},{"name":"c"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, err = Set(root, "$.items[1].name", "Bee")
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := Get(root, "$.items[1].name")
	if err != nil || !found || value != "Bee" {
		t.Fatalf("set result: %#v %v %v", value, found, err)
	}

	root, deleted, err := Delete(root, "$.items[1]")
	if err != nil || !deleted {
		t.Fatalf("delete: %v %v", deleted, err)
	}
	encoded, err := Encode(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"items":[{"name":"a"},{"name":"c"}]}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestExactJSONPathInvalidSyntax(t *testing.T) {
	root, err := Parse([]byte(`{"a":[1]}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"", "$.", "$..", "$...[a]", "$[", "$.a[", "$.a[]", "$.a[x]"} {
		if _, _, err := Get(root, path); err == nil {
			t.Fatalf("accepted invalid path %q", path)
		}
	}
}


func TestJSONPathWildcardMatchesAndMutates(t *testing.T) {
	root, err := Parse([]byte(`{"items":[{"score":1},{"score":2},{"score":3}],"obj":{"a":1,"b":2}}`))
	if err != nil {
		t.Fatal(err)
	}

	values, err := Matches(root, "$.items[*].score")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(1), float64(2), float64(3)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("matches got %#v want %#v", values, want)
	}

	root, count, err := SetMatches(root, "$.items[*].score", float64(9))
	if err != nil || count != 3 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	values, err = Matches(root, "$.items[*].score")
	if err != nil {
		t.Fatal(err)
	}
	want = []any{float64(9), float64(9), float64(9)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("updated matches got %#v want %#v", values, want)
	}

	root, count, err = DeleteMatches(root, "$.obj.*")
	if err != nil || count != 2 {
		t.Fatalf("delete count=%d err=%v", count, err)
	}
	value, found, err := Get(root, ".obj")
	if err != nil || !found {
		t.Fatalf("legacy get found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(value, map[string]any{}) {
		t.Fatalf("object after wildcard delete: %#v", value)
	}
}

func TestLegacyPathWithoutLeadingDot(t *testing.T) {
	root, err := Parse([]byte(`{"a":{"b":2}}`))
	if err != nil {
		t.Fatal(err)
	}

	value, found, err := Get(root, "a.b")
	if err != nil || !found || value != float64(2) {
		t.Fatalf("got %#v found=%v err=%v", value, found, err)
	}
}


func TestJSONPathRecursiveDescent(t *testing.T) {
	root, err := Parse([]byte(`{
		"name":"root",
		"nested":{"name":"child","deep":{"name":"leaf"}},
		"users":[{"id":1,"profile":{"id":10}},{"id":2}]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	values, err := Matches(root, "$..name")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"root", "child", "leaf"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("$..name got %#v want %#v", values, want)
	}

	values, err = Matches(root, "$.users..id")
	if err != nil {
		t.Fatal(err)
	}
	want = []any{float64(1), float64(10), float64(2)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("$.users..id got %#v want %#v", values, want)
	}
}

func TestJSONPathRecursiveSetAndDelete(t *testing.T) {
	root, err := Parse([]byte(`{
		"a":{"score":1,"nested":{"score":2}},
		"b":[{"score":3},{"x":1}]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$..score", float64(9))
	if err != nil || count != 3 {
		t.Fatalf("set count=%d err=%v", count, err)
	}

	values, err := Matches(root, "$..score")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(9), float64(9), float64(9)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("scores got %#v want %#v", values, want)
	}

	root, count, err = DeleteMatches(root, "$..score")
	if err != nil || count != 3 {
		t.Fatalf("delete count=%d err=%v", count, err)
	}

	values, err = Matches(root, "$..score")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatalf("scores remain: %#v", values)
	}
}


func TestJSONPathSlicesAndUnions(t *testing.T) {
	root, err := Parse([]byte(`{"items":[0,1,2,3,4,5]}`))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want []any
	}{
		{"$.items[1:4]", []any{float64(1), float64(2), float64(3)}},
		{"$.items[:3]", []any{float64(0), float64(1), float64(2)}},
		{"$.items[::2]", []any{float64(0), float64(2), float64(4)}},
		{"$.items[-3:]", []any{float64(3), float64(4), float64(5)}},
		{"$.items[0,2,4]", []any{float64(0), float64(2), float64(4)}},
		{"$.items[-1,0,-1]", []any{float64(5), float64(0)}},
	}

	for _, tc := range cases {
		got, err := Matches(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s got %#v want %#v", tc.path, got, tc.want)
		}
	}
}

func TestJSONPathSliceAndUnionMutation(t *testing.T) {
	root, err := Parse([]byte(`{"items":[0,1,2,3,4,5]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, count, err := SetMatches(root, "$.items[1:5:2]", float64(9))
	if err != nil || count != 2 {
		t.Fatalf("set count=%d err=%v", count, err)
	}
	values, err := Matches(root, "$.items[*]")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(0), float64(9), float64(2), float64(9), float64(4), float64(5)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("after set got %#v want %#v", values, want)
	}

	root, count, err = DeleteMatches(root, "$.items[0,2,4]")
	if err != nil || count != 3 {
		t.Fatalf("delete count=%d err=%v", count, err)
	}
	values, err = Matches(root, "$.items[*]")
	if err != nil {
		t.Fatal(err)
	}
	want = []any{float64(9), float64(9), float64(5)}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("after delete got %#v want %#v", values, want)
	}
}

func TestJSONPathSliceRejectsZeroStep(t *testing.T) {
	root, err := Parse([]byte(`{"items":[0,1,2]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Matches(root, "$.items[::0]"); err == nil {
		t.Fatal("expected zero-step slice to fail")
	}
}
