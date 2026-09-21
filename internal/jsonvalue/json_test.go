package jsonvalue

import "testing"

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


func TestGetAllJSONPathFeatures(t *testing.T) {
	root, err := Parse([]byte(`{
		"user": {"name": "Edijs", "roles": ["admin", "dev"]},
		"items": [{"price": 10}, {"price": 20}],
		"nested": {"price": 30}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path string
		want []any
	}{
		{"$.user.roles[0]", []any{"admin"}},
		{"$.user.roles[-1]", []any{"dev"}},
		{"$['user']['name']", []any{"Edijs"}},
		{"$.items[*].price", []any{float64(10), float64(20)}},
		{"$..price", []any{float64(10), float64(20), float64(30)}},
	}

	for _, tc := range tests {
		got, err := GetAll(root, tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %#v want %#v", tc.path, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s[%d]: got %#v want %#v", tc.path, i, got[i], tc.want[i])
			}
		}
	}
}

func TestSetAndDeleteArrayIndex(t *testing.T) {
	root, err := Parse([]byte(`{"items":[1,2,3]}`))
	if err != nil {
		t.Fatal(err)
	}

	root, err = Set(root, "$.items[-1]", float64(9))
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := Get(root, "$.items[2]")
	if err != nil || !found || value != float64(9) {
		t.Fatalf("set array index: value=%#v found=%v err=%v", value, found, err)
	}

	root, deleted, err := Delete(root, "$.items[1]")
	if err != nil || !deleted {
		t.Fatalf("delete array index: deleted=%v err=%v", deleted, err)
	}
	values, err := GetAll(root, "$.items[*]")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0] != float64(1) || values[1] != float64(9) {
		t.Fatalf("after delete: %#v", values)
	}
}

func TestLegacyAndRootPaths(t *testing.T) {
	root, err := Parse([]byte(`{"a":{"b":1}}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{".a.b", "a.b"} {
		value, found, err := Get(root, path)
		if err != nil || !found || value != float64(1) {
			t.Fatalf("%s: value=%#v found=%v err=%v", path, value, found, err)
		}
	}

	for _, path := range []string{"$", "."} {
		value, found, err := Get(root, path)
		if err != nil || !found || value == nil {
			t.Fatalf("%s root failed: value=%#v found=%v err=%v", path, value, found, err)
		}
	}
}
