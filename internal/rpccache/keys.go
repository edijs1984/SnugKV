package rpccache

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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

// Store layout (all in one Redis-protocol server; rate limits need SnugKV's
// snug_rate_limit function):
//
//	rp:<plan>          JSON Plan
//	rk:<id>            JSON KeyRecord; id is 32 hex digits of SHA-256(secret)
//	rl:<id>            token bucket owned by snug_rate_limit
//	ru:<id>:<YYYYMMDD> hash of usage counters for one UTC day
//
// The secret itself is never stored.
const (
	planPrefix  = "rp:"
	keyPrefix   = "rk:"
	limitPrefix = "rl:"
	usagePrefix = "ru:"

	usageTTL = 45 * 24 * time.Hour
)

// Plan is a set of limits a key can be attached to.
type Plan struct {
	// RPS is the sustained rate in JSON-RPC calls per second and Burst the
	// bucket size. A batch of n calls costs n and must not exceed Burst.
	RPS   float64 `json:"rps"`
	Burst int     `json:"burst"`
	// Daily is the number of calls allowed per UTC day; 0 means unlimited.
	Daily int64 `json:"daily"`
}

func (p Plan) validate() error {
	switch {
	case p.RPS <= 0:
		return errors.New("rps must be positive")
	case p.Burst < 1:
		return errors.New("burst must be at least 1")
	case p.Daily < 0:
		return errors.New("daily must not be negative")
	}
	return nil
}

// KeyRecord is the stored metadata for one API key.
type KeyRecord struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Plan    string `json:"plan"`
	Created int64  `json:"created"`
	Revoked bool   `json:"revoked,omitempty"`
}

var planName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// KeyID returns the store id for an API key secret.
func KeyID(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:16])
}

// Usage counter field names. calls is the total; the others split it by cache
// class. denied counts calls refused for rate or quota reasons and is not part
// of calls.
var usageFields = []string{"calls", "immutable", "static", "recent", "state", "tip", "bypass", "denied"}

func classField(c Class) string {
	switch c {
	case Immutable:
		return "immutable"
	case Static:
		return "static"
	case Recent:
		return "recent"
	case State:
		return "state"
	case Tip:
		return "tip"
	}
	return "bypass"
}

func usageKey(id, day string) string { return usagePrefix + id + ":" + day }

func dayOf(t time.Time) string { return t.UTC().Format("20060102") }

// Admin manages plans, keys and reads usage. It needs only a Client.
type Admin struct {
	c   *Client
	Now func() time.Time
}

func NewAdmin(c *Client) *Admin { return &Admin{c: c, Now: time.Now} }

func (a *Admin) SetPlan(name string, p Plan) error {
	if !planName.MatchString(name) {
		return fmt.Errorf("plan name %q: use letters, digits, '_', '.', '-' (up to 64)", name)
	}
	if err := p.validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(p)
	_, err := a.c.Do("SET", planPrefix+name, string(b))
	return err
}

func (a *Admin) GetPlan(name string) (Plan, bool, error) {
	v, err := a.c.Do("GET", planPrefix+name)
	if err != nil {
		return Plan{}, false, err
	}
	if v.Nil {
		return Plan{}, false, nil
	}
	var p Plan
	if err := json.Unmarshal([]byte(v.Str), &p); err != nil {
		return Plan{}, false, fmt.Errorf("plan %q is corrupt: %w", name, err)
	}
	return p, true, nil
}

func (a *Admin) Plans() (map[string]Plan, error) {
	names, err := a.scan(planPrefix + "*")
	if err != nil {
		return nil, err
	}
	out := make(map[string]Plan, len(names))
	for _, k := range names {
		name := strings.TrimPrefix(k, planPrefix)
		p, ok, err := a.GetPlan(name)
		if err != nil {
			return nil, err
		}
		if ok {
			out[name] = p
		}
	}
	return out, nil
}

