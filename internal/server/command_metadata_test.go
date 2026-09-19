package server

import (
	"bytes"
	"reflect"
	"strings"
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

func TestCommandDocsGet(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("GET"),
		},
	)

	for _, want := range [][]byte{
		[]byte("$3\r\nget\r\n"),
		[]byte("$7\r\nsummary\r\n"),
		[]byte("$5\r\nsince\r\n"),
		[]byte("$5\r\ngroup\r\n"),
		[]byte("$10\r\ncomplexity\r\n"),
		[]byte("$9\r\narguments\r\n"),
		[]byte("$14\r\nkey_spec_index\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS GET missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandDocsRequestedOrder(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("SET"),
			[]byte("GET"),
		},
	)

	setPos := bytes.Index(
		reply,
		[]byte("$3\r\nset\r\n"),
	)

	getPos := bytes.Index(
		reply,
		[]byte("$3\r\nget\r\n"),
	)

	if setPos < 0 || getPos < 0 {
		t.Fatalf(
			"missing requested docs: %q",
			reply,
		)
	}

	if setPos >= getPos {
		t.Fatalf(
			"requested order not preserved: %q",
			reply,
		)
	}
}

func TestCommandDocsUnknownIsOmitted(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("DOESNOTEXIST"),
			[]byte("GET"),
		},
	)

	if bytes.Contains(
		reply,
		[]byte("doesnotexist"),
	) {
		t.Fatalf(
			"unknown command unexpectedly documented: %q",
			reply,
		)
	}

	if !bytes.Contains(
		reply,
		[]byte("$3\r\nget\r\n"),
	) {
		t.Fatalf(
			"known command missing: %q",
			reply,
		)
	}
}

func TestCommandDocsAllOnlyAdvertisesRegisteredCommands(
	t *testing.T,
) {
	reply := commandDocsReply(nil)

	for name := range commandDocs {
		if _, ok := commandTable[name]; !ok {
			continue
		}

		want := []byte(
			strings.ToLower(name),
		)

		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS missing %s",
				name,
			)
		}
	}
}

func TestCommandDocsFlagsUseRedisArrayShape(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("DEL"),
			[]byte("PFADD"),
		},
	)

	for _, want := range [][]byte{
		[]byte("$5\r\nflags\r\n"),
		[]byte("+multiple\r\n"),
		[]byte("+optional\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS flags missing %q in %q",
				want,
				reply,
			)
		}
	}

	for _, forbidden := range [][]byte{
		[]byte("$8\r\nmultiple\r\n:1\r\n"),
		[]byte("$8\r\noptional\r\n:1\r\n"),
	} {
		if bytes.Contains(reply, forbidden) {
			t.Fatalf(
				"legacy argument flag encoding still present: %q",
				reply,
			)
		}
	}
}

