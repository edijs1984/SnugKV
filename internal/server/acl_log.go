package server

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const aclLogMaxEntries = 128

type ACLLogEntry struct {
	Count int64

	Reason   string
	Context  string
	Object   string
	Username string

	ClientInfo string

	EntryID int64

	TimestampCreated     int64
	TimestampLastUpdated int64
}

type ACLLog struct {
	mu      sync.RWMutex
	entries []ACLLogEntry
	nextID  int64
}

func NewACLLog() *ACLLog {
	return &ACLLog{}
}

func (l *ACLLog) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = nil
}

func (l *ACLLog) Add(
	reason string,
	object string,
	username string,
	clientInfo string,
) {
	now := time.Now().UnixMilli()

	l.mu.Lock()
	defer l.mu.Unlock()

	// Redis aggregates equivalent ACL violations instead of creating a new
	// entry for every occurrence. The original entry ID and creation time are
	// retained, while count, last-updated and client-info are refreshed.
	for index := range l.entries {
		entry := &l.entries[index]

		if entry.Reason != reason ||
			entry.Context != "toplevel" ||
			entry.Object != object ||
			entry.Username != username {
			continue
		}

		entry.Count++
		entry.TimestampLastUpdated = now
		entry.ClientInfo = clientInfo

		// Redis presents the most recently updated ACL entry first.
		if index > 0 {
			updated := *entry

			copy(
				l.entries[1:index+1],
				l.entries[0:index],
			)

			l.entries[0] = updated
		}

		return
	}

	l.nextID++

	entry := ACLLogEntry{
		Count: 1,

		Reason:   reason,
		Context:  "toplevel",
		Object:   object,
		Username: username,

		ClientInfo: clientInfo,

		EntryID: l.nextID,

		TimestampCreated:     now,
		TimestampLastUpdated: now,
	}

	l.entries = append(
		[]ACLLogEntry{entry},
		l.entries...,
	)

	if len(l.entries) > aclLogMaxEntries {
		l.entries = l.entries[:aclLogMaxEntries]
	}
}

func (l *ACLLog) Entries(limit int64) []ACLLogEntry {
	if limit <= 0 {
		return nil
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	n := len(l.entries)

	if int64(n) > limit {
		n = int(limit)
	}

	out := make([]ACLLogEntry, n)
	copy(out, l.entries[:n])

	return out
}

func aclLogReply(entries []ACLLogEntry) []byte {
	items := make([][]byte, 0, len(entries))

	now := time.Now()

	for _, entry := range entries {
		age := float64(
			now.UnixMilli()-entry.TimestampLastUpdated,
		) / 1000

		if age < 0 {
			age = 0
		}

		ageString := strconv.FormatFloat(
			age,
			'f',
			3,
			64,
		)

		ageString = strings.TrimRight(ageString, "0")
		ageString = strings.TrimRight(ageString, ".")

		if ageString == "" {
			ageString = "0"
		}

		item := array(
			formatBulkString([]byte("count")),
			integer(entry.Count),

			formatBulkString([]byte("reason")),
			formatBulkString([]byte(entry.Reason)),

			formatBulkString([]byte("context")),
			formatBulkString([]byte(entry.Context)),

			formatBulkString([]byte("object")),
			formatBulkString([]byte(entry.Object)),

			formatBulkString([]byte("username")),
			formatBulkString([]byte(entry.Username)),

			formatBulkString([]byte("age-seconds")),
			formatBulkString([]byte(ageString)),

			formatBulkString([]byte("client-info")),
			formatBulkString([]byte(entry.ClientInfo)),

			formatBulkString([]byte("entry-id")),
			integer(entry.EntryID),

			formatBulkString([]byte("timestamp-created")),
			integer(entry.TimestampCreated),

			formatBulkString([]byte("timestamp-last-updated")),
			integer(entry.TimestampLastUpdated),
		)

		items = append(items, item)
	}

	return array(items...)
}

func aclLogClientInfo(
	client *clientSession,
	auth *authSession,
) string {
	if client == nil {
		return ""
	}

	snapshot := client.snapshot()

	username := "default"
	if auth != nil && auth.username != "" {
		username = auth.username
	}

	age := int64(time.Since(snapshot.createdAt).Seconds())
	if age < 0 {
		age = 0
	}

	idle := int64(time.Since(snapshot.lastSeen).Seconds())
	if idle < 0 {
		idle = 0
	}

	name := snapshot.name
	command := snapshot.lastCmd

	return fmt.Sprintf(
		"id=%d addr=%s laddr=%s fd=-1 name=%s age=%d idle=%d flags=N db=0 sub=0 psub=0 ssub=0 multi=-1 watch=0 qbuf=0 qbuf-free=0 argv-mem=0 multi-mem=0 rbs=0 rbp=0 obl=0 oll=0 omem=0 tot-mem=0 events=r cmd=%s user=%s redir=-1 resp=2 lib-name=%s lib-ver=%s io-thread=0 tot-net-in=0 tot-net-out=0 tot-cmds=0",
		snapshot.id,
		snapshot.remoteAddr,
		snapshot.localAddr,
		name,
		age,
		idle,
		command,
		username,
		snapshot.libName,
		snapshot.libVer,
	)
}