// CreateKey makes a new key on an existing plan and returns the secret, which
// is shown once and not recoverable.
func (a *Admin) CreateKey(label, plan string) (secret string, rec KeyRecord, err error) {
	if _, ok, err := a.GetPlan(plan); err != nil {
		return "", rec, err
	} else if !ok {
		return "", rec, fmt.Errorf("plan %q does not exist; create it with: plan set", plan)
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", rec, err
	}
	secret = "rk_" + base64.RawURLEncoding.EncodeToString(raw[:])
	rec = KeyRecord{ID: KeyID(secret), Label: label, Plan: plan, Created: a.Now().Unix()}
	b, _ := json.Marshal(rec)
	if _, err := a.c.Do("SET", keyPrefix+rec.ID, string(b)); err != nil {
		return "", rec, err
	}
	return secret, rec, nil
}

func (a *Admin) GetKey(id string) (KeyRecord, bool, error) {
	v, err := a.c.Do("GET", keyPrefix+id)
	if err != nil {
		return KeyRecord{}, false, err
	}
	if v.Nil {
		return KeyRecord{}, false, nil
	}
	var rec KeyRecord
	if err := json.Unmarshal([]byte(v.Str), &rec); err != nil {
		return KeyRecord{}, false, fmt.Errorf("key %q is corrupt: %w", id, err)
	}
	return rec, true, nil
}

// Revoke disables a key. Running proxies notice within their record-cache TTL.
// The record and its usage history are kept.
func (a *Admin) Revoke(id string) error {
	rec, ok, err := a.GetKey(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no key %q", id)
	}
	rec.Revoked = true
	b, _ := json.Marshal(rec)
	_, err = a.c.Do("SET", keyPrefix+id, string(b))
	return err
}

// SetKeyPlan moves a key to another existing plan.
func (a *Admin) SetKeyPlan(id, plan string) error {
	rec, ok, err := a.GetKey(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no key %q", id)
	}
	if _, ok, err := a.GetPlan(plan); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("plan %q does not exist", plan)
	}
	rec.Plan = plan
	b, _ := json.Marshal(rec)
	_, err = a.c.Do("SET", keyPrefix+id, string(b))
	return err
}

func (a *Admin) Keys() ([]KeyRecord, error) {
	ids, err := a.scan(keyPrefix + "*")
	if err != nil {
		return nil, err
	}
	out := make([]KeyRecord, 0, len(ids))
	for _, k := range ids {
		rec, ok, err := a.GetKey(strings.TrimPrefix(k, keyPrefix))
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out, nil
}

// DayUsage is one day of counters.
type DayUsage struct {
	Day      string
	Counters map[string]int64
}

// Usage returns the last days days (today first) for a key.
func (a *Admin) Usage(id string, days int) ([]DayUsage, error) {
	if days < 1 {
		days = 1
	}
	now := a.Now()
	cmds := make([][]string, days)
	labels := make([]string, days)
	for i := 0; i < days; i++ {
		labels[i] = dayOf(now.AddDate(0, 0, -i))
		cmds[i] = []string{"HGETALL", usageKey(id, labels[i])}
	}
	vs, err := a.c.Pipeline(cmds)
	if err != nil {
		return nil, err
	}
	out := make([]DayUsage, days)
	for i, v := range vs {
		m := map[string]int64{}
		for j := 0; j+1 < len(v.Array); j += 2 {
			n, _ := strconv.ParseInt(v.Array[j+1].Str, 10, 64)
			m[v.Array[j].Str] = n
		}
		out[i] = DayUsage{Day: labels[i], Counters: m}
	}
	return out, nil
}

func (a *Admin) scan(pattern string) ([]string, error) {
	var out []string
	cursor := "0"
	for i := 0; i < 100000; i++ {
		v, err := a.c.Do("SCAN", cursor, "MATCH", pattern, "COUNT", "1000")
		if err != nil {
			return nil, err
		}
		if len(v.Array) != 2 {
			return nil, errors.New("unexpected SCAN reply")
		}
		cursor = v.Array[0].Str
		for _, k := range v.Array[1].Array {
			out = append(out, k.Str)
		}
		if cursor == "0" {
			break
		}
	}
	sort.Strings(out)
	// SCAN may return a key more than once.
	uniq := out[:0]
	for i, k := range out {
		if i == 0 || k != out[i-1] {
			uniq = append(uniq, k)
		}
	}
	return uniq, nil
}
