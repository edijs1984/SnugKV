package engine

import (
	"errors"

	"snugkv/internal/persistence"
)

// A StreamTx watches the streams one command may change. Afterwards it can
// describe the change as small persistence records (instead of exporting every
// touched stream whole), and it can undo the change if writing those records
// fails. Only one StreamTx exists at a time; the server serializes durable
// writes.
type StreamTx struct {
	s     *Store
	keys  []string
	items map[string]*streamTxItem
}

type streamTxItem struct {
	existed   bool
	body      *streamBody
	j         *bodyJournal
	memBefore uint64

	// Stored value (consumer groups) before the command first changed it.
	preStub    []byte
	preExpires stamp
	hasPreStub bool

	// Packed groups after the command's last change to them.
	groups    []byte
	groupsSet bool

	// The key was removed or replaced by something other than this command's
	// own bookkeeping, so a delta cannot describe it.
	unsafe bool
}

// bodyJournal records the changes made to one entry log.
type bodyJournal struct {
	appended      []byte
	appendedCount int
	through       StreamID
	hasThrough    bool
	deleted       []StreamID

	mark    streamAppendMark
	hasMark bool
	snap    *streamBodySnapshot
}

func (j *bodyJournal) noteAppend(b *streamBody, id StreamID, fields []StreamField) {
	if !j.hasMark && j.snap == nil {
		j.mark = b.mark()
		j.hasMark = true
	}
	j.appended = appendStreamEntryBytes(j.appended, id, fields)
	j.appendedCount++
}

func (j *bodyJournal) beforeDestructive(b *streamBody) {
	if j.snap == nil {
		j.snap = b.snapshot()
	}
}

func (j *bodyJournal) noteThrough(id StreamID) {
	if !j.hasThrough || j.through.less(id) {
		j.through, j.hasThrough = id, true
	}
}

func (j *bodyJournal) touched() bool {
	return j.appendedCount > 0 || j.hasThrough || len(j.deleted) > 0
}

// BeginStreamTx starts watching keys. It reports false when a delta could not
// describe the command: another transaction is active, or a key currently holds
// an expired value or something that is not a stream.
func (s *Store) BeginStreamTx(keys []string) (*StreamTx, bool) {
	if s.streamTx.Load() != nil {
		return nil, false
	}
	tx := &StreamTx{s: s, items: make(map[string]*streamTxItem, len(keys))}
	now := s.now()
	for _, key := range keys {
		if _, dup := tx.items[key]; dup {
			continue
		}
		sh := s.shardFor(key)
		sh.mu.Lock()
		e, ok := sh.get(key)
		item := &streamTxItem{}
		if ok {
			body := s.streamLogs.get(key)
			if sh.expired(key, e, now) || e.valueType != TypeStream || body == nil {
				sh.mu.Unlock()
				tx.End()
				return nil, false
			}
			item.existed, item.body = true, body
			item.memBefore = body.memory()
			item.j = &bodyJournal{}
			body.j = item.j
		}
		sh.mu.Unlock()
		tx.items[key] = item
		tx.keys = append(tx.keys, key)
	}
	if !s.streamTx.CompareAndSwap(nil, tx) {
		tx.End()
		return nil, false
	}
	return tx, true
}

// End stops watching. It must be called once the command and its journal write
// are finished, whatever the outcome.
func (tx *StreamTx) End() {
	for _, key := range tx.keys {
		item := tx.items[key]
		if item == nil || item.body == nil {
			continue
		}
		sh := tx.s.shardFor(key)
		sh.mu.Lock()
		if item.body.j == item.j {
			item.body.j = nil
			if tx.s.streamLogs.get(key) == item.body {
				before := item.body.memory()
				item.body.settle()
				tx.s.settleStreamMemory(before, item.body.memory())
			}
		}
		sh.mu.Unlock()
	}
	tx.s.streamTx.CompareAndSwap(tx, nil)
}

func (s *Store) streamTxItem(key string) *streamTxItem {
	tx := s.streamTx.Load()
	if tx == nil {
		return nil
	}
	return tx.items[key]
}

