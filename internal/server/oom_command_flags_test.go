package server

import (
	"errors"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func forceNoEvictionOOM(t *testing.T, s *Server) {
	t.Helper()

	if got := execute(t, s, "SET", "oom:seed", strings.Repeat("x", 4096)); got != "+OK\r\n" {
		t.Fatalf("seed SET = %q", got)
	}

	s.eviction = "noeviction"
	s.store.SetMaxMemory(1)

	memory := s.store.Memory()
	if memory.AccountedBytes <= memory.MaxBytes {
		t.Fatalf(
			"expected pre-existing OOM state: used=%d max=%d",
			memory.AccountedBytes,
			memory.MaxBytes,
		)
	}
}

func TestDenyOOMCommandsRejectedBeforeExecution(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"SET existing", []string{"SET", "oom:seed", "changed"}},
		{"APPEND", []string{"APPEND", "oom:seed", "x"}},
		{"INCR", []string{"INCR", "oom:counter"}},
		{"HSET", []string{"HSET", "oom:hash", "field", "value"}},
		{"SADD", []string{"SADD", "oom:set", "member"}},
		{"LPUSH", []string{"LPUSH", "oom:list", "member"}},
		{"ZADD", []string{"ZADD", "oom:zset", "1", "member"}},
		{"XADD", []string{"XADD", "oom:stream", "*", "field", "value"}},
		{"PFADD", []string{"PFADD", "oom:hll", "member"}},
		{"COPY", []string{"COPY", "oom:seed", "oom:copy"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(engine.New())

			// Seed type-specific inputs before entering OOM.
			switch tt.args[0] {
			case "INCR":
				execute(t, s, "SET", "oom:counter", "1")
			case "HSET":
				execute(t, s, "HSET", "oom:hash", "base", "value")
			case "SADD":
				execute(t, s, "SADD", "oom:set", "base")
			case "LPUSH":
				execute(t, s, "RPUSH", "oom:list", "base")
			case "ZADD":
				execute(t, s, "ZADD", "oom:zset", "0", "base")
			case "XADD":
				execute(t, s, "XADD", "oom:stream", "1-0", "base", "value")
			case "PFADD":
				execute(t, s, "PFADD", "oom:hll", "base")
			}

			forceNoEvictionOOM(t, s)

			args := make([][]byte, len(tt.args))
			for i, arg := range tt.args {
				args[i] = []byte(arg)
			}

			_, err := s.Execute(args)
			if !errors.Is(err, engine.ErrOOM) {
				t.Fatalf("%v error = %v, want engine.ErrOOM", tt.args, err)
			}
		})
	}
}

func TestNonDenyOOMMutationsAllowedWhileAlreadyOOM(t *testing.T) {
	tests := []struct {
		name  string
		seed  []string
		args  []string
		check func(t *testing.T, s *Server, got string)
	}{
		{
			name: "DEL",
			args: []string{"DEL", "oom:seed"},
			check: func(t *testing.T, s *Server, got string) {
				if got != ":1\r\n" {
					t.Fatalf("DEL = %q", got)
				}
			},
		},
		{
			name: "EXPIRE",
			args: []string{"EXPIRE", "oom:seed", "120"},
			check: func(t *testing.T, s *Server, got string) {
				if got != ":1\r\n" {
					t.Fatalf("EXPIRE = %q", got)
				}
			},
		},
		{
			name: "HDEL",
			seed: []string{"HSET", "oom:hash", "field", "value"},
			args: []string{"HDEL", "oom:hash", "field"},
			check: func(t *testing.T, s *Server, got string) {
				if got != ":1\r\n" {
					t.Fatalf("HDEL = %q", got)
				}
			},
		},
		{
			name: "SREM",
			seed: []string{"SADD", "oom:set", "member"},
			args: []string{"SREM", "oom:set", "member"},
			check: func(t *testing.T, s *Server, got string) {
				if got != ":1\r\n" {
					t.Fatalf("SREM = %q", got)
				}
			},
		},
		{
			name: "LPOP",
			seed: []string{"RPUSH", "oom:list", "a", "b"},
			args: []string{"LPOP", "oom:list"},
			check: func(t *testing.T, s *Server, got string) {
				if got != "$1\r\na\r\n" {
					t.Fatalf("LPOP = %q", got)
				}
			},
		},
		{
			name: "ZREM",
			seed: []string{"ZADD", "oom:zset", "1", "member"},
			args: []string{"ZREM", "oom:zset", "member"},
			check: func(t *testing.T, s *Server, got string) {
				if got != ":1\r\n" {
					t.Fatalf("ZREM = %q", got)
				}
			},
		},
		{
			name: "RENAME",
			args: []string{"RENAME", "oom:seed", "oom:renamed"},
			check: func(t *testing.T, s *Server, got string) {
				if got != "+OK\r\n" {
					t.Fatalf("RENAME = %q", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(engine.New())

			if len(tt.seed) > 0 {
				execute(t, s, tt.seed...)
			}
			forceNoEvictionOOM(t, s)

			args := make([][]byte, len(tt.args))
			for i, arg := range tt.args {
				args[i] = []byte(arg)
			}

			got, err := s.Execute(args)
			if err != nil {
				t.Fatalf("%v error = %v", tt.args, err)
			}
			tt.check(t, s, string(got))
		})
	}
}

func TestCommandInfoDenyOOMMatchesAuditedFamilies(t *testing.T) {
	s := New(engine.New())

	for _, name := range []string{
		"SET",
		"APPEND",
		"INCR",
		"HSET",
		"SADD",
		"LPUSH",
		"ZADD",
		"XADD",
		"PFADD",
		"COPY",
		"SORT",
	} {
		got := execute(t, s, "COMMAND", "INFO", name)
		if !strings.Contains(got, "+denyoom\r\n") {
			t.Fatalf("COMMAND INFO %s missing denyoom: %q", name, got)
		}
	}

	for _, name := range []string{
		"GET",
		"DEL",
		"EXPIRE",
		"PERSIST",
		"HDEL",
		"SREM",
		"LPOP",
		"ZREM",
		"XDEL",
		"PFCOUNT",
		"SORT_RO",
		"RENAME",
		"TOUCH",
	} {
		got := execute(t, s, "COMMAND", "INFO", name)
		if strings.Contains(got, "+denyoom\r\n") {
			t.Fatalf("COMMAND INFO %s unexpectedly reports denyoom: %q", name, got)
		}
	}
}
