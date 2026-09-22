package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func newSearchTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := engine.NewWithShards(4)
	if err != nil {
		t.Fatal(err)
	}
	return New(store)
}

func TestFTCreateListDrop(t *testing.T) {
	s := newSearchTestServer(t)

	reply, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("products"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("PREFIX"),
		[]byte("1"),
		[]byte("product:"),
		[]byte("SCHEMA"),
		[]byte("$.category"),
		[]byte("AS"),
		[]byte("category"),
		[]byte("TAG"),
		[]byte("$.price"),
		[]byte("AS"),
		[]byte("price"),
		[]byte("NUMERIC"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("reply=%q", reply)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT._LIST"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "products") {
		t.Fatalf("FT._LIST=%q", reply)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT.DROPINDEX"),
		[]byte("products"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("drop reply=%q", reply)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT._LIST"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "*0\r\n" {
		t.Fatalf("FT._LIST after drop=%q", reply)
	}
}

func TestFTCreateBackfillsExistingJSON(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("JSON.SET"),
		[]byte("product:1"),
		[]byte("$"),
		[]byte(`{"category":"books","price":12}`),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("products"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("PREFIX"),
		[]byte("1"),
		[]byte("product:"),
		[]byte("SCHEMA"),
		[]byte("$.category"),
		[]byte("AS"),
		[]byte("category"),
		[]byte("TAG"),
	}); err != nil {
		t.Fatal(err)
	}

	keys, ok := s.store.SearchTagKeys("products", "category", "books")
	if !ok || len(keys) != 1 || keys[0] != "product:1" {
		t.Fatalf("backfill keys=%v ok=%v", keys, ok)
	}
}

func TestFTCreateValidation(t *testing.T) {
	s := newSearchTestServer(t)

	tests := []struct {
		name string
		args [][]byte
	}{
		{
			name: "requires ON JSON",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("SCHEMA"),
				[]byte("$.x"), []byte("AS"), []byte("x"), []byte("TAG"),
			},
		},
		{
			name: "rejects unsupported ON type",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("HASH"),
				[]byte("SCHEMA"),
				[]byte("$.x"), []byte("AS"), []byte("x"), []byte("TAG"),
			},
		},
		{
			name: "requires AS",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("JSON"),
				[]byte("SCHEMA"),
				[]byte("$.x"), []byte("x"), []byte("TAG"),
			},
		},
		{
			name: "rejects unsupported field",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("JSON"),
				[]byte("SCHEMA"),
				[]byte("$.x"), []byte("AS"), []byte("x"), []byte("TEXT"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Execute(tc.args); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFTCreateRejectsDuplicateIndexAndAlias(t *testing.T) {
	s := newSearchTestServer(t)

	create := [][]byte{
		[]byte("FT.CREATE"), []byte("products"),
		[]byte("ON"), []byte("JSON"),
		[]byte("SCHEMA"),
		[]byte("$.category"), []byte("AS"), []byte("category"), []byte("TAG"),
	}

	if _, err := s.Execute(create); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(create); err == nil {
		t.Fatal("duplicate index unexpectedly succeeded")
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("bad"),
		[]byte("ON"), []byte("JSON"),
		[]byte("SCHEMA"),
		[]byte("$.a"), []byte("AS"), []byte("same"), []byte("TAG"),
		[]byte("$.b"), []byte("AS"), []byte("same"), []byte("NUMERIC"),
	}); err == nil {
		t.Fatal("duplicate alias unexpectedly succeeded")
	}
}

func TestFTCommandsExposeNoRedisKeys(t *testing.T) {
	for _, args := range [][][]byte{
		{
			[]byte("FT.CREATE"), []byte("idx"),
			[]byte("ON"), []byte("JSON"),
			[]byte("SCHEMA"),
			[]byte("$.x"), []byte("AS"), []byte("x"), []byte("TAG"),
		},
		{
			[]byte("FT.DROPINDEX"), []byte("idx"),
		},
		{
			[]byte("FT._LIST"),
		},
	} {
		refs, err := commandKeys(args)
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) != 0 {
			t.Fatalf("%s unexpectedly exposed keys: %#v", args[0], refs)
		}
	}
}

func TestACLSearchCategoryIncludesImplementedFTCommands(t *testing.T) {
	commands, ok := aclCommandsForCategory("search")
	if !ok {
		t.Fatal("search category missing")
	}

	have := make(map[string]bool, len(commands))
	for _, command := range commands {
		have[command] = true
	}

	for _, command := range []string{
		"ft.create",
		"ft.dropindex",
		"ft._list",
	} {
		if !have[command] {
			t.Errorf("%s missing from @search", command)
		}
	}
}
