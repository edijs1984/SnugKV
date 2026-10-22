package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Everything lives under one key prefix, so an ACL can keep other users out:
// "~snugkv:secrets:*" for the module's user, "%R~-snugkv:secrets:*"-style
// rules (or simply no access to it) for the rest. Values are ciphertext either way.
const (
	keyEnvs     = "snugkv:secrets:envs"      // SET of environment names
	keyEnvMeta  = "snugkv:secrets:envmeta"   // HASH env -> {"createdMs":...}
	keyEnv      = "snugkv:secrets:env:"      // HASH name -> current record, per env
	keyVer      = "snugkv:secrets:ver:"      // HASH name -> last version number, per env
	keyHist     = "snugkv:secrets:hist:"     // LIST of past records, newest first, per env+name
	keyTokens   = "snugkv:secrets:tokens"    // HASH id -> token record
	keyTokUsed  = "snugkv:secrets:tokenused" // HASH id -> last use (ms)
	keyAudit    = "snugkv:secrets:audit"     // STREAM of reads and changes
	keyCheck    = "snugkv:secrets:check"     // a sealed known value: detects a wrong key
	histKeep    = 20
	auditKeep   = 5000
	maxEnvs     = 200
	maxSecrets  = 1000
	maxValueLen = 64 << 10
	maxTokens   = 200
)

var (
	partRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	idRe   = regexp.MustCompile(`^[a-z2-7]{16}$`)

	errNotFound = errors.New("not found")
)

// envName joins and checks a project and environment ("airsig", "prod" -> "airsig/prod").
func envName(project, env string) (string, error) {
	if !partRe.MatchString(project) || !partRe.MatchString(env) {
		return "", errors.New("project and environment names use a-z, 0-9, - and _ (up to 48 characters)")
	}
	return project + "/" + env, nil
}

func checkName(name string) error {
	if !nameRe.MatchString(name) {
		return errors.New("secret names look like environment variables: letters, digits and _, not starting with a digit")
	}
	return nil
}

func bind(env, name string) string { return env + "\x00" + name }

// record is a secret's current value, or one of its past versions.
type record struct {
	Value   string `json:"v,omitempty"` // ciphertext
	Version int64  `json:"ver"`
	Ms      int64  `json:"ms"`
	By      string `json:"by,omitempty"`
	Deleted bool   `json:"del,omitempty"`
}

// SecretInfo describes a secret without its value (unless revealed).
type SecretInfo struct {
	Name      string `json:"name"`
	Hint      string `json:"hint"`
	Version   int64  `json:"version"`
	UpdatedMs int64  `json:"updatedMs"`
	UpdatedBy string `json:"updatedBy,omitempty"`
	Value     string `json:"value,omitempty"`
}