func TestCommandDocsEvalDynamicArguments(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("EVAL"),
		},
	)

	for _, want := range [][]byte{
		[]byte("$3\r\nkey\r\n"),
		[]byte("$3\r\narg\r\n"),
		[]byte("+optional\r\n"),
		[]byte("+multiple\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS EVAL missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandDocsCopyOptions(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("COPY"),
		},
	)

	for _, want := range [][]byte{
		[]byte("$14\r\ndestination-db\r\n"),
		[]byte("$2\r\nDB\r\n"),
		[]byte("$7\r\nreplace\r\n"),
		[]byte("$7\r\nREPLACE\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS COPY missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandDocsSetRichMetadata(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{[]byte("SET")},
	)

	for _, want := range [][]byte{
		[]byte("$7\r\nhistory\r\n"),
		[]byte("$9\r\ncondition\r\n"),
		[]byte("$5\r\noneof\r\n"),
		[]byte("$2\r\nNX\r\n"),
		[]byte("$2\r\nXX\r\n"),
		[]byte("$3\r\nGET\r\n"),
		[]byte("$10\r\nexpiration\r\n"),
		[]byte("$2\r\nEX\r\n"),
		[]byte("$2\r\nPX\r\n"),
		[]byte("$4\r\nEXAT\r\n"),
		[]byte("$4\r\nPXAT\r\n"),
		[]byte("$7\r\nKEEPTTL\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS SET missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandDocsXAddRichMetadata(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{[]byte("XADD")},
	)

	for _, want := range [][]byte{
		[]byte("$7\r\nhistory\r\n"),
		[]byte("$10\r\nNOMKSTREAM\r\n"),
		[]byte("$4\r\ntrim\r\n"),
		[]byte("$5\r\nblock\r\n"),
		[]byte("$8\r\nstrategy\r\n"),
		[]byte("$6\r\nMAXLEN\r\n"),
		[]byte("$5\r\nMINID\r\n"),
		[]byte("$8\r\noperator\r\n"),
		[]byte("$1\r\n=\r\n"),
		[]byte("$1\r\n~\r\n"),
		[]byte("$5\r\nLIMIT\r\n"),
		[]byte("$11\r\nid-selector\r\n"),
		[]byte("$1\r\n*\r\n"),
		[]byte("$4\r\ndata\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS XADD missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandDocsGeoAddRichMetadata(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{[]byte("GEOADD")},
	)

	for _, want := range [][]byte{
		[]byte("$7\r\nhistory\r\n"),
		[]byte("$9\r\ncondition\r\n"),
		[]byte("$2\r\nNX\r\n"),
		[]byte("$2\r\nXX\r\n"),
		[]byte("$6\r\nchange\r\n"),
		[]byte("$2\r\nCH\r\n"),
		[]byte("$9\r\nlongitude\r\n"),
		[]byte("$8\r\nlatitude\r\n"),
		[]byte("$6\r\nmember\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS GEOADD missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandInfoParentHasSupportedSubcommands(t *testing.T) {
	reply := commandInfoReply("CLIENT")

	for _, want := range [][]byte{
		[]byte("$6\r\nclient\r\n"),
		[]byte("$9\r\nclient|id\r\n"),
		[]byte("$11\r\nclient|list\r\n"),
		[]byte("$11\r\nclient|kill\r\n"),
		[]byte("$14\r\nclient|unblock\r\n"),
		[]byte("$15\r\nclient|tracking\r\n"),
		[]byte("$14\r\nclient|caching\r\n"),
		[]byte("$15\r\nclient|getredir\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND INFO CLIENT missing %q in %q",
				want,
				reply,
			)
		}
	}

}

func TestCommandInfoSubcommandLeaf(t *testing.T) {
	reply := commandInfoReply("FUNCTION|LOAD")

	for _, want := range [][]byte{
		[]byte("$13\r\nfunction|load\r\n"),
		[]byte("+write\r\n"),
		[]byte("+denyoom\r\n"),
		[]byte("+noscript\r\n"),
		[]byte("+@scripting\r\n"),
		[]byte("$25\r\nrequest_policy:all_shards\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"FUNCTION|LOAD metadata missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandInfoParentMetadata(t *testing.T) {
	reply := commandInfoReply("COMMAND")

	for _, want := range [][]byte{
		[]byte("+loading\r\n"),
		[]byte("+stale\r\n"),
		[]byte("+@slow\r\n"),
		[]byte("+@connection\r\n"),
		[]byte("$29\r\nnondeterministic_output_order\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND parent metadata missing %q in %q",
				want,
				reply,
			)
		}
	}

	if bytes.Contains(
		reply,
		[]byte("+readonly\r\n"),
	) {
		t.Fatalf(
			"COMMAND parent still using generic readonly metadata: %q",
			reply,
		)
	}
}

func TestCommandDocsParentSubcommands(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("SCRIPT"),
		},
	)

	for _, want := range [][]byte{
		[]byte("$11\r\nsubcommands\r\n"),
		[]byte("$11\r\nscript|load\r\n"),
		[]byte("$13\r\nscript|exists\r\n"),
		[]byte("$12\r\nscript|flush\r\n"),
		[]byte("$11\r\nscript|kill\r\n"),
		[]byte("$12\r\nscript|debug\r\n"),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS SCRIPT missing %q in %q",
				want,
				reply,
			)
		}
	}

}

func TestCommandDocsSubcommandLookup(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("COMMAND|COUNT"),
		},
	)

	for _, want := range [][]byte{
		[]byte("$13\r\ncommand|count\r\n"),
		[]byte("Returns a count of commands."),
	} {
		if !bytes.Contains(reply, want) {
			t.Fatalf(
				"COMMAND DOCS COMMAND|COUNT missing %q in %q",
				want,
				reply,
			)
		}
	}
}

