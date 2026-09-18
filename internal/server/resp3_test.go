package server

import (
	"bytes"
	"testing"
)

func TestRESP3AdaptNullBulk(t *testing.T) {
	got := resp3AdaptBasic(
		[]byte("$-1\r\n"),
	)

	want := []byte("_\r\n")

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3AdaptNestedNull(t *testing.T) {
	got := resp3AdaptBasic(
		[]byte(
			"*2\r\n" +
				"$5\r\nhello\r\n" +
				"$-1\r\n",
		),
	)

	want := []byte(
		"*2\r\n" +
			"$5\r\nhello\r\n" +
			"_\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3AdaptPreservesOrdinaryTypes(
	t *testing.T,
) {
	inputs := [][]byte{
		[]byte("+OK\r\n"),
		[]byte(":123\r\n"),
		[]byte("$5\r\nhello\r\n"),
		[]byte("*2\r\n:1\r\n:2\r\n"),
	}

	for _, input := range inputs {
		got := resp3AdaptBasic(input)

		if !bytes.Equal(got, input) {
			t.Fatalf(
				"got %q want %q",
				got,
				input,
			)
		}
	}
}

func TestHelloReplyRESP3IsMap(t *testing.T) {
	got := helloReply(3, 42)

	if len(got) == 0 || got[0] != '%' {
		t.Fatalf(
			"RESP3 HELLO is not a map: %q",
			got,
		)
	}
}

func TestHelloReplyRESP2IsArray(t *testing.T) {
	got := helloReply(2, 42)

	if len(got) == 0 || got[0] != '*' {
		t.Fatalf(
			"RESP2 HELLO is not an array: %q",
			got,
		)
	}
}

func TestClientSessionDefaultsRESP2(t *testing.T) {
	c := newClientSession(
		1,
		nil,
		"remote",
		"local",
	)

	if got := c.protocolVersion(); got != 2 {
		t.Fatalf(
			"got protocol %d want 2",
			got,
		)
	}

	c.setProtocol(3)

	if got := c.protocolVersion(); got != 3 {
		t.Fatalf(
			"got protocol %d want 3",
			got,
		)
	}
}

func TestRESP3HGetAllMap(t *testing.T) {
	input := []byte(
		"*4\r\n" +
			"$1\r\na\r\n" +
			"$1\r\n1\r\n" +
			"$1\r\nb\r\n" +
			"$1\r\n2\r\n",
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("HGETALL"),
			[]byte("h"),
		},
		input,
	)

	want := []byte(
		"%2\r\n" +
			"$1\r\na\r\n" +
			"$1\r\n1\r\n" +
			"$1\r\nb\r\n" +
			"$1\r\n2\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3SMembersSet(t *testing.T) {
	input := []byte(
		"*2\r\n" +
			"$1\r\na\r\n" +
			"$1\r\nb\r\n",
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("SMEMBERS"),
			[]byte("s"),
		},
		input,
	)

	want := []byte(
		"~2\r\n" +
			"$1\r\na\r\n" +
			"$1\r\nb\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3ZRangeWithScores(t *testing.T) {
	input := []byte(
		"*4\r\n" +
			"$1\r\na\r\n" +
			"$3\r\n1.5\r\n" +
			"$1\r\nb\r\n" +
			"$3\r\n2.5\r\n",
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("ZRANGE"),
			[]byte("z"),
			[]byte("0"),
			[]byte("-1"),
			[]byte("WITHSCORES"),
		},
		input,
	)

	want := []byte(
		"*2\r\n" +
			"*2\r\n" +
			"$1\r\na\r\n" +
			",1.5\r\n" +
			"*2\r\n" +
			"$1\r\nb\r\n" +
			",2.5\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3InfoVerbatim(t *testing.T) {
	input := formatBulkString(
		[]byte("# Server\r\nfoo:bar\r\n"),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("INFO"),
		},
		input,
	)

	want := resp3Verbatim(
		"txt",
		[]byte("# Server\r\nfoo:bar\r\n"),
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3ACLGetUser(t *testing.T) {
	input := array(
		formatBulkString([]byte("flags")),
		array(
			formatBulkString([]byte("on")),
			formatBulkString([]byte("nopass")),
		),

		formatBulkString([]byte("passwords")),
		array(),

		formatBulkString([]byte("commands")),
		formatBulkString([]byte("+@all")),

		formatBulkString([]byte("keys")),
		formatBulkString([]byte("~*")),

		formatBulkString([]byte("channels")),
		formatBulkString([]byte("&*")),

		formatBulkString([]byte("selectors")),
		array(),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("ACL"),
			[]byte("GETUSER"),
			[]byte("default"),
		},
		input,
	)

	if len(got) == 0 ||
		got[0] != '%' {
		t.Fatalf(
			"ACL GETUSER is not map: %q",
			got,
		)
	}

	if !bytes.Contains(
		got,
		[]byte("~2\r\n"),
	) {
		t.Fatalf(
			"ACL flags were not converted to set: %q",
			got,
		)
	}
}

func TestRESP3CommandInfoGET(t *testing.T) {
	input := array(
		commandInfoLegacyReply("GET"),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("COMMAND"),
			[]byte("INFO"),
			[]byte("GET"),
		},
		input,
	)

	if len(got) == 0 ||
		got[0] != '*' {
		t.Fatalf(
			"COMMAND INFO outer reply invalid: %q",
			got,
		)
	}

	if !bytes.Contains(
		got,
		[]byte(
			"~2\r\n+readonly\r\n+fast\r\n",
		),
	) {
		t.Fatalf(
			"command flags were not RESP3 set: %q",
			got,
		)
	}

	if !bytes.Contains(
		got,
		[]byte(
			"%3\r\n",
		),
	) {
		t.Fatalf(
			"key spec was not RESP3 map: %q",
			got,
		)
	}
}

func TestRESP3PubSubPush(t *testing.T) {
	input := array(
		formatBulkString(
			[]byte("message"),
		),
		formatBulkString(
			[]byte("channel"),
		),
		formatBulkString(
			[]byte("hello"),
		),
	)

	got := resp3PubSubPush(input)

	want := []byte(
		">3\r\n" +
			"$7\r\nmessage\r\n" +
			"$7\r\nchannel\r\n" +
			"$5\r\nhello\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3PubSubPushNull(t *testing.T) {
	input := array(
		formatBulkString(
			[]byte("unsubscribe"),
		),
		nullBulk(),
		integer(0),
	)

	got := resp3PubSubPush(input)

	want := []byte(
		">3\r\n" +
			"$11\r\nunsubscribe\r\n" +
			"_\r\n" +
			":0\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3ZScoreDouble(t *testing.T) {
	got := resp3AdaptCommand(
		[][]byte{
			[]byte("ZSCORE"),
			[]byte("z"),
			[]byte("a"),
		},
		formatBulkString(
			[]byte("1.5"),
		),
	)

	want := []byte(",1.5\r\n")

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3ZMSCoreDoubles(t *testing.T) {
	input := array(
		formatBulkString(
			[]byte("1.5"),
		),
		nullBulk(),
		formatBulkString(
			[]byte("2.5"),
		),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("ZMSCORE"),
			[]byte("z"),
			[]byte("a"),
			[]byte("missing"),
			[]byte("b"),
		},
		input,
	)

	want := []byte(
		"*3\r\n" +
			",1.5\r\n" +
			"_\r\n" +
			",2.5\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3ZPopPairs(t *testing.T) {
	input := array(
		formatBulkString(
			[]byte("a"),
		),
		formatBulkString(
			[]byte("1.5"),
		),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("ZPOPMIN"),
			[]byte("z"),
		},
		input,
	)

	want := []byte(
		"*1\r\n" +
			"*2\r\n" +
			"$1\r\na\r\n" +
			",1.5\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3GeoPosDoubles(t *testing.T) {
	input := array(
		array(
			formatBulkString(
				[]byte("13.1"),
			),
			formatBulkString(
				[]byte("38.1"),
			),
		),
		[]byte("*-1\r\n"),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("GEOPOS"),
			[]byte("geo"),
			[]byte("a"),
			[]byte("missing"),
		},
		input,
	)

	want := []byte(
		"*2\r\n" +
			"*2\r\n" +
			",13.1\r\n" +
			",38.1\r\n" +
			"_\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3XReadMap(t *testing.T) {
	input := array(
		array(
			formatBulkString(
				[]byte("stream"),
			),
			array(),
		),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("XREAD"),
			[]byte("STREAMS"),
			[]byte("stream"),
			[]byte("0"),
		},
		input,
	)

	want := []byte(
		"%1\r\n" +
			"$6\r\nstream\r\n" +
			"*0\r\n",
	)

	if !bytes.Equal(got, want) {
		t.Fatalf(
			"got %q want %q",
			got,
			want,
		)
	}
}

func TestRESP3XInfoStreamMap(t *testing.T) {
	input := array(
		formatBulkString(
			[]byte("length"),
		),
		integer(2),
		formatBulkString(
			[]byte("groups"),
		),
		integer(1),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("XINFO"),
			[]byte("STREAM"),
			[]byte("s"),
		},
		input,
	)

	if len(got) == 0 ||
		got[0] != '%' {
		t.Fatalf(
			"XINFO STREAM is not map: %q",
			got,
		)
	}
}

func TestRESP3FunctionStatsMap(t *testing.T) {
	input := array(
		formatBulkString(
			[]byte("running_script"),
		),
		nullBulk(),

		formatBulkString(
			[]byte("engines"),
		),
		array(
			formatBulkString(
				[]byte("LUA"),
			),
			array(
				formatBulkString(
					[]byte("libraries_count"),
				),
				integer(0),

				formatBulkString(
					[]byte("functions_count"),
				),
				integer(0),
			),
		),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("FUNCTION"),
			[]byte("STATS"),
		},
		input,
	)

	if len(got) == 0 ||
		got[0] != '%' {
		t.Fatalf(
			"FUNCTION STATS is not map: %q",
			got,
		)
	}
}

func TestRESP3ClientInfoVerbatim(t *testing.T) {
	input := formatBulkString(
		[]byte("id=1 resp=3"),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("CLIENT"),
			[]byte("INFO"),
		},
		input,
	)

	if len(got) == 0 ||
		got[0] != '=' {
		t.Fatalf(
			"CLIENT INFO is not verbatim: %q",
			got,
		)
	}
}

func TestRESP3ConfigGetMap(t *testing.T) {
	input := array(
		formatBulkString(
			[]byte("maxmemory"),
		),
		formatBulkString(
			[]byte("0"),
		),
	)

	got := resp3AdaptCommand(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("GET"),
			[]byte("maxmemory"),
		},
		input,
	)

	if len(got) == 0 ||
		got[0] != '%' {
		t.Fatalf(
			"CONFIG GET is not map: %q",
			got,
		)
	}
}
