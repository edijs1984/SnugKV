package server

import (
	"testing"

	"snugkv/internal/engine"
)

func TestRedisPCallArityErrorMatchesRedis(t *testing.T) {
	s := New(engine.New())
	got := execute(t, s, "EVAL", "local r=redis.pcall('SET','only-key'); return r.err", "0")
	want := "$58\r\nERR Wrong number of args calling Redis command from script\r\n"
	if got != want {
		t.Fatalf("redis.pcall arity error = %q, want %q", got, want)
	}
}
