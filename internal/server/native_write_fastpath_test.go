package server

import (
	"reflect"
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
		{"rpush-simple", [][]byte{[]byte("RPUSH"), []byte("l"), []byte("v")}, false},
		{"sadd-simple", [][]byte{[]byte("SADD"), []byte("s"), []byte("m")}, true},
		{"zadd-simple", [][]byte{[]byte("ZADD"), []byte("z"), []byte("1"), []byte("m")}, false},
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
		{"sadd", [][]byte{[]byte("SADD"), []byte("s"), []byte("m")}, ":1\r\n"},
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

func TestConcurrentNativeMutationFastPathDefersBlockingCapableWrites(t *testing.T) {
	s := New(engine.New())
	tests := [][][]byte{
		{[]byte("RPUSH"), []byte("l"), []byte("v")},
		{[]byte("ZADD"), []byte("z"), []byte("1"), []byte("m")},
	}
	for _, args := range tests {
		if _, handled, err := s.executeAuthorizedConcurrentNativeMutation(args); err != nil || handled {
			t.Fatalf("blocking-capable form handled=%v err=%v args=%q", handled, err, args)
		}
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


func TestConcurrentBlockingCapableNativeMutationFastPath(t *testing.T) {
	tests := []struct {
		name string
		args [][]byte
		want string
	}{
		{"rpush", [][]byte{[]byte("RPUSH"), []byte("l"), []byte("v")}, ":1\r\n"},
		{"zadd", [][]byte{[]byte("ZADD"), []byte("z"), []byte("1"), []byte("m")}, ":1\r\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(engine.New())
			got, handled, err := s.executeAuthorizedConcurrentBlockingCapableNativeMutation(tc.args)
			if err != nil {
				t.Fatalf("fast path error: %v", err)
			}
			if !handled {
				t.Fatal("blocking-aware fast path did not handle simple native mutation")
			}
			if string(got) != tc.want {
				t.Fatalf("reply=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestConcurrentBlockingCapableNativeMutationSignalsWaiters(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		s := New(engine.New())
		waiter, ok := s.registerListWaiter([]string{"l"})
		if !ok {
			t.Fatal("could not register list waiter")
		}
		defer s.unregisterListWaiter(waiter)

		if _, handled, err := s.executeAuthorizedConcurrentBlockingCapableNativeMutation(
			[][]byte{[]byte("RPUSH"), []byte("l"), []byte("v")},
		); err != nil || !handled {
			t.Fatalf("RPUSH handled=%v err=%v", handled, err)
		}

		select {
		case <-waiter.ch:
		default:
			t.Fatal("RPUSH fast path did not signal list waiter")
		}
	})

	t.Run("zset", func(t *testing.T) {
		s := New(engine.New())
		waiter, ok := s.registerZSetWaiter([]string{"z"})
		if !ok {
			t.Fatal("could not register zset waiter")
		}
		defer s.unregisterZSetWaiter(waiter)

		if _, handled, err := s.executeAuthorizedConcurrentBlockingCapableNativeMutation(
			[][]byte{[]byte("ZADD"), []byte("z"), []byte("1"), []byte("m")},
		); err != nil || !handled {
			t.Fatalf("ZADD handled=%v err=%v", handled, err)
		}

		select {
		case <-waiter.ch:
		default:
			t.Fatal("ZADD fast path did not signal zset waiter")
		}
	})
}


func TestConcurrentZSetScoreFastPath(t *testing.T) {
	s := New(engine.New())
	if _, _, _, err := s.store.ZSetAdd(
		"z",
		[]engine.ZSetItem{{Member: []byte("m"), Score: 42}},
		engine.ZSetAddOptions{},
	); err != nil {
		t.Fatalf("seed zset: %v", err)
	}

	got, handled, err := s.executeAuthorizedConcurrentZSetScore(
		[][]byte{[]byte("ZSCORE"), []byte("z"), []byte("m")},
	)
	if err != nil {
		t.Fatalf("fast path error: %v", err)
	}
	if !handled {
		t.Fatal("ZSCORE fast path did not handle command")
	}
	if string(got) != "$2\r\n42\r\n" {
		t.Fatalf("reply=%q", got)
	}

	got, handled, err = s.executeAuthorizedConcurrentZSetScore(
		[][]byte{[]byte("ZSCORE"), []byte("z"), []byte("missing")},
	)
	if err != nil || !handled {
		t.Fatalf("missing score handled=%v err=%v", handled, err)
	}
	if string(got) != "$-1\r\n" {
		t.Fatalf("missing reply=%q", got)
	}
}


func TestConcurrentSetContainsFastPath(t *testing.T) {
	s := New(engine.New())
	if _, err := s.store.SetAdd("s", [][]byte{[]byte("m")}); err != nil {
		t.Fatalf("seed set: %v", err)
	}

	got, handled, err := s.executeAuthorizedConcurrentSetContains(
		[][]byte{[]byte("SISMEMBER"), []byte("s"), []byte("m")},
	)
	if err != nil {
		t.Fatalf("fast path error: %v", err)
	}
	if !handled {
		t.Fatal("SISMEMBER fast path did not handle command")
	}
	if string(got) != ":1\r\n" {
		t.Fatalf("reply=%q", got)
	}

	got, handled, err = s.executeAuthorizedConcurrentSetContains(
		[][]byte{[]byte("SISMEMBER"), []byte("s"), []byte("missing")},
	)
	if err != nil || !handled {
		t.Fatalf("missing member handled=%v err=%v", handled, err)
	}
	if string(got) != ":0\r\n" {
		t.Fatalf("missing reply=%q", got)
	}
}


func TestConcurrentSetAddBatchFastPath(t *testing.T) {
	s := New(engine.New())

	keys := [][]byte{[]byte("a"), []byte("a"), []byte("b")}
	members := [][]byte{[]byte("x"), []byte("x"), []byte("y")}
	results, handled, err := s.executeAuthorizedConcurrentSetAddBatch(keys, members)
	if err != nil {
		t.Fatalf("batch error: %v", err)
	}
	if !handled {
		t.Fatal("SADD batch fast path did not handle commands")
	}
	want := []int64{1, 0, 1}
	if !reflect.DeepEqual(results, want) {
		t.Fatalf("results=%v want=%v", results, want)
	}

	found, err := s.store.SetContains("a", []byte("x"))
	if err != nil || !found {
		t.Fatalf("a/x found=%v err=%v", found, err)
	}
	found, err = s.store.SetContains("b", []byte("y"))
	if err != nil || !found {
		t.Fatalf("b/y found=%v err=%v", found, err)
	}
}
