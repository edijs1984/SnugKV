package server

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// respValue parses a raw RESP2 reply into string, int64, nil, error (type qErr) or []any.
type qErr string

func parseResp(t *testing.T, s string) any {
	t.Helper()
	v, rest := parseRespAt(t, s)
	if rest != "" {
		t.Fatalf("trailing data %q", rest)
	}
	return v
}

func parseRespAt(t *testing.T, s string) (any, string) {
	t.Helper()
	i := strings.Index(s, "\r\n")
	if i < 0 {
		t.Fatalf("bad frame %q", s)
	}
	head, rest := s[1:i], s[i+2:]
	switch s[0] {
	case '+':
		return head, rest
	case '-':
		return qErr(head), rest
	case ':':
		n, _ := strconv.ParseInt(head, 10, 64)
		return n, rest
	case '$':
		n, _ := strconv.Atoi(head)
		if n < 0 {
			return nil, rest
		}
		return rest[:n], rest[n+2:]
	case '*':
		n, _ := strconv.Atoi(head)
		if n < 0 {
			return nil, rest
		}
		out := make([]any, 0, n)
		for j := 0; j < n; j++ {
			var v any
			v, rest = parseRespAt(t, rest)
			out = append(out, v)
		}
		return out, rest
	}
	t.Fatalf("unknown frame %q", s)
	return nil, ""
}

type qt struct {
	t *testing.T
	c *atomicTestClient
	p string
}

func newQT(t *testing.T) *qt {
	return &qt{t: t, c: startBuiltinTestServer(t), p: "snugq:{t}:"}
}

func (q *qt) call(fn string, args ...string) any {
	q.t.Helper()
	a := append([]string{"FCALL", fn, "1", q.p}, args...)
	v := parseResp(q.t, q.c.do(a...))
	if e, ok := v.(qErr); ok {
		q.t.Fatalf("%s: %s", fn, e)
	}
	return v
}

func (q *qt) raw(args ...string) any {
	q.t.Helper()
	return parseResp(q.t, q.c.do(args...))
}

// jobArgs builds the 19 job arguments; overrides is "index=value" with 1-based index.
func jobArgs(name, data string, overrides ...string) []string {
	a := []string{name, data, "", "0", "0", "0", "1", "", "0", "-1", "0", "-1", "0", "", "", "", "0", "", "0"}
	for _, o := range overrides {
		i := strings.Index(o, "=")
		n, _ := strconv.Atoi(o[:i])
		a[n-1] = o[i+1:]
	}
	return a
}

func (q *qt) add(name, data string, overrides ...string) string {
	q.t.Helper()
	r := q.call("snugq_add", jobArgs(name, data, overrides...)...).([]any)
	return r[0].(string)
}

func (q *qt) fetch(token string) []any {
	q.t.Helper()
	return q.call("snugq_fetch", token, "30000").([]any)
}

func (q *qt) counts() map[string]int64 {
	q.t.Helper()
	r := q.call("snugq_counts").([]any)
	m := map[string]int64{}
	for i := 0; i < len(r); i += 2 {
		m[r[i].(string)] = r[i+1].(int64)
	}
	return m
}

func (q *qt) job(id string) map[string]string {
	q.t.Helper()
	r := q.raw("HGETALL", q.p+"job:"+id).([]any)
	m := map[string]string{}
	for i := 0; i+1 < len(r); i += 2 {
		m[r[i].(string)] = r[i+1].(string)
	}
	return m
}

