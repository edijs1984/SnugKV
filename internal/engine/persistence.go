package engine

import (
	"errors"
	"snugkv/internal/arena"
	"snugkv/internal/index"
	"snugkv/internal/persistence"
	"sort"
)

// Export returns logical records under a consistent all-shard snapshot.
// nil keys means the complete keyspace; explicit keys include deletion markers.
func (s *Store) Export(keys []string) []persistence.Record {
	unlock := s.lockAll()
	defer unlock()
	now := s.now()
	all := keys == nil
	if all {
		for i := range s.shards {
			for key := range s.shards[i].all() {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
	}
	records := make([]persistence.Record, 0, len(keys))
	seen := make(map[string]bool)
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		e, ok := s.shardFor(key).get(key)
		record := persistence.Record{Key: []byte(key)}
		if !ok || e.expired(now) {
			if all {
				continue
			}
			record.Deleted = true
		} else {
			record.Value = s.decode(e)
			if !e.expiresAt.IsZero() {
				record.ExpiresAtMS = e.expiresAt.UnixMilli()
			}
		}
		records = append(records, record)
	}
	return records
}

// Restore applies a logical batch atomically. force is reserved for rollback of
// previously admitted data after a failed durability write.
func (s *Store) Restore(records []persistence.Record, force bool) error {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Reset {
			s.resetForRecovery()
			records = records[i+1:]
			break
		}
	}
	for _, record := range records {
		if len(record.Value) > 32<<20 {
			return errors.New("ERR recovered value exceeds 32 MiB limit")
		}
	}
	unlock := s.lockAll()
	defer unlock()
	now := s.now()
	updates := make(map[string]entry)
	deletions := make(map[string]bool)
	for _, record := range records {
		key := string(record.Key)
		if record.Deleted || record.ExpiresAtMS != 0 && record.ExpiresAtMS <= now.UnixMilli() {
			deletions[key] = true
			delete(updates, key)
			continue
		}
		e := s.makeEntry(record.Value)
		if record.ExpiresAtMS != 0 {
			e.expiresAt = stamp(record.ExpiresAtMS)
		}
		updates[key] = e
		delete(deletions, key)
	}
	ordered := make([]string, 0, len(updates))
	for key := range updates {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	var before, after, extra, extraArena uint64
	allocations := make(map[*shard][]int)
	growth := make(map[*shard]int)
	for key := range deletions {
		sh := s.shardFor(key)
		if old, ok := sh.get(key); ok {
			before += entryCharge(key, old)
			growth[sh]--
		}
	}
	for _, key := range ordered {
		e := updates[key]
		sh := s.shardFor(key)
		if old, ok := sh.get(key); ok {
			before += entryCharge(key, old)
		} else {
			growth[sh]++
		}
		after += entryCharge(key, e)
		allocations[sh] = append(allocations[sh], len(e.value))
	}
	for sh, n := range growth {
		extra += sh.data.GrowthBytes(n)
	}
	for sh, lengths := range allocations {
		extraArena += sh.arena.GrowthFor(lengths)
	}
	s.memory.mu.Lock()
	next := s.memory.used - before + after + extra + extraArena
	if !force && s.memory.max > 0 && next > s.memory.max {
		s.memory.mu.Unlock()
		return ErrOOM
	}
	s.memory.mu.Unlock()
	for key := range deletions {
		s.remove(s.shardFor(key), key)
	}
	// All shards remain locked, so no concurrent reservation can change admission.
	for _, key := range ordered {
		e := updates[key]
		if err := s.publishRecord(s.shardFor(key), key, e, false); err != nil {
			return err
		}
	}
	return nil
}

// resetForRecovery is used only for the reset frame in a compacted AOF.
func (s *Store) resetForRecovery() {
	unlock := s.lockAll()
	defer unlock()
	for i := range s.shards {
		sh := &s.shards[i]
		for key := range sh.all() {
			s.remove(sh, key)
		}
		s.memory.mu.Lock()
		arenaBytes, indexBytes := sh.arena.MemoryBytes(), sh.data.CapacityBytes()
		s.memory.used -= arenaBytes + indexBytes
		s.memory.arenas -= arenaBytes
		s.memory.index -= indexBytes
		s.memory.mu.Unlock()
		sh.arena = arena.Arena{}
		sh.data = index.New[uint32]()
		sh.entries = nil
		sh.freeIDs = nil
		sh.expiration = expirationQueue{}
	}
}
func (s *Store) FlushDB() {
	s.resetForRecovery()
}
