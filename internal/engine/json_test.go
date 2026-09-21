package engine

import (
	"testing"
	"time"
)

func TestJSONPathGetTypeAndInspectors(t *testing.T) {
	s := New()
	if _, err := s.JSONSet("doc", "$", []byte(`{
		"user":{"name":"Edijs","roles":["admin","dev"]},
		"items":[{"price":10},{"price":20}],
		"nested":{"price":30}
	}`), false, false); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.JSONGet("doc", "$.items[*].price")
	if err != nil || !found {
		t.Fatalf("JSONGet: found=%v err=%v", found, err)
	}
	if string(got) != "[10,20]" {
		t.Fatalf("JSONGet = %s", got)
	}

	types, exists, err := s.JSONTypes("doc", "$..price")
	if err != nil || !exists {
		t.Fatalf("JSONTypes: exists=%v err=%v", exists, err)
	}
	if len(types) != 3 {
		t.Fatalf("JSONTypes len=%d want=3 (%v)", len(types), types)
	}
	for _, typ := range types {
		if typ != "integer" {
			t.Fatalf("unexpected type %q", typ)
		}
	}

	lengths, exists, err := s.JSONArrLen("doc", "$.user.roles")
	if err != nil || !exists || len(lengths) != 1 || !lengths[0].Valid || lengths[0].Value != 2 {
		t.Fatalf("JSONArrLen: %#v exists=%v err=%v", lengths, exists, err)
	}

	lengths, exists, err = s.JSONStrLen("doc", "$.user.name")
	if err != nil || !exists || len(lengths) != 1 || !lengths[0].Valid || lengths[0].Value != 5 {
		t.Fatalf("JSONStrLen: %#v exists=%v err=%v", lengths, exists, err)
	}

	keys, exists, err := s.JSONObjKeys("doc", "$.user")
	if err != nil || !exists || len(keys) != 1 || !keys[0].Valid {
		t.Fatalf("JSONObjKeys: %#v exists=%v err=%v", keys, exists, err)
	}
	if len(keys[0].Values) != 2 || keys[0].Values[0] != "name" || keys[0].Values[1] != "roles" {
		t.Fatalf("JSONObjKeys = %#v", keys[0].Values)
	}
}

func TestJSONArraySetAndDeletePreserveTTL(t *testing.T) {
	s := New()
	if _, err := s.JSONSet("doc", "$", []byte(`{"items":[1,2,3]}`), false, false); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("doc", time.Minute) {
		t.Fatal("expire failed")
	}

	before := s.TTL("doc", true)
	if before <= 0 {
		t.Fatalf("unexpected TTL %d", before)
	}

	if _, err := s.JSONSet("doc", "$.items[-1]", []byte("9"), false, false); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.JSONDel("doc", "$.items[1]"); err != nil || deleted != 1 {
		t.Fatalf("JSONDel deleted=%d err=%v", deleted, err)
	}

	got, found, err := s.JSONGet("doc", "$.items")
	if err != nil || !found || string(got) != "[[1,9]]" {
		t.Fatalf("JSONGet items=%s found=%v err=%v", got, found, err)
	}
	if ttl := s.TTL("doc", true); ttl <= 0 {
		t.Fatalf("TTL lost: %d", ttl)
	}
}
