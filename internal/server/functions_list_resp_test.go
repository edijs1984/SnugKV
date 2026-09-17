package server

import (
	"testing"

	"snugkv/internal/engine"
)

func TestFunctionListFlagsUseSimpleStrings(t *testing.T) {
	s := New(engine.New())
	code := "#!lua name=flags\nredis.register_function{function_name='reader',callback=function(keys,args) return 1 end,flags={'no-writes'}}"
	loadFunctionLibrary(t, s, code)

	got := execute(t, s, "FUNCTION", "LIST", "LIBRARYNAME", "flags")
	want := "*1\r\n*6\r\n$12\r\nlibrary_name\r\n$5\r\nflags\r\n$6\r\nengine\r\n$3\r\nLUA\r\n$9\r\nfunctions\r\n*1\r\n*6\r\n$4\r\nname\r\n$6\r\nreader\r\n$11\r\ndescription\r\n$-1\r\n$5\r\nflags\r\n*1\r\n+no-writes\r\n"
	if got != want {
		t.Fatalf("FUNCTION LIST flag encoding = %q, want %q", got, want)
	}
}
