package server

import (
	"bytes"
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

func TestCommandInfoReplyHasRedisTenFieldShape(t *testing.T) {
	reply := commandInfoReply("GET")

	if !bytes.HasPrefix(
		reply,
		[]byte("*10\r\n"),
	) {
		t.Fatalf(
			"COMMAND INFO GET reply does not have 10 fields: %q",
			reply,
		)
	}
}

func TestCommandInfoGetMetadata(t *testing.T) {
	reply := commandInfoReply("GET")

	for _, want := range [][]byte{
		[]byte("+readonly\r\n"),
		[]byte("+fast\r\n"),
		[]byte("+@read\r\n"),
		[]byte("+@string\r\n"),
		[]byte("+@fast\r\n"),
		[]byte("+RO\r\n"),
		[]byte("+access\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND INFO GET missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandInfoSetMetadata(t *testing.T) {
	reply := commandInfoReply("SET")

	for _, want := range [][]byte{
		[]byte("+write\r\n"),
		[]byte("+denyoom\r\n"),
		[]byte("+@write\r\n"),
		[]byte("+@string\r\n"),
		[]byte("+RW\r\n"),
		[]byte("+access\r\n"),
		[]byte("+update\r\n"),
		[]byte("+variable_flags\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND INFO SET missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandInfoEvalUsesDynamicKeySpec(t *testing.T) {
	reply := commandInfoReply("EVAL")

	for _, want := range [][]byte{
		[]byte("+movablekeys\r\n"),
		[]byte("$6\r\nkeynum\r\n"),
		[]byte("$9\r\nkeynumidx\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND INFO EVAL missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandInfoCopyHasTwoKeySpecs(t *testing.T) {
	reply := commandInfoReply("COPY")

	if bytes.Count(
		reply,
		[]byte("$12\r\nbegin_search\r\n"),
	) != 2 {
		t.Fatalf(
			"COPY should publish two key specs: %q",
			reply,
		)
	}
}

func TestCommandInfoSetIncludesRedisKeySpecNote(t *testing.T) {
	reply := commandInfoReply("SET")

	want := []byte(
		"RW and ACCESS due to the optional `GET` argument",
	)

	if !bytes.Contains(reply, want) {
		t.Fatalf(
			"COMMAND INFO SET missing key-spec note: %q",
			reply,
		)
	}
}

func TestCommandInfoEvalIncludesWorstCaseNote(t *testing.T) {
	reply := commandInfoReply("EVAL")

	want := []byte(
		"We cannot tell how the keys will be used so we assume the worst, RW and UPDATE",
	)

	if !bytes.Contains(reply, want) {
		t.Fatalf(
			"COMMAND INFO EVAL missing key-spec note: %q",
			reply,
		)
	}
}

func TestCommandInfoXAddMetadata(t *testing.T) {
	reply := commandInfoReply("XADD")

	for _, want := range [][]byte{
		[]byte("$23\r\nnondeterministic_output\r\n"),
		[]byte(
			"UPDATE instead of INSERT because of the optional trimming feature",
		),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND INFO XADD missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandInfoPFAddUsesInsertFlag(t *testing.T) {
	reply := commandInfoReply("PFADD")

	if !bytes.Contains(
		reply,
		[]byte("+insert\r\n"),
	) {
		t.Fatalf(
			"COMMAND INFO PFADD missing insert flag: %q",
			reply,
		)
	}
}

func TestCommandInfoTipsUseBulkStrings(t *testing.T) {
	for _, name := range []string{"DEL", "XADD"} {
		reply := commandInfoReply(name)

		var want []byte

		switch name {
		case "DEL":
			want = []byte(
				"$26\r\nrequest_policy:multi_shard\r\n",
			)
		case "XADD":
			want = []byte(
				"$23\r\nnondeterministic_output\r\n",
			)
		}

		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND INFO %s tips are not bulk strings: %q",
				name,
				reply,
			)
		}
	}
}