// VersionInfo is one entry of a secret's history.
type VersionInfo struct {
	Version int64  `json:"version"`
	Ms      int64  `json:"ms"`
	By      string `json:"by,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// EnvInfo describes an environment.
type EnvInfo struct {
	Name      string `json:"name"`
	Project   string `json:"project"`
	Env       string `json:"env"`
	Secrets   int64  `json:"secrets"`
	CreatedMs int64  `json:"createdMs"`
}

// Token is a read token: it can read the secrets of the environments it lists
// ("airsig/prod", or "airsig/*" for every environment of a project).
type Token struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Envs       []string `json:"envs"`
	Hash       string   `json:"hash,omitempty"`
	CreatedMs  int64    `json:"createdMs"`
	LastUsedMs int64    `json:"lastUsedMs,omitempty"`
}

func (t *Token) allows(env string) bool {
	for _, p := range t.Envs {
		if p == env || (strings.HasSuffix(p, "/*") && strings.HasPrefix(env, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	ID     string `json:"id"`
	Ms     int64  `json:"ms"`
	Action string `json:"action"`
	Env    string `json:"env,omitempty"`
	Name   string `json:"name,omitempty"`
	Actor  string `json:"actor"`
}

func newID() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(base32.StdEncoding.EncodeToString(b))
}

func hashToken(secret string) string {
	s := sha256.Sum256([]byte("snugkv-secrets-token\x00" + secret))
	return hex.EncodeToString(s[:])
}

// ---- environments

func (m *Manager) envExists(env string) (bool, error) {
	v, err := m.kv.do("SISMEMBER", keyEnvs, env)
	return v.n == 1, err
}

func (m *Manager) createEnv(env string) (created bool, err error) {
	ok, err := m.envExists(env)
	if err != nil || ok {
		return false, err
	}
	n, err := m.kv.do("SCARD", keyEnvs)
	if err != nil {
		return false, err
	}
	if n.n >= maxEnvs {
		return false, errors.New("too many environments")
	}
	meta, _ := json.Marshal(map[string]int64{"createdMs": time.Now().UnixMilli()})
	if _, err := m.kv.do("HSETNX", keyEnvMeta, env, string(meta)); err != nil {
		return false, err
	}
	v, err := m.kv.do("SADD", keyEnvs, env)
	return v.n == 1, err
}

func (m *Manager) listEnvs() ([]EnvInfo, error) {
	v, err := m.kv.do("SMEMBERS", keyEnvs)
	if err != nil {
		return nil, err
	}
	out := make([]EnvInfo, 0, len(v.arr))
	for _, e := range v.arr {
		p, en, _ := strings.Cut(e.str, "/")
		info := EnvInfo{Name: e.str, Project: p, Env: en}
		if n, err := m.kv.do("HLEN", keyEnv+e.str); err == nil {
			info.Secrets = n.n
		}
		if meta, err := m.kv.do("HGET", keyEnvMeta, e.str); err == nil && !meta.isNil {
			var mm map[string]int64
			json.Unmarshal([]byte(meta.str), &mm)
			info.CreatedMs = mm["createdMs"]
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Manager) deleteEnv(env string) error {
	names, err := m.kv.do("HKEYS", keyEnv+env)
	if err != nil {
		return err
	}
	keys := []string{"DEL", keyEnv + env, keyVer + env}
	for _, n := range names.arr {
		keys = append(keys, keyHist+env+":"+n.str)
	}
	if _, err := m.kv.do(keys...); err != nil {
		return err
	}
	m.kv.do("HDEL", keyEnvMeta, env)
	_, err = m.kv.do("SREM", keyEnvs, env)
	return err
}

// ---- secrets

func (m *Manager) current(env, name string) (*record, error) {
	v, err := m.kv.do("HGET", keyEnv+env, name)
	if err != nil {
		return nil, err
	}
	if v.isNil {
		return nil, errNotFound
	}
	var r record
	if err := json.Unmarshal([]byte(v.str), &r); err != nil {
		return nil, errors.New("stored secret is corrupt")
	}
	return &r, nil
}

func (m *Manager) listSecrets(env string, reveal bool) ([]SecretInfo, error) {
	v, err := m.kv.do("HGETALL", keyEnv+env)
	if err != nil {
		return nil, err
	}
	out := make([]SecretInfo, 0, len(v.arr)/2)
	for i := 0; i+1 < len(v.arr); i += 2 {
		name := v.arr[i].str
		var r record
		if err := json.Unmarshal([]byte(v.arr[i+1].str), &r); err != nil {
			return nil, fmt.Errorf("stored secret %s is corrupt", name)
		}
		plain, err := m.sealer.open(r.Value, bind(env, name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		info := SecretInfo{Name: name, Hint: hint(plain), Version: r.Version, UpdatedMs: r.Ms, UpdatedBy: r.By}
		if reveal {
			info.Value = plain
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// values returns every secret of an environment in plain text.
func (m *Manager) values(env string) (map[string]string, error) {
	list, err := m.listSecrets(env, true)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, s := range list {
		out[s.Name] = s.Value
	}
	return out, nil
}

func (m *Manager) getSecret(env, name string) (string, *record, error) {
	r, err := m.current(env, name)
	if err != nil {
		return "", nil, err
	}
	plain, err := m.sealer.open(r.Value, bind(env, name))
	return plain, r, err
}

// setSecret stores a new version. It returns the version written, or 0 when
// the value is the same as the current one (nothing written).
func (m *Manager) setSecret(env, name, plain, actor string) (int64, error) {
	if len(plain) > maxValueLen {
		return 0, fmt.Errorf("a secret value can be at most %d bytes", maxValueLen)
	}
	prev, err := m.current(env, name)
	if err != nil && !errors.Is(err, errNotFound) {
		return 0, err
	}
	if prev != nil {
		if old, err := m.sealer.open(prev.Value, bind(env, name)); err == nil && old == plain {
			return 0, nil
		}
	} else {
		n, err := m.kv.do("HLEN", keyEnv+env)
		if err != nil {
			return 0, err
		}
		if n.n >= maxSecrets {
			return 0, errors.New("too many secrets in this environment")
		}
	}
	return m.write(env, name, plain, actor, prev)
}

func (m *Manager) write(env, name, plain, actor string, prev *record) (int64, error) {
	ver, err := m.kv.do("HINCRBY", keyVer+env, name, "1")
	if err != nil {
		return 0, err
	}
	enc, err := m.sealer.seal(plain, bind(env, name))
	if err != nil {
		return 0, err
	}
	r := record{Value: enc, Version: ver.n, Ms: time.Now().UnixMilli(), By: actor}
	b, _ := json.Marshal(r)
	if _, err := m.kv.do("HSET", keyEnv+env, name, string(b)); err != nil {
		return 0, err
	}
	m.pushHistory(env, name, r)
	return r.Version, nil
}

func (m *Manager) pushHistory(env, name string, r record) {
	b, _ := json.Marshal(r)
	k := keyHist + env + ":" + name
	m.kv.do("LPUSH", k, string(b))
	m.kv.do("LTRIM", k, "0", strconv.Itoa(histKeep-1))
}

func (m *Manager) deleteSecret(env, name, actor string) error {
	if _, err := m.current(env, name); err != nil {
		return err
	}
	ver, err := m.kv.do("HINCRBY", keyVer+env, name, "1")
	if err != nil {
		return err
	}
	if _, err := m.kv.do("HDEL", keyEnv+env, name); err != nil {
		return err
	}
	m.pushHistory(env, name, record{Version: ver.n, Ms: time.Now().UnixMilli(), By: actor, Deleted: true})
	return nil
}

func (m *Manager) history(env, name string) ([]record, error) {
	v, err := m.kv.do("LRANGE", keyHist+env+":"+name, "0", "-1")
	if err != nil {
		return nil, err
	}
	out := make([]record, 0, len(v.arr))
	for _, e := range v.arr {
		var r record
		if json.Unmarshal([]byte(e.str), &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *Manager) versions(env, name string) ([]VersionInfo, error) {
	hist, err := m.history(env, name)
	if err != nil {
		return nil, err
	}
	out := make([]VersionInfo, 0, len(hist))
	for _, r := range hist {
		vi := VersionInfo{Version: r.Version, Ms: r.Ms, By: r.By, Deleted: r.Deleted}
		if !r.Deleted {
			if p, err := m.sealer.open(r.Value, bind(env, name)); err == nil {
				vi.Hint = hint(p)
			}
		}
		out = append(out, vi)
	}
	return out, nil
}

// rollback makes an old version current again, as a new version.
func (m *Manager) rollback(env, name string, version int64, actor string) (int64, error) {
	hist, err := m.history(env, name)
	if err != nil {
		return 0, err
	}
	for _, r := range hist {
		if r.Version != version {
			continue
		}
		if r.Deleted {
			return 0, errors.New("that version is a deletion")
		}
		plain, err := m.sealer.open(r.Value, bind(env, name))
		if err != nil {
			return 0, err
		}
		prev, _ := m.current(env, name)
		return m.write(env, name, plain, actor, prev)
	}
	return 0, fmt.Errorf("version %d is not in the history (the last %d are kept)", version, histKeep)
}

// ---- tokens

func (m *Manager) createToken(name string, envs []string) (*Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, "", errors.New("a token needs a name (up to 64 characters)")
	}
	if len(envs) == 0 || len(envs) > 50 {
		return nil, "", errors.New("a token needs 1 to 50 environments")
	}
	for _, e := range envs {
		p, en, ok := strings.Cut(e, "/")
		if !ok || !partRe.MatchString(p) || (en != "*" && !partRe.MatchString(en)) {
			return nil, "", fmt.Errorf("%q is not an environment (project/env or project/*)", e)
		}
	}
	n, err := m.kv.do("HLEN", keyTokens)
	if err != nil {
		return nil, "", err
	}
	if n.n >= maxTokens {
		return nil, "", errors.New("too many tokens")
	}
	sb := make([]byte, 24)
	if _, err := rand.Read(sb); err != nil {
		return nil, "", err
	}
	t := &Token{ID: newID(), Name: name, Envs: envs, CreatedMs: time.Now().UnixMilli()}
	secret := hex.EncodeToString(sb)
	t.Hash = hashToken(secret)
	b, _ := json.Marshal(t)
	if _, err := m.kv.do("HSET", keyTokens, t.ID, string(b)); err != nil {
		return nil, "", err
	}
	t.Hash = ""
	return t, "sks_" + t.ID + "_" + secret, nil
}

func (m *Manager) listTokens() ([]Token, error) {
	v, err := m.kv.do("HGETALL", keyTokens)
	if err != nil {
		return nil, err
	}
	used, _ := m.kv.do("HGETALL", keyTokUsed)
	last := map[string]int64{}
	for i := 0; i+1 < len(used.arr); i += 2 {
		last[used.arr[i].str], _ = strconv.ParseInt(used.arr[i+1].str, 10, 64)
	}
	out := make([]Token, 0, len(v.arr)/2)
	for i := 0; i+1 < len(v.arr); i += 2 {
		var t Token
		if json.Unmarshal([]byte(v.arr[i+1].str), &t) != nil {
			continue
		}
		t.Hash, t.LastUsedMs = "", last[t.ID]
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedMs < out[j].CreatedMs })
	return out, nil
}

func (m *Manager) deleteToken(id string) error {
	v, err := m.kv.do("HDEL", keyTokens, id)
	if err != nil {
		return err
	}
	if v.n == 0 {
		return errNotFound
	}
	m.kv.do("HDEL", keyTokUsed, id)
	return nil
}

// tokenFor checks a presented read token ("sks_<id>_<secret>").
func (m *Manager) tokenFor(presented string) (*Token, bool) {
	rest, ok := strings.CutPrefix(presented, "sks_")
	if !ok {
		return nil, false
	}
	id, secret, ok := strings.Cut(rest, "_")
	if !ok || !idRe.MatchString(id) {
		return nil, false
	}
	v, err := m.kv.do("HGET", keyTokens, id)
	if err != nil || v.isNil {
		return nil, false
	}
	var t Token
	if json.Unmarshal([]byte(v.str), &t) != nil {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(hashToken(secret)), []byte(t.Hash)) != 1 {
		return nil, false
	}
	m.kv.do("HSET", keyTokUsed, id, strconv.FormatInt(time.Now().UnixMilli(), 10))
	return &t, true
}

// ---- audit

func (m *Manager) audit(action, env, name, actor string) {
	if _, err := m.kv.do("XADD", keyAudit, "MAXLEN", "~", strconv.Itoa(auditKeep), "*",
		"action", action, "env", env, "name", name, "actor", actor); err != nil {
		m.logf("audit write failed: %v", err)
	}
}

func (m *Manager) readAudit(limit int, env string) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	// Read more when filtering, so a busy other environment does not hide this one.
	count := limit
	if env != "" {
		count = min(limit*10, auditKeep)
	}
	v, err := m.kv.do("XREVRANGE", keyAudit, "+", "-", "COUNT", strconv.Itoa(count))
	if err != nil {
		return nil, err
	}
	out := []AuditEntry{}
	for _, e := range v.arr {
		if len(e.arr) != 2 {
			continue
		}
		a := AuditEntry{ID: e.arr[0].str}
		if ms, _, ok := strings.Cut(a.ID, "-"); ok {
			a.Ms, _ = strconv.ParseInt(ms, 10, 64)
		}
		f := e.arr[1].arr
		for i := 0; i+1 < len(f); i += 2 {
			switch f[i].str {
			case "action":
				a.Action = f[i+1].str
			case "env":
				a.Env = f[i+1].str
			case "name":
				a.Name = f[i+1].str
			case "actor":
				a.Actor = f[i+1].str
			}
		}
		if env != "" && a.Env != env {
			continue
		}
		out = append(out, a)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// checkKey makes sure the configured key is the one the stored data was
// written with, so a mistyped key fails at start instead of on first read.
func (m *Manager) checkKey() error {
	v, err := m.kv.do("GET", keyCheck)
	if err != nil {
		return err
	}
	if v.isNil {
		enc, err := m.sealer.seal("snugkv-secrets", "check")
		if err != nil {
			return err
		}
		_, err = m.kv.do("SET", keyCheck, enc, "NX")
		return err
	}
	if _, err := m.sealer.open(v.str, "check"); err != nil {
		return errors.New("the secrets key does not match the one the stored secrets were written with")
	}
	return nil
}
