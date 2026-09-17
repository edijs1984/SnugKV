package server

import (
	"reflect"
	"testing"
)

func TestCommandKeysFixedSingleKey(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("SET"),
			[]byte("key"),
			[]byte("value"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 1 {
		t.Fatalf("refs=%d want 1", len(refs))
	}

	if string(refs[0].value) != "key" {
		t.Fatalf(
			"key=%q want key",
			refs[0].value,
		)
	}

	want := []string{"OW", "update"}

	if !reflect.DeepEqual(refs[0].flags, want) {
		t.Fatalf(
			"flags=%v want=%v",
			refs[0].flags,
			want,
		)
	}
}

func TestCommandKeysMultiKey(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("DEL"),
			[]byte("a"),
			[]byte("b"),
			[]byte("c"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 3 {
		t.Fatalf("refs=%d want 3", len(refs))
	}

	for index, want := range []string{
		"a",
		"b",
		"c",
	} {
		if string(refs[index].value) != want {
			t.Fatalf(
				"refs[%d]=%q want %q",
				index,
				refs[index].value,
				want,
			)
		}
	}
}

func TestCommandKeysCopyFlags(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("COPY"),
			[]byte("source"),
			[]byte("destination"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 2 {
		t.Fatalf("refs=%d want 2", len(refs))
	}

	if !reflect.DeepEqual(
		refs[0].flags,
		[]string{"RO", "access"},
	) {
		t.Fatalf(
			"source flags=%v",
			refs[0].flags,
		)
	}

	if !reflect.DeepEqual(
		refs[1].flags,
		[]string{"OW", "update"},
	) {
		t.Fatalf(
			"destination flags=%v",
			refs[1].flags,
		)
	}
}

func TestCommandKeysEval(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("EVAL"),
			[]byte("return 1"),
			[]byte("2"),
			[]byte("a"),
			[]byte("b"),
			[]byte("arg"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 2 {
		t.Fatalf("refs=%d want 2", len(refs))
	}

	if string(refs[0].value) != "a" ||
		string(refs[1].value) != "b" {
		t.Fatalf("unexpected refs: %#v", refs)
	}
}

func TestCommandKeysEvalROFlags(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("EVAL_RO"),
			[]byte("return 1"),
			[]byte("1"),
			[]byte("a"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"RO", "access"}

	if !reflect.DeepEqual(refs[0].flags, want) {
		t.Fatalf(
			"flags=%v want=%v",
			refs[0].flags,
			want,
		)
	}
}

func TestCommandKeysFCall(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("FCALL"),
			[]byte("fn"),
			[]byte("2"),
			[]byte("a"),
			[]byte("b"),
			[]byte("arg"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 2 {
		t.Fatalf("refs=%d want 2", len(refs))
	}
}

func TestCommandKeysRejectsUnknownCommand(t *testing.T) {
	_, err := commandKeys(
		[][]byte{
			[]byte("DOESNOTEXIST"),
		},
	)

	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCommandKeysRejectsTooManyDeclaredKeys(t *testing.T) {
	_, err := commandKeys(
		[][]byte{
			[]byte("EVAL"),
			[]byte("return 1"),
			[]byte("2"),
			[]byte("only-one"),
		},
	)

	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCommandKeysZUnion(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("ZUNION"),
			[]byte("2"),
			[]byte("a"),
			[]byte("b"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 2 {
		t.Fatalf("refs=%d want 2", len(refs))
	}

	if string(refs[0].value) != "a" ||
		string(refs[1].value) != "b" {
		t.Fatalf("unexpected refs: %#v", refs)
	}
}

func TestCommandKeysZUnionStore(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("ZUNIONSTORE"),
			[]byte("dest"),
			[]byte("2"),
			[]byte("a"),
			[]byte("b"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 3 {
		t.Fatalf("refs=%d want 3", len(refs))
	}

	if string(refs[0].value) != "dest" {
		t.Fatalf("destination=%q", refs[0].value)
	}

	if !reflect.DeepEqual(
		refs[0].flags,
		[]string{"OW", "update"},
	) {
		t.Fatalf(
			"destination flags=%v",
			refs[0].flags,
		)
	}

	for index, want := range []string{"a", "b"} {
		ref := refs[index+1]

		if string(ref.value) != want {
			t.Fatalf(
				"refs[%d]=%q want=%q",
				index+1,
				ref.value,
				want,
			)
		}

		if !reflect.DeepEqual(
			ref.flags,
			[]string{"RO", "access"},
		) {
			t.Fatalf(
				"source flags=%v",
				ref.flags,
			)
		}
	}
}

func TestCommandKeysZMPop(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("ZMPOP"),
			[]byte("2"),
			[]byte("a"),
			[]byte("b"),
			[]byte("MIN"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 2 {
		t.Fatalf("refs=%d want 2", len(refs))
	}
}

func TestCommandKeysBZMPop(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("BZMPOP"),
			[]byte("0"),
			[]byte("2"),
			[]byte("a"),
			[]byte("b"),
			[]byte("MAX"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 2 {
		t.Fatalf("refs=%d want 2", len(refs))
	}

	if string(refs[0].value) != "a" ||
		string(refs[1].value) != "b" {
		t.Fatalf("unexpected refs: %#v", refs)
	}
}

func TestCommandKeysXRead(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("XREAD"),
			[]byte("COUNT"),
			[]byte("10"),
			[]byte("STREAMS"),
			[]byte("stream-a"),
			[]byte("stream-b"),
			[]byte("0-0"),
			[]byte("$"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 2 {
		t.Fatalf("refs=%d want 2", len(refs))
	}

	if string(refs[0].value) != "stream-a" ||
		string(refs[1].value) != "stream-b" {
		t.Fatalf("unexpected refs: %#v", refs)
	}
}

func TestCommandKeysXReadGroup(t *testing.T) {
	refs, err := commandKeys(
		[][]byte{
			[]byte("XREADGROUP"),
			[]byte("GROUP"),
			[]byte("group"),
			[]byte("consumer"),
			[]byte("COUNT"),
			[]byte("10"),
			[]byte("STREAMS"),
			[]byte("stream-a"),
			[]byte("stream-b"),
			[]byte(">"),
			[]byte(">"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(refs) != 2 {
		t.Fatalf("refs=%d want 2", len(refs))
	}

	wantFlags := []string{
		"RO",
		"access",
	}

	for _, ref := range refs {
		if !reflect.DeepEqual(
			ref.flags,
			wantFlags,
		) {
			t.Fatalf(
				"flags=%v want=%v",
				ref.flags,
				wantFlags,
			)
		}
	}
}
