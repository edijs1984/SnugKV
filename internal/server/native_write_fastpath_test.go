package server

import (
	"reflect"
	"snugkv/internal/engine"
	"testing"
)


func TestConcurrentScalarCommandIncludesRealLifeSingleKeyTraffic(t *testing.T) {
	tests := []struct {
		name string
		args [][]byte
		want bool
	}{
		{"set-ex", [][]byte{[]byte("SET"), []byte("k"), []byte("v"), []byte("EX"), []byte("60")}, true},
		{"set-nx", [][]byte{[]byte("SET"), []byte("k"), []byte("v"), []byte("NX")}, false},
		{"incr", [][]byte{[]byte("INCR"), []byte("counter")}, true},
		{"expire", [][]byte{[]byte("EXPIRE"), []byte("k"), []byte("60")}, true},
		{"lpush", [][]byte{[]byte("LPUSH"), []byte("l"), []byte("v")}, true},
		{"ltrim", [][]byte{[]byte("LTRIM"), []byte("l"), []byte("0"), []byte("99")}, true},
		{"zincrby", [][]byte{[]byte("ZINCRBY"), []byte("z"), []byte("1"), []byte("m")}, true},
		{"zrevrank", [][]byte{[]byte("ZREVRANK"), []byte("z"), []byte("m")}, true},
		{"zrevrange", [][]byte{[]byte("ZREVRANGE"), []byte("z"), []byte("0"), []byte("9")}, true},
		{"zrevrange-withscores", [][]byte{[]byte("ZREVRANGE"), []byte("z"), []byte("0"), []byte("9"), []byte("WITHSCORES")}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConcurrentScalarCommand(tc.args); got != tc.want {
				t.Fatalf("isConcurrentScalarCommand(%q)=%v want=%v", tc.args, got, tc.want)
			}
		})
	}
}

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


