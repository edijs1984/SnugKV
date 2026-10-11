package webhook

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"
)

const (
	keyIDs   = "snugkv:webhook:ids"
	keyDef   = "snugkv:webhook:def:"
	keyLog   = "snugkv:webhook:log:"
	keyStats = "snugkv:webhook:stats:"
	logKeep  = 200
)

// store keeps webhook definitions, delivery logs and counters in SnugKV
// itself, so they are persisted and replicated like any other data.
type store struct{ kv *kvClient }

func (s *store) list() ([]Definition, error) {
	v, err := s.kv.do("SMEMBERS", keyIDs)
	if err != nil {
		return nil, err
	}
	out := make([]Definition, 0, len(v.arr))
	for _, id := range v.arr {
		d, err := s.get(id.str)
		if err != nil {
			if errors.Is(err, errNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedMS < out[j].CreatedMS })
	return out, nil
}

var errNotFound = errors.New("webhook not found")

func (s *store) get(id string) (*Definition, error) {
	if !idRe.MatchString(id) {
		return nil, errNotFound
	}
	v, err := s.kv.do("GET", keyDef+id)
	if err != nil {
		return nil, err
	}
	if v.isNil {
		return nil, errNotFound
	}
	var d Definition
	if err := json.Unmarshal([]byte(v.str), &d); err != nil {
		return nil, errors.New("stored webhook is corrupt: " + err.Error())
	}
	return &d, nil
}

func (s *store) put(d *Definition) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if _, err := s.kv.do("SET", keyDef+d.ID, string(b)); err != nil {
		return err
	}
	_, err = s.kv.do("SADD", keyIDs, d.ID)
	return err
}

func (s *store) remove(id string) error {
	if _, err := s.kv.do("SREM", keyIDs, id); err != nil {
		return err
	}
	_, err := s.kv.do("DEL", keyDef+id, keyLog+id, keyStats+id)
	return err
}

// LogEntry is one line of a webhook's delivery log.
type LogEntry struct {
	TimeMS    int64  `json:"time"`
	Direction string `json:"direction"`           // in or out
	Result    string `json:"result"`              // ok, retry, failed, rejected
	Status    int    `json:"status,omitempty"`    // HTTP status
	Attempt   int    `json:"attempt,omitempty"`   // outbound attempt number
	MessageID string `json:"messageId,omitempty"` // stream entry id
	Millis    int64  `json:"ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// logOnly appends a line to the log without touching the counters.
func (s *store) logOnly(id string, e LogEntry) {
	if e.TimeMS == 0 {
		e.TimeMS = time.Now().UnixMilli()
	}
	b, _ := json.Marshal(e)
	s.kv.do("LPUSH", keyLog+id, string(b))
	s.kv.do("LTRIM", keyLog+id, "0", strconv.Itoa(logKeep-1))
}

// log appends a line and counts it.
func (s *store) log(id string, e LogEntry) {
	if e.TimeMS == 0 {
		e.TimeMS = time.Now().UnixMilli()
	}
	s.logOnly(id, e)
	field := "ok"
	switch e.Result {
	case "failed", "rejected":
		field = "failed"
	case "retry":
		field = "retries"
	}
	s.kv.do("HINCRBY", keyStats+id, field, "1")
	s.kv.do("HSET", keyStats+id, "last", strconv.FormatInt(e.TimeMS, 10))
}

func (s *store) readLog(id string, limit int) ([]LogEntry, error) {
	if limit < 1 || limit > logKeep {
		limit = 50
	}
	v, err := s.kv.do("LRANGE", keyLog+id, "0", strconv.Itoa(limit-1))
	if err != nil {
		return nil, err
	}
	out := make([]LogEntry, 0, len(v.arr))
	for _, item := range v.arr {
		var e LogEntry
		if json.Unmarshal([]byte(item.str), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// Stats are the counters of one webhook.
type Stats struct {
	OK      int64 `json:"ok"`
	Failed  int64 `json:"failed"`
	Retries int64 `json:"retries"`
	LastMS  int64 `json:"lastMs"`
}

func (s *store) stats(id string) Stats {
	v, err := s.kv.do("HGETALL", keyStats+id)
	var st Stats
	if err != nil {
		return st
	}
	for i := 0; i+1 < len(v.arr); i += 2 {
		n, _ := strconv.ParseInt(v.arr[i+1].str, 10, 64)
		switch v.arr[i].str {
		case "ok":
			st.OK = n
		case "failed":
			st.Failed = n
		case "retries":
			st.Retries = n
		case "last":
			st.LastMS = n
		}
	}
	return st
}
