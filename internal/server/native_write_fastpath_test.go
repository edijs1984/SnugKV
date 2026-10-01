package server

import (
	"snugkv/internal/engine"
	"testing"
)

func TestConcurrentScalarCommandIncludesSimpleNativeWrites(t *testing.T) {
	tests := []struct {
		name string
		args [][]byte
		want bool
	}{
		{"hset-simple", [][]byte{[]byte("HSET"), []byte("h"), []byte("f"), []byte("v")}, true},
		{"rpush-simple", [][]byte{[]byte("RPUSH"), []byte("l"), []byte("v")}, true},
		{"sadd-simple", [][]byte{[]byte("SADD"), []byte("s"), []byte("m")}, true},
		{"zadd-simple", [][]byte{[]byte("ZADD"), []byte("z"), []byte("1"), []byte("m")}, true},
		{"hset-multi", [][]byte{[]byte("HSET"), []byte("h"), []byte("f1"), []byte("v1"), []byte("f2"), []byte("v2")}, false},
		{"rpush-multi", [][]byte{[]byte("RPUSH"), []byte("l"), []byte("a"), []byte("b")}, false},
		{"sadd-multi", [][]byte{[]byte("SADD"), []byte("s"), []byte("a"), []byte("b")}, false},
		{"zadd-options", [][]byte{[]byte("ZADD"), []byte("z"), []byte("NX"), []byte("1"), []byte("m")}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConcurrentScalarCommand(tc.args); got != tc.want {
				t.Fatalf("isConcurrentScalarCommand(%q)=%v want=%v", tc.args, got, tc.want)
			}
		})
	}
}


func TestConcurrentNativeMutationFastPath(t *testing.T) {
	tests := []struct {
		name string
		args [][]byte
		want string
	}{
		{"hset", [][]byte{[]byte("HSET"), []byte("h"), []byte("f"), []byte("v")}, ":1\r\n"},
		{"rpush", [][]byte{[]byte("RPUSH"), []byte("l"), []byte("v")}, ":1\r\n"},
		{"sadd", [][]byte{[]byte("SADD"), []byte("s"), []byte("m")}, ":1\r\n"},
		{"zadd", [][]byte{[]byte("ZADD"), []byte("z"), []byte("1"), []byte("m")}, ":1\r\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(engine.New())
			got, handled, err := s.executeAuthorizedConcurrentNativeMutation(tc.args)
			if err != nil {
				t.Fatalf("fast path error: %v", err)
			}
			if !handled {
				t.Fatal("fast path did not handle simple native mutation")
			}
			if string(got) != tc.want {
				t.Fatalf("reply=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestConcurrentNativeMutationFastPathRejectsComplexForms(t *testing.T) {
	s := New(engine.New())
	tests := [][][]byte{
		{[]byte("HSET"), []byte("h"), []byte("f1"), []byte("v1"), []byte("f2"), []byte("v2")},
		{[]byte("RPUSH"), []byte("l"), []byte("a"), []byte("b")},
		{[]byte("SADD"), []byte("s"), []byte("a"), []byte("b")},
		{[]byte("ZADD"), []byte("z"), []byte("NX"), []byte("1"), []byte("m")},
	}
	for _, args := range tests {
		if _, handled, err := s.executeAuthorizedConcurrentNativeMutation(args); err != nil || handled {
			t.Fatalf("complex form handled=%v err=%v args=%q", handled, err, args)
		}
	}
}
