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

// atomicScopedWriteCommands are the write commands whose key arguments are
// known to name every key they change. A transaction made only of these (and of
// reads) snapshots just those keys. Any other write command, including scripts,
// FLUSHALL and SORT STORE, makes the transaction snapshot the whole keyspace.
// Every name here is exercised by atomicTestCorpus in the tests, which compare
// the database before and after a rollback and the append-only file with the
// live store, so a command's key positions cannot drift unnoticed.
var atomicScopedWriteCommands = map[string]bool{
	"SET": true, "SETEX": true, "PSETEX": true, "SETNX": true, "GETSET": true,
	"GETDEL": true, "GETEX": true, "APPEND": true, "SETRANGE": true,
	"INCR": true, "DECR": true, "INCRBY": true, "DECRBY": true, "INCRBYFLOAT": true,
	"MSET": true, "MSETNX": true,
	"DEL": true, "UNLINK": true,
	"EXPIRE": true, "PEXPIRE": true, "EXPIREAT": true, "PEXPIREAT": true, "PERSIST": true,
	"RENAME": true, "RENAMENX": true, "COPY": true,
	"HSET": true, "HMSET": true, "HSETNX": true, "HDEL": true, "HINCRBY": true, "HINCRBYFLOAT": true,
	"LPUSH": true, "RPUSH": true, "LPUSHX": true, "RPUSHX": true, "LPOP": true, "RPOP": true,
	"LTRIM": true, "LSET": true, "LINSERT": true, "LREM": true, "LMOVE": true, "RPOPLPUSH": true,
	"SADD": true, "SREM": true, "SPOP": true, "SMOVE": true,
	"SINTERSTORE": true, "SUNIONSTORE": true, "SDIFFSTORE": true,
	"ZADD": true, "ZREM": true, "ZINCRBY": true, "ZPOPMIN": true, "ZPOPMAX": true,
	"ZREMRANGEBYSCORE": true, "ZREMRANGEBYRANK": true,
	"ZUNIONSTORE": true, "ZINTERSTORE": true, "ZDIFFSTORE": true,
	"XADD": true, "XDEL": true, "XTRIM": true, "XGROUP": true, "XREADGROUP": true,
	"SETBIT": true, "BITOP": true, "PFADD": true, "PFMERGE": true, "GEOADD": true,
	"JSON.SET": true, "JSON.DEL": true,
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
		if !info.write {
			continue
		}
		if !atomicScopedWriteCommands[name] {
			return nil, false
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
