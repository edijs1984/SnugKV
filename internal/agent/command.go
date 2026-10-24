package agent

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Commands from the console.
//
//	snug:agent:cmd:<region>:<server>      STREAM  c = command JSON, s = Ed25519 signature of c (base64)
//	snug:agent:results:<region>:<server>  STREAM  r = CommandResult JSON
//
// The console signs every command with its key; the agent got the public key
// when it joined (SNUG_AGENT_COMMAND_KEY) and runs only commands that carry a
// valid signature, name this server, have not expired and were not run
// before. Writing to the stream (the SnugKV password) is not enough to run
// anything.
const (
	KeyCommands = "snug:agent:cmd:"
	KeyResults  = "snug:agent:results:"
	// A command older than this is refused even when its expiry says otherwise.
	maxCommandLife = 10 * time.Minute
	clockSkew      = 2 * time.Minute
)

// Command is one signed instruction.
type Command struct {
	ID        string          `json:"id"`
	Region    string          `json:"region"`
	Server    string          `json:"server"`
	Kind      string          `json:"kind"`
	Args      json.RawMessage `json:"args,omitempty"`
	By        string          `json:"by"`
	IssuedMs  int64           `json:"issuedMs"`
	ExpiresMs int64           `json:"expiresMs"`
}

// CommandResult is what the agent reports back.
type CommandResult struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	OK         bool   `json:"ok"`
	Output     string `json:"output,omitempty"`
	Data       any    `json:"data,omitempty"`
	StartedMs  int64  `json:"startedMs"`
	FinishedMs int64  `json:"finishedMs"`
	Agent      string `json:"agent"`
}

// ParseCommandKey reads the console's public key (base64, 32 bytes).
func ParseCommandKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("the console command key must be 32 bytes in base64")
	}
	return ed25519.PublicKey(b), nil
}

// VerifyCommand checks the signature and that the command is for this server
// and current.
func VerifyCommand(key ed25519.PublicKey, payload, sig, region, server string, now time.Time) (Command, error) {
	var c Command
	jsonErr := json.Unmarshal([]byte(payload), &c)
	s, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || !ed25519.Verify(key, []byte(payload), s) {
		// The id is only used to report the refusal
		return Command{ID: c.ID, Kind: c.Kind}, errors.New("bad signature")
	}
	if jsonErr != nil {
		return Command{}, fmt.Errorf("unreadable command: %w", jsonErr)
	}
	if c.Region != region || c.Server != server {
		return c, fmt.Errorf("command is for %s/%s", c.Region, c.Server)
	}
	if c.ID == "" || c.Kind == "" {
		return c, errors.New("command without id or kind")
	}
	ms := now.UnixMilli()
	if c.IssuedMs > ms+clockSkew.Milliseconds() {
		return c, errors.New("command issued in the future (check the clocks)")
	}
	if c.ExpiresMs < ms || c.ExpiresMs-c.IssuedMs > maxCommandLife.Milliseconds() {
		return c, errors.New("command expired")
	}
	return c, nil
}

// commandRunner runs the console's commands on this server.
type commandRunner struct {
	o      Options
	key    ed25519.PublicKey
	dock   *dockerClient
	meshIP string
	box    *ecdh.PrivateKey // opens values sealed to this server
	// docker CLI (tests use a fake)
	dockerBin string
	binSHA    string // SHA-256 of the running binary
	mu        sync.Mutex
	seen      map[string]int64 // id -> expiry, against replays
	state     string           // file with the last stream id handled
	stream    string
	results   string
}

func newCommandRunner(o Options, dock *dockerClient) (*commandRunner, error) {
	key, err := ParseCommandKey(o.CommandKey)
	if err != nil {
		return nil, err
	}
	r := &commandRunner{o: o, key: key, dock: dock, meshIP: o.MeshIP, seen: map[string]int64{},
		stream: KeyCommands + o.Region + ":" + o.Server, results: KeyResults + o.Region + ":" + o.Server}
	if o.StateDir != "" {
		r.state = filepath.Join(o.StateDir, "commands.last")
		if r.box, err = EnsureBoxKey(o.StateDir); err != nil {
			return nil, fmt.Errorf("sealing key: %w", err)
		}
	} else if r.box, err = ecdh.X25519().GenerateKey(rand.Reader); err != nil {
		return nil, err
	}
	return r, nil
}