func TestConcurrentSetContainsBatchFastPath(t *testing.T) {
	s := New(engine.New())
	if _, err := s.store.SetAdd("a", [][]byte{[]byte("x"), []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.SetAdd("b", [][]byte{[]byte("z")}); err != nil {
		t.Fatal(err)
	}

	keys := [][]byte{
		[]byte("a"),
		[]byte("a"),
		[]byte("b"),
		[]byte("a"),
	}
	members := [][]byte{
		[]byte("x"),
		[]byte("missing"),
		[]byte("z"),
		[]byte("y"),
	}
	results, handled, err := s.executeAuthorizedConcurrentSetContainsBatch(keys, members)
	if err != nil {
		t.Fatalf("batch error: %v", err)
	}
	if !handled {
		t.Fatal("SISMEMBER batch fast path did not handle commands")
	}
	want := []int64{1, 0, 1, 1}
	if !reflect.DeepEqual(results, want) {
		t.Fatalf("results=%v want=%v", results, want)
	}
}


func TestConcurrentHashSetBatchFastPath(t *testing.T) {
	s := New(engine.New())

	keys := [][]byte{
		[]byte("h1"),
		[]byte("h1"),
		[]byte("h2"),
		[]byte("h1"),
	}
	fields := [][]byte{
		[]byte("a"),
		[]byte("a"),
		[]byte("x"),
		[]byte("b"),
	}
	values := [][]byte{
		[]byte("1"),
		[]byte("2"),
		[]byte("3"),
		[]byte("4"),
	}

	results, handled, err := s.executeAuthorizedConcurrentHashSetBatch(keys, fields, values)
	if err != nil {
		t.Fatalf("batch error: %v", err)
	}
	if !handled {
		t.Fatal("HSET batch fast path did not handle commands")
	}
	want := []int64{1, 0, 1, 1}
	if !reflect.DeepEqual(results, want) {
		t.Fatalf("results=%v want=%v", results, want)
	}
}


func TestConcurrentHashGetBatchFastPath(t *testing.T) {
	s := New(engine.New())
	if _, err := s.store.HashSet(
		"h1",
		[][]byte{[]byte("a"), []byte("b")},
		[][]byte{[]byte("1"), []byte("2")},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.HashSet(
		"h2",
		[][]byte{[]byte("x")},
		[][]byte{[]byte("3")},
	); err != nil {
		t.Fatal(err)
	}

	keys := [][]byte{
		[]byte("h1"),
		[]byte("h1"),
		[]byte("h2"),
		[]byte("h1"),
	}
	fields := [][]byte{
		[]byte("a"),
		[]byte("missing"),
		[]byte("x"),
		[]byte("b"),
	}

	results := make([]engine.HashGetResult, len(keys))
	out, handled, err := s.executeAuthorizedConcurrentHashGetBatchInto(
		keys,
		fields,
		nil,
		results,
	)
	if err != nil {
		t.Fatalf("batch error: %v", err)
	}
	if !handled {
		t.Fatal("HGET batch fast path did not handle commands")
	}

	found := make([]bool, len(results))
	got := make([]string, len(results))
	for i, result := range results {
		found[i] = result.Found
		if !result.Found {
			continue
		}
		start := int(result.Offset)
		end := start + int(result.Length)
		got[i] = string(out[start:end])
	}

	wantFound := []bool{true, false, true, true}
	if !reflect.DeepEqual(found, wantFound) {
		t.Fatalf("found=%v want=%v", found, wantFound)
	}
	want := []string{"1", "", "3", "2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("values=%v want=%v", got, want)
	}
}

func TestConcurrentHashGetBatchPreservesInterleavedOrder(t *testing.T) {
	s := New(engine.New())
	if _, err := s.store.HashSet(
		"h1",
		[][]byte{[]byte("a"), []byte("b")},
		[][]byte{[]byte("1"), []byte("2")},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.HashSet(
		"h2",
		[][]byte{[]byte("x")},
		[][]byte{[]byte("3")},
	); err != nil {
		t.Fatal(err)
	}

	keys := [][]byte{
		[]byte("h1"),
		[]byte("h2"),
		[]byte("h1"),
		[]byte("h2"),
	}
	fields := [][]byte{
		[]byte("a"),
		[]byte("x"),
		[]byte("b"),
		[]byte("missing"),
	}

	results := make([]engine.HashGetResult, len(keys))
	out, handled, err := s.executeAuthorizedConcurrentHashGetBatchInto(
		keys,
		fields,
		nil,
		results,
	)
	if err != nil {
		t.Fatalf("batch error: %v", err)
	}
	if !handled {
		t.Fatal("HGET batch fast path did not handle commands")
	}

	found := make([]bool, len(results))
	got := make([]string, len(results))
	for i, result := range results {
		found[i] = result.Found
		if !result.Found {
			continue
		}
		start := int(result.Offset)
		end := start + int(result.Length)
		got[i] = string(out[start:end])
	}

	wantFound := []bool{true, true, true, false}
	if !reflect.DeepEqual(found, wantFound) {
		t.Fatalf("found=%v want=%v", found, wantFound)
	}
	want := []string{"1", "3", "2", ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("values=%v want=%v", got, want)
	}
}


func TestConcurrentListPushRightBatchFastPath(t *testing.T) {
	s := New(engine.New())
	keys := [][]byte{[]byte("a"), []byte("a"), []byte("b")}
	values := [][]byte{[]byte("x"), []byte("y"), []byte("z")}

	results, handled, err := s.executeAuthorizedConcurrentListPushRightBatch(keys, values)
	if err != nil {
		t.Fatalf("batch error: %v", err)
	}
	if !handled {
		t.Fatal("RPUSH batch fast path did not handle commands")
	}
	if want := []int64{1, 2, 1}; !reflect.DeepEqual(results, want) {
		t.Fatalf("results=%v want=%v", results, want)
	}

	got, err := s.store.ListRange("a", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]byte{[]byte("x"), []byte("y")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list a=%q want=%q", got, want)
	}
}

func TestConcurrentListIndexFastPaths(t *testing.T) {
	s := New(engine.New())
	if _, err := s.store.ListPushRight("a", [][]byte{[]byte("x"), []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.ListPushRight("b", [][]byte{[]byte("z")}); err != nil {
		t.Fatal(err)
	}

	got, handled, err := s.executeAuthorizedConcurrentListIndex(
		[][]byte{[]byte("LINDEX"), []byte("a"), []byte("1")},
	)
	if err != nil || !handled {
		t.Fatalf("single handled=%v err=%v", handled, err)
	}
	if string(got) != "$1\r\ny\r\n" {
		t.Fatalf("single reply=%q", got)
	}

	results, handled, err := s.executeAuthorizedConcurrentListIndexBatch(
		[][]byte{[]byte("a"), []byte("a"), []byte("b")},
		[]int64{0, 9, 0},
	)
	if err != nil || !handled {
		t.Fatalf("batch handled=%v err=%v", handled, err)
	}
	found := []bool{results[0].Found, results[1].Found, results[2].Found}
	if want := []bool{true, false, true}; !reflect.DeepEqual(found, want) {
		t.Fatalf("found=%v want=%v", found, want)
	}
	if string(results[0].Value) != "x" || string(results[2].Value) != "z" {
		t.Fatalf("values=%q,%q", results[0].Value, results[2].Value)
	}
}

func TestConcurrentZSetAddAndScoreBatchFastPaths(t *testing.T) {
	s := New(engine.New())
	keys := [][]byte{[]byte("a"), []byte("a"), []byte("b")}
	scores := [][]byte{[]byte("1"), []byte("2"), []byte("7")}
	members := [][]byte{[]byte("x"), []byte("y"), []byte("z")}

	added, handled, err := s.executeAuthorizedConcurrentZSetAddBatch(keys, scores, members)
	if err != nil || !handled {
		t.Fatalf("ZADD batch handled=%v err=%v", handled, err)
	}
	if want := []int64{1, 1, 1}; !reflect.DeepEqual(added, want) {
		t.Fatalf("added=%v want=%v", added, want)
	}

	results, handled, err := s.executeAuthorizedConcurrentZSetScoreBatch(
		[][]byte{[]byte("a"), []byte("a"), []byte("b"), []byte("a")},
		[][]byte{[]byte("x"), []byte("missing"), []byte("z"), []byte("y")},
	)
	if err != nil || !handled {
		t.Fatalf("ZSCORE batch handled=%v err=%v", handled, err)
	}
	found := []bool{results[0].Found, results[1].Found, results[2].Found, results[3].Found}
	if want := []bool{true, false, true, true}; !reflect.DeepEqual(found, want) {
		t.Fatalf("found=%v want=%v", found, want)
	}
	if results[0].Score != 1 || results[2].Score != 7 || results[3].Score != 2 {
		t.Fatalf("scores=%+v", results)
	}
}