// noteStreamRemoved is called when a key is removed while a transaction is
// active.
func (s *Store) noteStreamRemoved(key string) {
	if item := s.streamTxItem(key); item != nil {
		item.unsafe = true
	}
}

// noteStreamGroups records the packed groups a command just stored.
func (s *Store) noteStreamGroups(key string, groups []streamGroup) {
	item := s.streamTxItem(key)
	if item == nil {
		return
	}
	packed, err := appendStreamGroups(nil, groups)
	if err != nil {
		item.unsafe = true
		return
	}
	item.groups, item.groupsSet = packed, true
}

// notePreStub remembers the stored value before its first change.
func (s *Store) notePreStub(sh *shard, key string, old entry) {
	item := s.streamTxItem(key)
	if item == nil || !item.existed || item.hasPreStub {
		return
	}
	item.preStub = append([]byte(nil), sh.encoded(old)...)
	item.preExpires = sh.expirationAt(key, old)
	item.hasPreStub = true
}

func idPair(id StreamID) [2]uint64 { return [2]uint64{id.Millis, id.Sequence} }

// Records describes the command's changes. It reports false when some change
// cannot be described as a delta; the caller then exports the keys whole.
func (tx *StreamTx) Records() ([]persistence.Record, bool) {
	var out []persistence.Record
	for _, key := range tx.keys {
		item := tx.items[key]
		if item.unsafe {
			return nil, false
		}
		sh := tx.s.shardFor(key)
		sh.mu.RLock()
		e, ok := sh.get(key)
		body := tx.s.streamLogs.get(key)
		sh.mu.RUnlock()
		if !ok {
			if item.existed {
				return nil, false
			}
			continue
		}
		if e.valueType != TypeStream || body == nil || item.existed && body != item.body {
			return nil, false
		}
		d := &persistence.StreamDelta{
			LastID: idPair(body.LastID), MaxDeletedID: idPair(body.MaxDeletedID), EntriesAdded: body.EntriesAdded,
		}
		if !item.existed {
			d.Create = true
			d.Append = body.packedEntries(nil)
			d.AppendCount = body.Len()
			d.Groups, d.GroupsSet = item.groups, true
			if d.Groups == nil {
				d.Groups = []byte{0}
			}
		} else {
			j := item.j
			changed := j.touched() || item.groupsSet
			if !changed {
				continue
			}
			d.Append, d.AppendCount = j.appended, j.appendedCount
			if j.hasThrough {
				d.RemoveThrough, d.HasRemoveThrough = idPair(j.through), true
			}
			for _, id := range j.deleted {
				d.Delete = appendStreamID(d.Delete, id)
			}
			if item.groupsSet {
				if ops, ok := item.groupOpsFor(); ok {
					d.GroupOps = ops
				} else {
					d.Groups, d.GroupsSet = item.groups, true
				}
			}
			if !j.touched() && len(d.GroupOps) == 0 && !d.GroupsSet {
				continue
			}
		}
		out = append(out, persistence.Record{Key: []byte(key), ValueType: uint8(TypeStream), Stream: d})
	}
	return out, true
}

var errStreamRollback = errors.New("ERR stream change cannot be undone")