func TestCommandParentsAdvertiseOnlyImplementedChildren(t *testing.T) {
	cases := map[string][]string{
		"COMMAND": {
			"COMMAND|COUNT",
			"COMMAND|INFO",
			"COMMAND|DOCS",
			"COMMAND|GETKEYS",
			"COMMAND|GETKEYSANDFLAGS",
		},
		"CLIENT": {
			"CLIENT|ID",
			"CLIENT|GETNAME",
			"CLIENT|SETNAME",
			"CLIENT|SETINFO",
			"CLIENT|INFO",
			"CLIENT|LIST",
			"CLIENT|KILL",
			"CLIENT|UNBLOCK",
			"CLIENT|HELP",
			"CLIENT|TRACKING",
			"CLIENT|CACHING",
			"CLIENT|GETREDIR",
		},
		"FUNCTION": {
			"FUNCTION|LOAD",
			"FUNCTION|LIST",
			"FUNCTION|DELETE",
			"FUNCTION|FLUSH",
			"FUNCTION|DUMP",
			"FUNCTION|RESTORE",
			"FUNCTION|STATS",
			"FUNCTION|KILL",
			"FUNCTION|HELP",
		},
		"SCRIPT": {
			"SCRIPT|LOAD",
			"SCRIPT|EXISTS",
			"SCRIPT|FLUSH",
			"SCRIPT|KILL",
			"SCRIPT|DEBUG",
		},
	}

	for parent, children := range cases {
		reply := commandInfoReply(parent)

		for _, child := range children {
			if !bytes.Contains(
				reply,
				[]byte(strings.ToLower(child)),
			) {
				t.Fatalf(
					"%s missing %s: %q",
					parent,
					child,
					reply,
				)
			}
		}
	}
}

func TestCommandInfoSupportedIncludesSubcommands(t *testing.T) {
	for _, name := range []string{
		"COMMAND|COUNT",
		"COMMAND|DOCS",
		"CLIENT|ID",
		"CLIENT|UNBLOCK",
		"CLIENT|TRACKING",
		"CLIENT|CACHING",
		"CLIENT|GETREDIR",
		"FUNCTION|LOAD",
		"FUNCTION|STATS",
		"SCRIPT|LOAD",
		"SCRIPT|KILL",
		"SCRIPT|DEBUG",
	} {
		if !commandInfoSupported(name) {
			t.Fatalf(
				"expected %s to be introspectable",
				name,
			)
		}
	}

	for _, name := range []string{
		"DOESNOTEXIST",
	} {
		if commandInfoSupported(name) {
			t.Fatalf(
				"unsupported command unexpectedly introspectable: %s",
				name,
			)
		}
	}
}

func TestCommandInfoSubcommandMetadataIsNotNull(t *testing.T) {
	for _, name := range []string{
		"COMMAND|COUNT",
		"CLIENT|ID",
		"CLIENT|TRACKING",
		"CLIENT|CACHING",
		"CLIENT|GETREDIR",
		"FUNCTION|LOAD",
		"SCRIPT|KILL",
		"SCRIPT|DEBUG",
	} {
		reply := commandInfoReply(name)

		if bytes.Equal(reply, nullBulk()) {
			t.Fatalf(
				"%s returned null metadata",
				name,
			)
		}

		if !bytes.Contains(
			reply,
			[]byte(strings.ToLower(name)),
		) {
			t.Fatalf(
				"%s metadata missing canonical name: %q",
				name,
				reply,
			)
		}
	}
}
