package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func monitorTestSend(t *testing.T, conn net.Conn, args ...string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, arg := range args { fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(arg), arg) }
	if _, err := io.WriteString(conn, b.String()); err != nil { t.Fatal(err) }
}

func monitorTestReply(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil { t.Fatal(err) }
	if len(line) < 3 { t.Fatalf("invalid reply %q", line) }
	if line[0] == '*' || line[0] == '$' {
		n, err := strconv.Atoi(strings.TrimSuffix(line[1:], "\r\n"))
		if err != nil { t.Fatal(err) }
		if n < 0 { return line }
		if line[0] == '*' {
			for i := 0; i < n; i++ { monitorTestReply(t, r) }
		} else {
			data := make([]byte, n+2)
			if _, err := io.ReadFull(r, data); err != nil { t.Fatal(err) }
		}
	}
	return line
}

func TestMonitorTCPCommandsAndExec(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer s.Close()
	dial := func() net.Conn {
		c, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
		if err != nil { t.Fatal(err) }
		t.Cleanup(func() { c.Close() })
		c.SetDeadline(time.Now().Add(5*time.Second))
		return c
	}
	monitor, actor := dial(), dial()
	mr, ar := bufio.NewReader(monitor), bufio.NewReader(actor)
	monitorTestSend(t, monitor, "MONITOR")
	if got := monitorTestReply(t, mr); got != "+OK\r\n" { t.Fatalf("MONITOR=%q", got) }
	commands := [][]string{
		{"PING"}, {"SET", "monitor:a", "hello"}, {"GET", "monitor:a"},
		{"MULTI"}, {"SET", "monitor:b", "world"}, {"GET", "monitor:b"},
		{"EXEC"}, {"DEL", "monitor:a", "monitor:b"},
	}
	for _, args := range commands {
		monitorTestSend(t, actor, args...)
		if got := monitorTestReply(t, ar); strings.HasPrefix(got, "-") { t.Fatalf("command %v: %q", args, got) }
	}
	for _, args := range commands {
		line, err := mr.ReadString('\n')
		if err != nil { t.Fatal(err) }
		space := strings.IndexByte(line, ' ')
		if space < 2 { t.Fatalf("invalid event %q", line) }
		if _, err := strconv.ParseFloat(line[1:space], 64); err != nil { t.Fatal(err) }
		want := " [0 "+actor.LocalAddr().String()+"]"
		for _, arg := range args { want += " "+monitorQuote([]byte(arg)) }
		want += "\r\n"
		if line[space:] != want { t.Fatalf("event=%q want suffix=%q", line, want) }
	}
}

func TestMonitorBinaryEscaping(t *testing.T) {
	got := monitorQuote([]byte{'a', '"', '\\', '\n', '\r', '\t', 0, 255})
	want := "\"a\\\"\\\\\\n\\r\\t\\x00\\xff\""
	if got != want { t.Fatalf("quote=%q want=%q", got, want) }
}

func TestMonitorSensitiveCommandsExcluded(t *testing.T) {
	for _, name := range []string{"AUTH", "HELLO", "ACL", "CONFIG", "MIGRATE", "MONITOR"} {
		if monitorVisible(clientArgs(name, "secret")) { t.Fatalf("%s must not be emitted", name) }
	}
}

func TestMonitorACLRejected(t *testing.T) {
	conn := connectTestServer(t)
	r := bufio.NewReader(conn)
	monitorTestSend(t, conn, "ACL", "SETUSER", "monitor-denied", "on", ">test-password", "+@all", "-monitor", "~*")
	if got := monitorTestReply(t, r); got != "+OK\r\n" { t.Fatalf("SETUSER=%q", got) }
	monitorTestSend(t, conn, "AUTH", "monitor-denied", "test-password")
	if got := monitorTestReply(t, r); got != "+OK\r\n" { t.Fatalf("AUTH=%q", got) }
	monitorTestSend(t, conn, "MONITOR")
	if got := monitorTestReply(t, r); !strings.HasPrefix(got, "-NOPERM") { t.Fatalf("MONITOR=%q", got) }
}