// Rollback undoes everything the command did to the watched streams.
func (tx *StreamTx) Rollback() error {
	s := tx.s
	for i := len(tx.keys) - 1; i >= 0; i-- {
		key := tx.keys[i]
		item := tx.items[key]
		if item.unsafe {
			return errStreamRollback
		}
		sh := s.shardFor(key)
		sh.mu.Lock()
		err := s.rollbackStreamItem(sh, key, item)
		sh.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) rollbackStreamItem(sh *shard, key string, item *streamTxItem) error {
	if !item.existed {
		if _, ok := sh.get(key); ok {
			s.remove(sh, key)
		}
		return nil
	}
	body := s.streamLogs.get(key)
	if body != item.body {
		return errStreamRollback
	}
	j := item.j
	if j.touched() {
		before := body.memory()
		body.j = nil
		if j.snap != nil {
			body.restoreSnapshot(j.snap)
		}
		if j.hasMark {
			body.rollback(j.mark)
		}
		body.j = j
		s.settleStreamMemory(before, body.memory())
	}
	if item.hasPreStub {
		prepared := streamPreparedEntry(item.preStub)
		prepared.keepStream = true
		prepared.expiresAt = item.preExpires
		if err := s.publishRecord(sh, key, prepared, allowOverMemoryLimit); err != nil {
			return err
		}
	}
	return nil
}

// diffStreamGroups lists what changed between two group lists, one entry per
// group that differs.
func diffStreamGroups(before, after []streamGroup) []persistence.GroupDelta {
	old := make(map[string]*streamGroup, len(before))
	for i := range before {
		old[before[i].Name] = &before[i]
	}
	var out []persistence.GroupDelta
	seen := make(map[string]bool, len(after))
	for i := range after {
		g := &after[i]
		seen[g.Name] = true
		prev := old[g.Name]
		d := persistence.GroupDelta{Name: g.Name}
		if prev == nil {
			d.Set = true
			prev = &streamGroup{}
		} else if prev.LastDeliveredID != g.LastDeliveredID || prev.EntriesRead != g.EntriesRead {
			d.Set = true
		}
		if d.Set {
			d.LastDelivered, d.EntriesRead = idPair(g.LastDeliveredID), g.EntriesRead
		}
		prevConsumers := make(map[string]streamConsumer, len(prev.Consumers))
		for _, c := range prev.Consumers {
			prevConsumers[c.Name] = c
		}
		nowConsumers := make(map[string]bool, len(g.Consumers))
		for _, c := range g.Consumers {
			nowConsumers[c.Name] = true
			if p, ok := prevConsumers[c.Name]; !ok || p != c {
				d.Consumers = append(d.Consumers, persistence.ConsumerState{Name: c.Name, SeenAt: c.SeenAt, ActiveAt: c.ActiveAt})
			}
		}
		for _, c := range prev.Consumers {
			if !nowConsumers[c.Name] {
				d.DelConsumers = append(d.DelConsumers, c.Name)
			}
		}
		prevPending := make(map[StreamID]streamPending, len(prev.Pending))
		for _, p := range prev.Pending {
			prevPending[p.ID] = p
		}
		nowPending := make(map[StreamID]bool, len(g.Pending))
		for _, p := range g.Pending {
			nowPending[p.ID] = true
			if q, ok := prevPending[p.ID]; !ok || q != p {
				d.Pending = append(d.Pending, persistence.PendingState{
					ID: idPair(p.ID), Consumer: p.Consumer, DeliveredAt: p.DeliveredAt, Deliveries: p.Deliveries,
				})
			}
		}
		for _, p := range prev.Pending {
			if !nowPending[p.ID] {
				d.DelPending = append(d.DelPending, idPair(p.ID))
			}
		}
		if d.Set || len(d.Consumers) > 0 || len(d.DelConsumers) > 0 || len(d.Pending) > 0 || len(d.DelPending) > 0 {
			out = append(out, d)
		}
	}
	for i := range before {
		if !seen[before[i].Name] {
			out = append(out, persistence.GroupDelta{Name: before[i].Name, Remove: true})
		}
	}
	return out
}

// groupOpsFor describes the command's change to the consumer groups of an
// existing stream. ok is false when the before-image is not available.
func (item *streamTxItem) groupOpsFor() (ops []persistence.GroupDelta, ok bool) {
	if !item.hasPreStub {
		return nil, false
	}
	before, err := decodePackedStream(item.preStub)
	if err != nil {
		return nil, false
	}
	offset := 0
	after, err := readStreamGroups(item.groups, &offset, packedStreamHeaderV3[2])
	if err != nil || offset != len(item.groups) {
		return nil, false
	}
	return diffStreamGroups(before.Groups, after), true
}