func (q *qt) wantCounts(want map[string]int64) {
	q.t.Helper()
	got := q.counts()
	for k, v := range want {
		if got[k] != v {
			q.t.Fatalf("counts[%s] = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

func TestQueueLibraryLoads(t *testing.T) {
	q := newQT(t)
	if got := q.raw("FCALL_RO", "snugq_version", "0"); got != "1" {
		t.Fatalf("version = %v", got)
	}
	// The old builtin functions still work.
	if got := q.raw("FCALL_RO", "snug_functions_version", "0"); got != "1" {
		t.Fatalf("snug version = %v", got)
	}
}

func TestQueueFifoLifoPriorityAndComplete(t *testing.T) {
	q := newQT(t)
	a := q.add("job", "A")
	b := q.add("job", "B")
	c := q.add("job", "C", "6=1") // lifo
	d := q.add("job", "D", "5=2")
	e := q.add("job", "E", "5=1")
	q.wantCounts(map[string]int64{"wait": 3, "prioritized": 2})
	var order []string
	for i := 0; i < 5; i++ {
		r := q.fetch("w1")
		if r[0] != "job" {
			t.Fatalf("fetch %d = %v", i, r)
		}
		order = append(order, r[1].(string))
		if got := q.call("snugq_complete", r[1].(string), "w1", "\"ok\""); got != int64(0) {
			t.Fatalf("complete = %v", got)
		}
	}
	want := []string{c, a, b, e, d}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	q.wantCounts(map[string]int64{"completed": 5, "active": 0, "wait": 0})
	if j := q.job(a); j["rv"] != "\"ok\"" || j["atm"] != "1" || j["po"] == "" || j["fo"] == "" {
		t.Fatalf("job = %v", j)
	}
	r := q.fetch("w1")
	if r[0] != "empty" || r[1] != int64(-1) {
		t.Fatalf("empty fetch = %v", r)
	}
}

func TestQueueRetriesBackoffAndFailure(t *testing.T) {
	q := newQT(t)
	id := q.add("job", "x", "7=3", "8=fixed", "9=60")
	r := q.fetch("w")
	if got := q.call("snugq_fail", id, "w", "boom", "stack", "0", "-1"); got != "retry" {
		t.Fatalf("fail 1 = %v", got)
	}
	_ = r
	q.wantCounts(map[string]int64{"delayed": 1, "active": 0})
	if r := q.fetch("w"); r[0] != "empty" {
		t.Fatalf("delayed job fetched early: %v", r)
	}
	time.Sleep(120 * time.Millisecond)
	if r := q.fetch("w"); r[0] != "job" || r[1] != id {
		t.Fatalf("retry fetch = %v", r)
	}
	// Custom backoff of 0 puts it straight back.
	if got := q.call("snugq_fail", id, "w", "boom2", "stack", "0", "0"); got != "retry" {
		t.Fatalf("fail 2 = %v", got)
	}
	q.wantCounts(map[string]int64{"wait": 1})
	q.fetch("w")
	if got := q.call("snugq_fail", id, "w", "boom3", "stack", "0", "-1"); got != "failed" {
		t.Fatalf("fail 3 = %v", got)
	}
	q.wantCounts(map[string]int64{"failed": 1, "wait": 0, "delayed": 0})
	if j := q.job(id); j["fr"] != "boom3" || j["atm"] != "3" {
		t.Fatalf("job = %v", j)
	}
	// Wrong token.
	if got := q.raw("FCALL", "snugq_complete", "1", q.p, id, "nope", "x"); got != int64(-2) {
		t.Fatalf("wrong token = %v", got)
	}
	// Retry from failed.
	if got := q.call("snugq_retry", id, "failed", "1"); got != int64(0) {
		t.Fatalf("retry = %v", got)
	}
	q.wantCounts(map[string]int64{"wait": 1, "failed": 0})
	if j := q.job(id); j["atm"] != "0" || j["fr"] != "" {
		t.Fatalf("job after retry = %v", j)
	}
	// Unrecoverable errors skip remaining attempts.
	q.fetch("w")
	if got := q.call("snugq_fail", id, "w", "fatal", "", "1", "-1"); got != "failed" {
		t.Fatalf("final = %v", got)
	}
}

func TestQueueExponentialBackoff(t *testing.T) {
	q := newQT(t)
	id := q.add("job", "x", "7=4", "8=exponential", "9=200")
	q.fetch("w")
	q.call("snugq_fail", id, "w", "e", "", "0", "-1")
	score := q.raw("ZSCORE", q.p+"delayed", id).(string)
	f, _ := strconv.ParseFloat(score, 64)
	due := int64(f / 4096)
	now := time.Now().UnixMilli()
	if d := due - now; d < 100 || d > 220 {
		t.Fatalf("first backoff = %dms", d)
	}
	q.call("snugq_promote", id)
	q.fetch("w")
	q.call("snugq_fail", id, "w", "e", "", "0", "-1")
	f, _ = strconv.ParseFloat(q.raw("ZSCORE", q.p+"delayed", id).(string), 64)
	if d := int64(f/4096) - time.Now().UnixMilli(); d < 300 || d > 420 {
		t.Fatalf("second backoff = %dms", d)
	}
}

func TestQueueState(t *testing.T) {
	q := newQT(t)
	state := func(id string) any { return q.raw("FCALL_RO", "snugq_state", "1", q.p, id) }
	w := q.add("job", "x")
	d := q.add("job", "x", "4=100000")
	p := q.add("job", "x", "5=2")
	if state(w) != "wait" || state(d) != "delayed" || state(p) != "prioritized" || state("nope") != "unknown" {
		t.Fatalf("states: %v %v %v", state(w), state(d), state(p))
	}
	r := q.fetch("w")
	if state(r[1].(string)) != "active" {
		t.Fatalf("active state = %v", state(r[1].(string)))
	}
	q.call("snugq_complete", r[1].(string), "w", "1")
	if state(r[1].(string)) != "completed" {
		t.Fatalf("completed state = %v", state(r[1].(string)))
	}
}

func TestQueueCustomBackoffUsesWorkerDelay(t *testing.T) {
	q := newQT(t)
	id := q.add("job", "x", "7=3", "8=custom")
	q.fetch("w")
	q.call("snugq_fail", id, "w", "e", "", "0", "150")
	f, _ := strconv.ParseFloat(q.raw("ZSCORE", q.p+"delayed", id).(string), 64)
	if d := int64(f/4096) - time.Now().UnixMilli(); d < 50 || d > 170 {
		t.Fatalf("custom backoff = %dms", d)
	}
}

func TestQueueDelayDedupAndCustomIds(t *testing.T) {
	q := newQT(t)
	id := q.add("job", "x", "4=80")
	q.wantCounts(map[string]int64{"delayed": 1})
	if r := q.fetch("w"); r[0] != "empty" || r[1].(int64) <= 0 || r[1].(int64) > 80 {
		t.Fatalf("fetch before due = %v", r)
	}
	time.Sleep(120 * time.Millisecond)
	if r := q.fetch("w"); r[0] != "job" || r[1] != id {
		t.Fatalf("fetch after due = %v", r)
	}
	// Custom ids are deduplicated; integers are rejected.
	first := q.call("snugq_add", jobArgs("j", "1", "3=mine")...).([]any)
	again := q.call("snugq_add", jobArgs("j", "2", "3=mine")...).([]any)
	if first[0] != "mine" || first[1] != int64(1) || again[0] != "mine" || again[1] != int64(0) {
		t.Fatalf("custom ids: %v %v", first, again)
	}
	if got := q.raw(append([]string{"FCALL", "snugq_add", "1", q.p}, jobArgs("j", "1", "3=42")...)...); !strings.Contains(fmt.Sprint(got), "cannot be integers") {
		t.Fatalf("integer id = %v", got)
	}
	// Deduplication by key with a ttl.
	d1 := q.call("snugq_add", jobArgs("j", "1", "18=k", "19=200")...).([]any)
	d2 := q.call("snugq_add", jobArgs("j", "1", "18=k", "19=200")...).([]any)
	if d1[1] != int64(1) || d2[1] != int64(0) || d2[0] != d1[0] {
		t.Fatalf("dedup: %v %v", d1, d2)
	}
	time.Sleep(260 * time.Millisecond)
	d3 := q.call("snugq_add", jobArgs("j", "1", "18=k", "19=200")...).([]any)
	if d3[1] != int64(1) {
		t.Fatalf("dedup after ttl: %v", d3)
	}
}

func TestQueuePauseResumeAndGlobalConcurrency(t *testing.T) {
	q := newQT(t)
	a := q.add("job", "a")
	p := q.add("job", "p", "5=3")
	q.call("snugq_pause")
	b := q.add("job", "b")
	if r := q.fetch("w"); r[0] != "paused" {
		t.Fatalf("paused fetch = %v", r)
	}
	q.wantCounts(map[string]int64{"paused": 2})
	q.call("snugq_resume")
	q.wantCounts(map[string]int64{"paused": 0, "wait": 2, "prioritized": 1})
	got := []string{}
	for i := 0; i < 3; i++ {
		r := q.fetch(fmt.Sprint("w", i))
		if r[0] != "job" {
			if i == 2 && r[0] == "busy" {
				break
			}
			t.Fatalf("fetch = %v", r)
		}
		got = append(got, r[1].(string))
		if i == 1 {
			q.raw("HSET", q.p+"meta", "gconc", "2")
		}
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{a, b}) {
		t.Fatalf("order after resume = %v", got)
	}
	_ = p
	if r := q.fetch("w9"); r[0] != "busy" {
		t.Fatalf("global concurrency should block: %v", r)
	}
}

func TestQueueRateLimit(t *testing.T) {
	q := newQT(t)
	q.raw("HSET", q.p+"meta", "max", "2", "duration", "300")
	for i := 0; i < 3; i++ {
		q.add("job", fmt.Sprint(i))
	}
	for i := 0; i < 2; i++ {
		if r := q.fetch("w"); r[0] != "job" {
			t.Fatalf("fetch %d = %v", i, r)
		}
	}
	r := q.fetch("w")
	if r[0] != "limited" || r[1].(int64) <= 0 || r[1].(int64) > 300 {
		t.Fatalf("limited = %v", r)
	}
	time.Sleep(350 * time.Millisecond)
	if r := q.fetch("w"); r[0] != "job" {
		t.Fatalf("after window = %v", r)
	}
	// Manual rate limit via move_to_wait keeps the attempt count.
	id := q.add("job", "m")
	q.raw("DEL", q.p+"limiter")
	// drain the rest first
	for q.counts()["wait"] > 1 {
		q.raw("RPOP", q.p+"wait")
	}
	r = q.fetch("w")
	if r[0] != "job" {
		t.Fatalf("fetch = %v", r)
	}
	jid := r[1].(string)
	_ = id
	q.call("snugq_move_to_wait", jid, "w", "200")
	if j := q.job(jid); j["atm"] != "0" {
		t.Fatalf("attempt used: %v", j)
	}
	if r := q.fetch("w"); r[0] != "limited" {
		t.Fatalf("manual limit = %v", r)
	}
}

func TestQueueStalledJobs(t *testing.T) {
	q := newQT(t)
	id := q.add("job", "x")
	q.call("snugq_fetch", "w", "50")
	time.Sleep(90 * time.Millisecond) // lock expires
	r := q.call("snugq_stalled", "1", "10").([]any)
	if fmt.Sprint(r[0]) != fmt.Sprint([]any{id}) || len(r[1].([]any)) != 0 {
		t.Fatalf("stalled = %v", r)
	}
	q.wantCounts(map[string]int64{"wait": 1, "active": 0})
	// The check is gated by its interval.
	if r := q.call("snugq_stalled", "1", "10000").([]any); len(r[0].([]any)) != 0 {
		t.Fatalf("gated = %v", r)
	}
	time.Sleep(20 * time.Millisecond)
	q.call("snugq_fetch", "w", "50")
	time.Sleep(90 * time.Millisecond)
	q.raw("DEL", q.p+"stalled-check")
	r = q.call("snugq_stalled", "1", "10").([]any)
	if fmt.Sprint(r[1]) != fmt.Sprint([]any{id}) {
		t.Fatalf("expected failure after max stalls: %v", r)
	}
	if j := q.job(id); j["fr"] != "job stalled more than allowable limit" {
		t.Fatalf("job = %v", j)
	}
	// extend_lock keeps a job alive
	id2 := q.add("job", "y")
	q.call("snugq_fetch", "w", "60")
	if got := q.call("snugq_extend_lock", id2, "w", "5000"); got != int64(1) {
		t.Fatalf("extend = %v", got)
	}
	time.Sleep(90 * time.Millisecond)
	q.raw("DEL", q.p+"stalled-check")
	if r := q.call("snugq_stalled", "1", "10").([]any); len(r[0].([]any)) != 0 {
		t.Fatalf("extended job stalled: %v", r)
	}
	if got := q.call("snugq_extend_lock", id2, "other", "5000"); got != int64(0) {
		t.Fatalf("extend with wrong token = %v", got)
	}
}

func TestQueueRemoveOnCompleteAndFail(t *testing.T) {
	q := newQT(t)
	keep := []string{"10=2", "12=1"} // keep 2 completed, 1 failed
	var ids []string
	for i := 0; i < 4; i++ {
		ids = append(ids, q.add("job", "x", keep...))
		r := q.fetch("w")
		q.call("snugq_complete", r[1].(string), "w", "1")
	}
	q.wantCounts(map[string]int64{"completed": 2})
	if q.raw("EXISTS", q.p+"job:"+ids[0]) != int64(0) || q.raw("EXISTS", q.p+"job:"+ids[3]) != int64(1) {
		t.Fatal("oldest completed job should be trimmed")
	}
	id := q.add("job", "x", "10=0", "12=0")
	r := q.fetch("w")
	q.call("snugq_complete", r[1].(string), "w", "1")
	if q.raw("EXISTS", q.p+"job:"+id) != int64(0) {
		t.Fatal("removeOnComplete=0 should delete the job")
	}
	id = q.add("job", "x", "12=0")
	q.fetch("w")
	q.call("snugq_fail", id, "w", "e", "", "0", "-1")
	if q.raw("EXISTS", q.p+"job:"+id) != int64(0) || q.counts()["failed"] != 0 {
		t.Fatal("removeOnFail=0 should delete the job")
	}
}

func TestQueueFlowParentChildren(t *testing.T) {
	q := newQT(t)
	parent := q.add("parent", "P", "17=1")
	q.wantCounts(map[string]int64{"waiting-children": 1})
	c1 := q.add("child", "1", "14="+q.p, "15="+parent)
	c2 := q.add("child", "2", "14="+q.p, "15="+parent, "16=ignore", "7=1")
	if j := q.job(parent); j["pend"] != "2" {
		t.Fatalf("pending = %v", j)
	}
	r := q.fetch("w")
	q.call("snugq_complete", r[1].(string), "w", "\"r1\"")
	q.wantCounts(map[string]int64{"waiting-children": 1})
	r = q.fetch("w")
	if r[1] != c2 {
		t.Fatalf("second child = %v", r)
	}
	q.call("snugq_fail", c2, "w", "bad", "", "0", "-1")
	q.wantCounts(map[string]int64{"waiting-children": 0, "wait": 1})
	pr := q.fetch("w")
	if pr[1] != parent {
		t.Fatalf("parent not released: %v", pr)
	}
	if got := q.raw("HGETALL", q.p+"job:"+parent+":processed"); fmt.Sprint(got) != fmt.Sprint([]any{q.p + "|" + c1, "\"r1\""}) {
		t.Fatalf("processed = %v", got)
	}
	if got := q.raw("HGET", q.p+"job:"+parent+":failed", q.p+"|"+c2); got != "bad" {
		t.Fatalf("failed children = %v", got)
	}

	// fail-parent-on-failure
	parent2 := q.add("parent", "P2", "17=1")
	child := q.add("child", "x", "14="+q.p, "15="+parent2, "16=fail")
	for {
		r := q.fetch("w")
		if r[0] != "job" {
			break
		}
		if r[1] == child {
			q.call("snugq_fail", child, "w", "oops", "", "0", "-1")
		} else {
			q.call("snugq_complete", r[1].(string), "w", "1")
		}
	}
	if j := q.job(parent2); !strings.Contains(j["fr"], "oops") {
		t.Fatalf("parent2 = %v", j)
	}
	q.wantCounts(map[string]int64{"waiting-children": 0})

	// Removing a parent removes its unresolved children.
	parent3 := q.add("parent", "P3", "17=1")
	child3 := q.add("child", "x", "14="+q.p, "15="+parent3)
	if got := q.call("snugq_remove", parent3); got != int64(1) {
		t.Fatalf("remove parent = %v", got)
	}
	if q.raw("EXISTS", q.p+"job:"+child3) != int64(0) {
		t.Fatal("child should be removed with its parent")
	}
}

func TestQueueSchedulerEvery(t *testing.T) {
	q := newQT(t)
	args := append([]string{"s1", "60", "", "0", "0", "0"}, jobArgs("tick", "d")...)
	if got := q.call("snugq_upsert_scheduler", args...); got != int64(1) {
		t.Fatalf("upsert = %v", got)
	}
	r := q.fetch("w")
	if r[0] != "job" || !strings.HasPrefix(r[1].(string), "repeat:s1:") {
		t.Fatalf("first run = %v", r)
	}
	q.call("snugq_complete", r[1].(string), "w", "1")
	// Starting the first job scheduled the second.
	q.wantCounts(map[string]int64{"delayed": 1})
	time.Sleep(130 * time.Millisecond)
	r = q.fetch("w")
	if r[0] != "job" || r[1] == "" {
		t.Fatalf("second run = %v", r)
	}
	if got := q.raw("HGET", q.p+"repeat:s1", "count"); got.(string) < "2" {
		t.Fatalf("count = %v", got)
	}
	if got := q.call("snugq_remove_scheduler", "s1"); got != int64(1) {
		t.Fatalf("remove = %v", got)
	}
	q.call("snugq_complete", r[1].(string), "w", "1")
	q.wantCounts(map[string]int64{"delayed": 0})
	if q.raw("EXISTS", q.p+"repeat:s1") != int64(0) {
		t.Fatal("scheduler still exists")
	}
}

func TestQueueSchedulerPatternAndLimit(t *testing.T) {
	q := newQT(t)
	next := strconv.FormatInt(time.Now().UnixMilli()+30, 10)
	args := append([]string{"c1", "0", "*/1 * * * *", next, "2", "0"}, jobArgs("cron", "d")...)
	q.call("snugq_upsert_scheduler", args...)
	time.Sleep(60 * time.Millisecond)
	r := q.fetch("w")
	if r[0] != "job" {
		t.Fatalf("fetch = %v", r)
	}
	h := r[2].([]any)
	foundPat := false
	for i := 0; i+1 < len(h); i += 2 {
		if h[i] == "pat" && h[i+1] == "*/1 * * * *" {
			foundPat = true
		}
	}
	if !foundPat {
		t.Fatalf("job carries no pattern: %v", h)
	}
	// Pattern schedulers do not self-schedule; the worker asks for the next run.
	q.wantCounts(map[string]int64{"delayed": 0})
	n2 := strconv.FormatInt(time.Now().UnixMilli()+5000, 10)
	if got := q.call("snugq_scheduler_next", "c1", n2); got != int64(1) {
		t.Fatalf("next = %v", got)
	}
	if got := q.call("snugq_scheduler_next", "c1", n2); got != int64(0) {
		t.Fatalf("same run twice = %v", got)
	}
	// limit=2 reached
	n3 := strconv.FormatInt(time.Now().UnixMilli()+9000, 10)
	if got := q.call("snugq_scheduler_next", "c1", n3); got != int64(0) {
		t.Fatalf("beyond limit = %v", got)
	}
}

func TestQueueControlOperations(t *testing.T) {
	q := newQT(t)
	d := q.add("job", "d", "4=100000")
	w := q.add("job", "w")
	pr := q.add("job", "p", "5=5")
	if got := q.call("snugq_promote", d); got != int64(0) {
		t.Fatalf("promote = %v", got)
	}
	q.wantCounts(map[string]int64{"delayed": 0, "wait": 2})
	if got := q.call("snugq_promote", d); got != int64(-3) {
		t.Fatalf("promote twice = %v", got)
	}
	if got := q.call("snugq_change_priority", w, "9", "0"); got != int64(0) {
		t.Fatalf("change priority = %v", got)
	}
	q.wantCounts(map[string]int64{"wait": 1, "prioritized": 2})
	// jobs listing
	rows := q.call("snugq_jobs", "prioritized", "0", "-1", "1").([]any)
	if len(rows) != 2 || rows[0].([]any)[0] != pr || rows[1].([]any)[0] != w {
		t.Fatalf("prioritized = %v", rows)
	}
	rows = q.call("snugq_jobs", "wait", "0", "-1", "1").([]any)
	if len(rows) != 1 || rows[0].([]any)[0] != d {
		t.Fatalf("wait = %v", rows)
	}
	// progress, data and logs
	q.call("snugq_progress", w, "42")
	q.call("snugq_update_data", w, "newdata")
	q.call("snugq_log", w, "one", "2")
	q.call("snugq_log", w, "two", "2")
	if n := q.call("snugq_log", w, "three", "2"); n != int64(2) {
		t.Fatalf("log len = %v", n)
	}
	if j := q.job(w); j["prg"] != "42" || j["data"] != "newdata" {
		t.Fatalf("job = %v", j)
	}
	// locked jobs cannot be removed
	r := q.fetch("w")
	if got := q.call("snugq_remove", r[1].(string)); got != int64(-1) {
		t.Fatalf("remove locked = %v", got)
	}
	// clean, drain, obliterate
	q.call("snugq_complete", r[1].(string), "w", "1")
	if got := q.call("snugq_clean", "completed", "0", "0").([]any); len(got) != 1 {
		t.Fatalf("clean = %v", got)
	}
	if got := q.call("snugq_drain", "1"); got != int64(2) {
		t.Fatalf("drain = %v", got)
	}
	q.wantCounts(map[string]int64{"wait": 0, "prioritized": 0, "completed": 0})
	for i := 0; i < 5; i++ {
		q.add("job", "x")
	}
	q.fetch("w")
	if got := q.call("snugq_obliterate", "0", "100"); got != int64(-1) {
		t.Fatalf("obliterate active = %v", got)
	}
	steps := 0
	for q.call("snugq_obliterate", "1", "2") == int64(1) {
		steps++
		if steps > 10 {
			t.Fatal("obliterate does not finish")
		}
	}
	if keys := q.raw("KEYS", "snugq:*").([]any); len(keys) != 0 {
		t.Fatalf("keys left: %v", keys)
	}
}

func TestQueueEventsAndRegistry(t *testing.T) {
	q := newQT(t)
	id := q.add("job", "x")
	r := q.fetch("w")
	q.call("snugq_complete", r[1].(string), "w", "1")
	if got := q.raw("SMEMBERS", "snugq:queues"); fmt.Sprint(got) != "[t]" {
		t.Fatalf("registry = %v", got)
	}
	ev := q.raw("XRANGE", q.p+"events", "-", "+").([]any)
	var names []string
	for _, e := range ev {
		f := e.([]any)[1].([]any)
		names = append(names, f[1].(string))
	}
	want := "added,waiting,active,completed"
	if strings.Join(names, ",") != want {
		t.Fatalf("events = %v (job %s)", names, id)
	}
	if q.raw("GET", q.p+"metrics:completed:total") != "1" {
		t.Fatal("metrics total missing")
	}
}