func TestMonitorResetAndQuit(t *testing.T) {
	conn := connectTestServer(t)
	r := bufio.NewReader(conn)
	for _, args := range [][]string{{"CLIENT", "SETNAME", "before-reset"}, {"MONITOR"}} {
		monitorTestSend(t, conn, args...)
		if got := monitorTestReply(t, r); got != "+OK\r\n" { t.Fatalf("%v=%q", args, got) }
	}
	monitorTestSend(t, conn, "PING")
	if got := monitorTestReply(t, r); got != "+PONG\r\n" { t.Fatalf("PING reply must precede event: %q", got) }
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasSuffix(line, " \"PING\"\r\n") { t.Fatalf("PING event=%q err=%v", line, err) }
	monitorTestSend(t, conn, "RESET")
	if got := monitorTestReply(t, r); got != "+RESET\r\n" { t.Fatalf("RESET=%q", got) }
	monitorTestSend(t, conn, "CLIENT", "GETNAME")
	if got := monitorTestReply(t, r); got != "$-1\r\n" { t.Fatalf("name after RESET=%q", got) }
	monitorTestSend(t, conn, "PING")
	if got := monitorTestReply(t, r); got != "+PONG\r\n" { t.Fatalf("PING after RESET=%q", got) }
	monitorTestSend(t, conn, "QUIT")
	if got := monitorTestReply(t, r); got != "+OK\r\n" { t.Fatalf("QUIT=%q (unexpected monitor event after RESET)", got) }
	if _, err := r.ReadByte(); err != io.EOF { t.Fatalf("QUIT should close connection: %v", err) }
}

func TestMonitorCanSubscribeAgainAfterReset(t *testing.T) {
	conn := connectTestServer(t)
	r := bufio.NewReader(conn)
	for i := 0; i < 3; i++ {
		monitorTestSend(t, conn, "MONITOR")
		if got := monitorTestReply(t, r); got != "+OK\r\n" { t.Fatalf("MONITOR=%q", got) }
		monitorTestSend(t, conn, "PING")
		if got := monitorTestReply(t, r); got != "+PONG\r\n" { t.Fatalf("PING=%q", got) }
		line, err := r.ReadString('\n')
		if err != nil || !strings.HasSuffix(line, " \"PING\"\r\n") { t.Fatalf("event=%q err=%v", line, err) }
		monitorTestSend(t, conn, "RESET")
		if got := monitorTestReply(t, r); got != "+RESET\r\n" { t.Fatalf("RESET=%q", got) }
	}
}

func TestMonitorLuaOrdering(t *testing.T) {
	for _, transaction := range []bool{false, true} {
		name := "direct"
		if transaction { name = "exec" }
		t.Run(name, func(t *testing.T) {
			s, err := Listen("127.0.0.1:0", engine.New())
			if err != nil { t.Fatal(err) }
			defer s.Close()
			dial := func() net.Conn {
				conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
				if err != nil { t.Fatal(err) }
				t.Cleanup(func() { conn.Close() })
				conn.SetDeadline(time.Now().Add(5*time.Second))
				return conn
			}
			monitor, actor := dial(), dial()
			mr, ar := bufio.NewReader(monitor), bufio.NewReader(actor)
			monitorTestSend(t, monitor, "MONITOR")
			if got := monitorTestReply(t, mr); got != "+OK\r\n" { t.Fatalf("MONITOR=%q", got) }
			if transaction {
				monitorTestSend(t, actor, "MULTI")
				monitorTestReply(t, ar)
			}
			script := "redis.call('SET',KEYS[1],ARGV[1]); return redis.call('GET',KEYS[1])"
			monitorTestSend(t, actor, "EVAL", script, "1", "monitor:lua", "hello")
			if got := monitorTestReply(t, ar); strings.HasPrefix(got, "-") { t.Fatalf("EVAL=%q", got) }
			if transaction {
				monitorTestSend(t, actor, "EXEC")
				if got := monitorTestReply(t, ar); strings.HasPrefix(got, "-") { t.Fatalf("EXEC=%q", got) }
			}
			peer := " [0 "+actor.LocalAddr().String()+"] "
			want := []string{
				peer+"\"EVAL\" "+monitorQuote([]byte(script))+" \"1\" \"monitor:lua\" \"hello\"\r\n",
				" [0 lua] \"SET\" \"monitor:lua\" \"hello\"\r\n",
				" [0 lua] \"GET\" \"monitor:lua\"\r\n",
			}
			if transaction {
				want = append([]string{peer+"\"MULTI\"\r\n"}, want...)
				want = append(want, peer+"\"EXEC\"\r\n")
			}
			for _, suffix := range want {
				line, err := mr.ReadString('\n')
				if err != nil || !strings.HasSuffix(line, suffix) { t.Fatalf("event=%q want suffix=%q err=%v", line, suffix, err) }
			}
		})
	}
}


