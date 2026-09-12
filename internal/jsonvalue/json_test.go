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