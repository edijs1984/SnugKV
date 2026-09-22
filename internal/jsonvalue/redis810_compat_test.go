package jsonvalue

import (
	"reflect"
	"testing"
)

func TestRedis810UnionPreservesDuplicates(t *testing.T) {
	root, err := Parse([]byte(`{"nums":[0,1,2,3,4]}`))
	if err != nil {
		t.Fatal(err)
	}

	got, err := Matches(root, "$.nums[0,0,2]")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{float64(0), float64(0), float64(2)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestRedis810RejectsNegativeSliceStep(t *testing.T) {
	root, err := Parse([]byte(`{"nums":[0,1,2,3]}`))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Matches(root, "$.nums[::-1]"); err == nil {
		t.Fatal("expected negative slice step to be rejected")
	}
}