func TestMonitorScriptFamilies(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, srv *Server, actor net.Conn, ar *bufio.Reader)
		command    []string
		wantNested string
	}{
		{
			name: "evalsha",
			setup: func(t *testing.T, srv *Server, actor net.Conn, ar *bufio.Reader) {
				script := "return redis.call('GET',KEYS[1])"
				monitorTestSend(t, actor, "SET", "monitor:script", "hello")
				monitorTestReply(t, ar)
				monitorTestSend(t, actor, "SCRIPT", "LOAD", script)
				monitorTestReply(t, ar)
			},
			command: []string{"EVALSHA", scriptSHA("return redis.call('GET',KEYS[1])"), "1", "monitor:script"},
			wantNested: " [0 lua] \"GET\" \"monitor:script\"\r\n",
		},
		{
			name: "eval_ro",
			setup: func(t *testing.T, srv *Server, actor net.Conn, ar *bufio.Reader) {
				monitorTestSend(t, actor, "SET", "monitor:script", "hello")
				monitorTestReply(t, ar)
			},
			command: []string{"EVAL_RO", "return redis.call('GET',KEYS[1])", "1", "monitor:script"},
			wantNested: " [0 lua] \"GET\" \"monitor:script\"\r\n",
		},
		{
			name: "fcall",
			setup: func(t *testing.T, srv *Server, actor net.Conn, ar *bufio.Reader) {
				code := "#!lua name=monitorlib\nredis.register_function('reader', function(keys,args) return redis.call('GET',keys[1]) end)"
				monitorTestSend(t, actor, "SET", "monitor:script", "hello")
				monitorTestReply(t, ar)
				if got := loadFunctionLibrary(t, srv, code); !strings.HasPrefix(got, "$") {
					t.Fatalf("FUNCTION LOAD=%q", got)
				}
			},
			command: []string{"FCALL", "reader", "1", "monitor:script"},
			wantNested: " [0 lua] \"GET\" \"monitor:script\"\r\n",
		},
		{
			name: "fcall_ro",
			setup: func(t *testing.T, srv *Server, actor net.Conn, ar *bufio.Reader) {
				code := "#!lua name=monitorlibro\nredis.register_function{function_name='reader_ro',callback=function(keys,args) return redis.call('GET',keys[1]) end,flags={'no-writes'}}"
				monitorTestSend(t, actor, "SET", "monitor:script", "hello")
				monitorTestReply(t, ar)
				if got := loadFunctionLibrary(t, srv, code); !strings.HasPrefix(got, "$") {
					t.Fatalf("FUNCTION LOAD=%q", got)
				}
			},
			command: []string{"FCALL_RO", "reader_ro", "1", "monitor:script"},
			wantNested: " [0 lua] \"GET\" \"monitor:script\"\r\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Listen("127.0.0.1:0", engine.New())
			if err != nil { t.Fatal(err) }
			defer s.Close()

			dial := func() net.Conn {
				conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
				if err != nil { t.Fatal(err) }
				t.Cleanup(func() { conn.Close() })
				conn.SetDeadline(time.Now().Add(5*time.Second))
				return conn
			}

			monitor, actor := dial(), dial()
			mr, ar := bufio.NewReader(monitor), bufio.NewReader(actor)
			tc.setup(t, s.server, actor, ar)

			monitorTestSend(t, monitor, "MONITOR")
			if got := monitorTestReply(t, mr); got != "+OK\r\n" { t.Fatalf("MONITOR=%q", got) }

			monitorTestSend(t, actor, tc.command...)
			if got := monitorTestReply(t, ar); strings.HasPrefix(got, "-") { t.Fatalf("%s=%q", tc.command[0], got) }

			line, err := mr.ReadString('\n')
			if err != nil { t.Fatal(err) }
			space := strings.IndexByte(line, ' ')
			if space < 2 { t.Fatalf("invalid outer event %q", line) }
			outer := " [0 "+actor.LocalAddr().String()+"]"
			for _, arg := range tc.command { outer += " "+monitorQuote([]byte(arg)) }
			outer += "\r\n"
			if line[space:] != outer { t.Fatalf("outer event=%q want suffix=%q", line, outer) }

			line, err = mr.ReadString('\n')
			if err != nil || !strings.HasSuffix(line, tc.wantNested) {
				t.Fatalf("nested event=%q want suffix=%q err=%v", line, tc.wantNested, err)
			}
		})
	}
}
