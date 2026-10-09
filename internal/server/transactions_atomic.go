package server

import (
	"fmt"
	"sort"
	"strings"
)

// atomicRejectedCommands cannot run inside MULTI ATOMIC because their effect
// is not data in the keyspace: it either leaves the process immediately
// (PUBLISH), changes server, connection or cluster state, or rewrites storage
// outside the transaction's key set. Rejecting them when they are queued keeps
// the all-or-nothing promise honest instead of silently partial.
var atomicRejectedCommands = map[string]bool{
	"PUBLISH": true, "SPUBLISH": true,
	"WAIT": true, "WAITAOF": true,
	"CONFIG": true, "ACL": true, "CLIENT": true, "DEBUG": true,
	"FUNCTION": true, "SCRIPT": true,
	"SHUTDOWN": true, "REPLICAOF": true, "SLAVEOF": true, "FAILOVER": true,
	"SAVE": true, "BGSAVE": true, "BGREWRITEAOF": true,
	"MIGRATE": true, "CLUSTER": true, "SWAPDB": true,
	"MONITOR": true, "AUTH": true, "HELLO": true,
	"SNUG.AOFREWRITE": true, "SNUG.COMPACT": true,
	"SNUG.FAILOVER": true, "SNUG.POLICY": true,
	"FT.ALIASADD": true, "FT.ALIASDEL": true, "FT.ALIASUPDATE": true,
	"FT.ALTER": true, "FT.CONFIG": true, "FT.CREATE": true,
	"FT.DICTADD": true, "FT.DICTDEL": true, "FT.DROPINDEX": true,
	"FT.SYNUPDATE": true, "FT.CURSOR": true,
	"FT.SUGADD": true, "FT.SUGDEL": true,
}

// atomicFullSnapshotCommands may write keys that their arguments do not name
// (scripts touch any key, FLUSH* touches all of them), so a transaction that
// contains one is snapshotted as a whole instead of per key.
var atomicFullSnapshotCommands = map[string]bool{
	"EVAL": true, "EVALSHA": true, "FCALL": true,
	"FLUSHALL": true, "FLUSHDB": true,
	"SORT": true,
}

func atomicQueueValidation(args [][]byte) error {
	if len(args) == 0 {
		return nil
	}
	cmd := strings.ToUpper(string(args[0]))
	if atomicRejectedCommands[cmd] {
		return fmt.Errorf("ERR command '%s' is not allowed inside MULTI ATOMIC", strings.ToLower(cmd))
	}
	return nil
}

// atomicSnapshotKeys returns the keys an atomic transaction can change. scoped
// is false when the transaction has to be snapshotted as a whole.
func atomicSnapshotKeys(commands [][][]byte) (keys []string, scoped bool) {
	seen := make(map[string]struct{})
	for _, command := range commands {
		if len(command) == 0 {
			continue
		}
		name := strings.ToUpper(string(command[0]))
		info, known := commandTable[name]
		if !known {
			return nil, false
		}
		if atomicFullSnapshotCommands[name] {
			return nil, false
		}
		if !info.write {
			continue
		}
		refs, err := commandKeys(command)
		if err != nil || len(refs) == 0 {
			return nil, false
		}
		for _, ref := range refs {
			key := string(ref.value)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, true
}

func atomicAbortError(index int, command []byte, cause error) error {
	return fmt.Errorf(
		"EXECABORT Atomic transaction rolled back: command %d (%s) failed: %s",
		index+1, strings.ToUpper(string(command)), cause.Error(),
	)
}
