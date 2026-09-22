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

	for _, path := range []string{"", "a", "$.", "$..a", "$[", "$.a[", "$.a[]", "$.a[x]"} {
		if _, _, err := Get(root, path); err == nil {
			t.Fatalf("accepted invalid path %q", path)
		}
	}
}