// BoxKey is the public half of the sealing key, for the card.
func (r *commandRunner) BoxKey() string {
	return base64.StdEncoding.EncodeToString(r.box.PublicKey().Bytes())
}

func (r *commandRunner) lastID() string {
	if r.state != "" {
		if b, err := os.ReadFile(r.state); err == nil && strings.TrimSpace(string(b)) != "" {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// startID is where reading starts: after the last command handled, or (first
// start, no state) after the newest command already in the stream.
func (r *commandRunner) startID(kv *kvConn) (string, error) {
	if id := r.lastID(); id != "" {
		return id, nil
	}
	v, err := kv.doAny("XREVRANGE", r.stream, "+", "-", "COUNT", "1")
	if err != nil {
		return "", err
	}
	if list, _ := v.([]any); len(list) == 1 {
		if e, _ := list[0].([]any); len(e) == 2 {
			if id, _ := e[0].(string); id != "" {
				return id, nil
			}
		}
	}
	return "0-0", nil
}

func (r *commandRunner) saveLast(id string) {
	if r.state != "" {
		_ = writeFileAtomic(r.state, []byte(id+"\n"), 0o600)
	}
}

// loop reads commands with XREAD BLOCK until ctx ends.
func (r *commandRunner) loop(ctx context.Context) {
	kv, err := parseKVURL(r.o.URL, r.o.CAFile)
	if err != nil {
		r.o.Logf("commands: %v", err)
		return
	}
	kv.blockFor = 5 * time.Second
	defer kv.close()
	r.o.Logf("commands: on (signed by the console)")
	last, failing := "", false
	for ctx.Err() == nil {
		if last == "" {
			id, err := r.startID(kv)
			if err != nil {
				if !failing {
					r.o.Logf("commands: reading: %v", err)
					failing = true
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}
			last = id
		}
		v, err := kv.doAny("XREAD", "COUNT", "10", "BLOCK", "5000", "STREAMS", r.stream, last)
		if err != nil {
			if !failing {
				r.o.Logf("commands: reading: %v", err)
				failing = true
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		failing = false
		for _, e := range streamEntries(v) {
			r.handle(ctx, kv, e)
			last = e.id
			r.saveLast(last)
		}
	}
}

type streamEntry struct {
	id     string
	fields map[string]string
}

// streamEntries flattens an XREAD reply of one stream.
func streamEntries(v any) []streamEntry {
	streams, _ := v.([]any)
	var out []streamEntry
	for _, s := range streams {
		pair, _ := s.([]any)
		if len(pair) != 2 {
			continue
		}
		entries, _ := pair[1].([]any)
		for _, e := range entries {
			ep, _ := e.([]any)
			if len(ep) != 2 {
				continue
			}
			id, _ := ep[0].(string)
			flat, _ := ep[1].([]any)
			f := map[string]string{}
			for i := 0; i+1 < len(flat); i += 2 {
				k, _ := flat[i].(string)
				val, _ := flat[i+1].(string)
				f[k] = val
			}
			out = append(out, streamEntry{id: id, fields: f})
		}
	}
	return out
}

func (r *commandRunner) handle(ctx context.Context, kv *kvConn, e streamEntry) {
	payload, sig := e.fields["c"], e.fields["s"]
	if payload == "" {
		return // not a command
	}
	now := time.Now()
	c, err := VerifyCommand(r.key, payload, sig, r.o.Region, r.o.Server, now)
	res := CommandResult{ID: c.ID, Kind: c.Kind, StartedMs: now.UnixMilli(), Agent: Version}
	if err != nil {
		r.o.Logf("commands: refused %s: %v", e.id, err)
		if c.ID == "" {
			return
		}
		res.Output = "refused: " + err.Error()
	} else if !r.firstTime(c) {
		r.o.Logf("commands: %s was already run", c.ID)
		return
	} else {
		r.o.Logf("commands: %s %s (by %s)", c.Kind, c.ID, c.By)
		data, out, err := r.run(ctx, c)
		res.OK, res.Data, res.Output = err == nil, data, out
		if err != nil {
			res.Output = strings.TrimSpace(out + "\n" + err.Error())
		}
	}
	res.FinishedMs = time.Now().UnixMilli()
	b, _ := json.Marshal(res)
	if _, err := kv.do("XADD", r.results, "MAXLEN", "~", "200", "*", "r", string(b)); err != nil {
		r.o.Logf("commands: cannot report the result of %s: %v", c.ID, err)
	}
}

func (r *commandRunner) firstTime(c Command) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UnixMilli()
	for id, exp := range r.seen {
		if exp < now {
			delete(r.seen, id)
		}
	}
	if _, ok := r.seen[c.ID]; ok {
		return false
	}
	r.seen[c.ID] = c.ExpiresMs
	return true
}

// run does one command. Kinds: ping, swarm.init, swarm.join, swarm.leave, swarm.tokens,
// the stack ones in stack.go and the node ones in node.go.
func (r *commandRunner) run(_ context.Context, c Command) (data any, output string, err error) {
	var args struct {
		Managers []string `json:"managers"`
		Token    string   `json:"token"`
		Force    bool     `json:"force"`
	}
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &args); err != nil {
			return nil, "", fmt.Errorf("bad arguments: %w", err)
		}
	}
	needDocker := func() error {
		if r.dock == nil {
			return errors.New("this agent runs without Docker")
		}
		return nil
	}
	meshAddr := func() (string, error) {
		p, err := netip.ParsePrefix(r.meshIP)
		if err != nil {
			return "", errors.New("this server has no mesh address (it joined without the mesh)")
		}
		return p.Addr().String(), nil
	}
	switch c.Kind {
	case "ping":
		return map[string]string{"agent": Version}, "pong", nil
	case "swarm.init":
		if err := needDocker(); err != nil {
			return nil, "", err
		}
		addr, err := meshAddr()
		if err != nil {
			return nil, "", err
		}
		t, err := r.dock.swarmInit(addr)
		if err != nil {
			return nil, "", err
		}
		return t, "swarm created; this server is its first manager on " + addr, nil
	case "swarm.tokens":
		if err := needDocker(); err != nil {
			return nil, "", err
		}
		t, err := r.dock.swarmTokens()
		if err != nil {
			return nil, "", err
		}
		if addr, err := meshAddr(); err == nil {
			t.AdvertiseAddr = addr
		}
		return t, "", nil
	case "swarm.join":
		if err := needDocker(); err != nil {
			return nil, "", err
		}
		addr, err := meshAddr()
		if err != nil {
			return nil, "", err
		}
		if len(args.Managers) == 0 || args.Token == "" {
			return nil, "", errors.New("swarm.join needs managers and a token")
		}
		for i, m := range args.Managers {
			if _, err := netip.ParseAddrPort(m); err != nil {
				if _, err := netip.ParseAddr(m); err != nil {
					return nil, "", fmt.Errorf("manager address %q", m)
				}
				args.Managers[i] = m + ":2377"
			}
		}
		if err := r.dock.swarmJoin(addr, args.Managers, args.Token); err != nil {
			return nil, "", err
		}
		return nil, "joined the swarm on " + addr, nil
	case "swarm.leave":
		if err := needDocker(); err != nil {
			return nil, "", err
		}
		if err := r.dock.swarmLeave(args.Force); err != nil {
			return nil, "", err
		}
		return nil, "left the swarm", nil
	}
	if isStackKind(c.Kind) {
		return r.runStack(context.Background(), c)
	}
	if isDockerKind(c.Kind) {
		return r.runDocker(context.Background(), c)
	}
	if isNodeKind(c.Kind) {
		return r.runNode(context.Background(), c)
	}
	if c.Kind == "agent.update" {
		return r.requestUpdate(c)
	}
	if c.Kind == "agent.remove" {
		return r.requestRemove(c)
	}
	return nil, "", fmt.Errorf("unknown command %q", c.Kind)
}
