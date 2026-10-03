package engine

import (
	"bytes"
	"errors"
	"sort"
	"unsafe"

	"snugkv/internal/arena"
	"snugkv/internal/index"
)

const hotHashMinSlots = 16

type hotHashRecord struct {
	hash       uint64
	fieldOff   uint32
	fieldLen   uint32
	valueOff   uint32
	valueLen   uint32
}

type hotHash struct {
	slots   []uint32 // record index + 1; zero means empty
	records []hotHashRecord
	payload []byte
}

func newHotHash(pairs []HashPair) *hotHash {
	slots := hotHashMinSlots
	for slots < len(pairs)*2 {
		slots <<= 1
	}
	h := &hotHash{
		slots:   make([]uint32, slots),
		records: make([]hotHashRecord, 0, len(pairs)),
	}
	for _, pair := range pairs {
		h.set(pair.Field, pair.Value)
	}
	return h
}

func hotHashSidecarBytes(sh *shard) uint64 {
	if sh.hotHashes == nil {
		return 0
	}
	return uint64(unsafe.Sizeof(hotHashSidecar{})) +
		uint64(cap(sh.hotHashes.slots))*uint64(unsafe.Sizeof((*hotHash)(nil)))
}

func (h *hotHash) memoryBytes() uint64 {
	if h == nil {
		return 0
	}
	return uint64(unsafe.Sizeof(hotHash{})) +
		uint64(cap(h.slots))*uint64(unsafe.Sizeof(uint32(0))) +
		uint64(cap(h.records))*uint64(unsafe.Sizeof(hotHashRecord{})) +
		uint64(cap(h.payload))
}

func (h *hotHash) find(field []byte, hash uint64) (slot int, record int, found bool) {
	if len(h.slots) == 0 {
		return 0, 0, false
	}
	mask := len(h.slots) - 1
	start := int(hash) & mask
	for probe := 0; probe < len(h.slots); probe++ {
		slot = (start + probe) & mask
		raw := h.slots[slot]
		if raw == 0 {
			return slot, 0, false
		}
		record = int(raw - 1)
		r := h.records[record]
		if r.hash != hash {
			continue
		}
		start := int(r.fieldOff)
		end := start + int(r.fieldLen)
		if start >= 0 && end >= start && end <= len(h.payload) &&
			bytes.Equal(h.payload[start:end], field) {
			return slot, record, true
		}
	}
	return 0, 0, false
}

func (h *hotHash) rehash(slots int) {
	if slots < hotHashMinSlots {
		slots = hotHashMinSlots
	}
	next := make([]uint32, slots)
	mask := slots - 1
	for i, r := range h.records {
		start := int(r.hash) & mask
		for probe := 0; probe < slots; probe++ {
			slot := (start + probe) & mask
			if next[slot] == 0 {
				next[slot] = uint32(i + 1)
				break
			}
		}
	}
	h.slots = next
}

func (h *hotHash) ensureInsertCapacity() {
	if len(h.slots) == 0 {
		h.slots = make([]uint32, hotHashMinSlots)
		return
	}
	if (len(h.records)+1)*10 >= len(h.slots)*7 {
		h.rehash(len(h.slots) << 1)
	}
}

func (h *hotHash) set(field, value []byte) (added bool) {
	hash := index.HashBytes(field)
	if _, record, found := h.find(field, hash); found {
		valueOff := len(h.payload)
		h.payload = append(h.payload, value...)
		h.records[record].valueOff = uint32(valueOff)
		h.records[record].valueLen = uint32(len(value))
		return false
	}

	h.ensureInsertCapacity()
	slot, _, _ := h.find(field, hash)
	fieldOff := len(h.payload)
	h.payload = append(h.payload, field...)
	valueOff := len(h.payload)
	h.payload = append(h.payload, value...)
	h.records = append(h.records, hotHashRecord{
		hash:     hash,
		fieldOff: uint32(fieldOff),
		fieldLen: uint32(len(field)),
		valueOff: uint32(valueOff),
		valueLen: uint32(len(value)),
	})
	h.slots[slot] = uint32(len(h.records))
	return true
}

func (h *hotHash) get(field []byte) ([]byte, bool) {
	hash := index.HashBytes(field)
	_, record, found := h.find(field, hash)
	if !found {
		return nil, false
	}
	r := h.records[record]
	start := int(r.valueOff)
	end := start + int(r.valueLen)
	if start < 0 || end < start || end > len(h.payload) {
		panic("invalid hot hash payload")
	}
	return h.payload[start:end], true
}

func (h *hotHash) delete(fields [][]byte) int64 {
	if len(fields) == 0 || len(h.records) == 0 {
		return 0
	}
	remove := make([]bool, len(h.records))
	var deleted int64
	for _, field := range fields {
		hash := index.HashBytes(field)
		_, record, found := h.find(field, hash)
		if found && !remove[record] {
			remove[record] = true
			deleted++
		}
	}
	if deleted == 0 {
		return 0
	}

	nextRecords := make([]hotHashRecord, 0, len(h.records)-int(deleted))
	nextPayload := make([]byte, 0, len(h.payload))
	for i, r := range h.records {
		if remove[i] {
			continue
		}
		fs := int(r.fieldOff)
		fe := fs + int(r.fieldLen)
		vs := int(r.valueOff)
		ve := vs + int(r.valueLen)
		fieldOff := len(nextPayload)
		nextPayload = append(nextPayload, h.payload[fs:fe]...)
		valueOff := len(nextPayload)
		nextPayload = append(nextPayload, h.payload[vs:ve]...)
		r.fieldOff = uint32(fieldOff)
		r.valueOff = uint32(valueOff)
		nextRecords = append(nextRecords, r)
	}
	h.records = nextRecords
	h.payload = nextPayload
	slots := hotHashMinSlots
	for slots < len(h.records)*2 {
		slots <<= 1
	}
	h.rehash(slots)
	return deleted
}

func (h *hotHash) pairs() []HashPair {
	pairs := make([]HashPair, 0, len(h.records))
	for _, r := range h.records {
		fs := int(r.fieldOff)
		fe := fs + int(r.fieldLen)
		vs := int(r.valueOff)
		ve := vs + int(r.valueLen)
		if fs < 0 || fe < fs || fe > len(h.payload) ||
			vs < 0 || ve < vs || ve > len(h.payload) {
			panic("invalid hot hash payload")
		}
		pairs = append(pairs, HashPair{
			Field: append([]byte(nil), h.payload[fs:fe]...),
			Value: append([]byte(nil), h.payload[vs:ve]...),
		})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return bytes.Compare(pairs[i].Field, pairs[j].Field) < 0
	})
	return pairs
}

func (h *hotHash) packed() ([]byte, error) {
	if h == nil {
		return nil, errors.New("nil hot hash")
	}
	return encodePackedHash(h.pairs())
}


func (s *Store) accountHotHashResize(oldBytes, newBytes uint64) {
	if oldBytes == newBytes {
		return
	}
	s.memory.mu.Lock()
	if newBytes >= oldBytes {
		delta := newBytes - oldBytes
		s.memory.used += delta
		s.memory.hotHashes += delta
	} else {
		delta := oldBytes - newBytes
		s.memory.used -= delta
		s.memory.hotHashes -= delta
	}
	s.memory.mu.Unlock()
}

func (s *Store) hotHashForKeyLocked(sh *shard, key string) (*hotHash, uint32, bool) {
	return sh.hotHashForKey(key)
}

func (s *Store) thawHotHashLocked(sh *shard, key string, e entry) (*hotHash, bool, error) {
	if e.isHotHash() {
		h, _, ok := sh.hotHashForKey(key)
		if !ok || h == nil {
			return nil, false, errors.New("HOT hash sidecar invariant")
		}
		return h, true, nil
	}
	if s.memory.max.Load() != 0 {
		return nil, false, nil
	}

	pairs, err := decodePackedHash(s.decode(sh, e))
	if err != nil {
		return nil, false, err
	}
	pairs = liveHashPairs(pairs, s.now().UnixMilli())
	for _, pair := range pairs {
		if pair.ExpiresAtMS != 0 {
			return nil, false, nil
		}
	}

	id, ok := sh.data.Get(key)
	if !ok {
		return nil, false, nil
	}
	h := newHotHash(pairs)
	hotBytes := h.memoryBytes()
	sidecarBytes := uint64(0)
	if sh.hotHashes == nil {
		sidecarBytes =
			uint64(unsafe.Sizeof(hotHashSidecar{})) +
			uint64(cap(sh.entries))*uint64(unsafe.Sizeof((*hotHash)(nil)))
	}

	oldPayload := uint64(0)
	oldBlock := uint64(0)
	freeGrowth := uint64(0)
	if !e.ref.IsInline() {
		oldPayload = uint64(len(sh.encoded(e)))
		oldBlock = sh.arena.AllocationBytes(e.ref)
		freeGrowth = sh.arena.FreeGrowth(e.ref)
	}

	s.memory.mu.Lock()
	s.memory.used += hotBytes + sidecarBytes + freeGrowth
	s.memory.hotHashes += hotBytes + sidecarBytes
	s.memory.arenas += freeGrowth
	if oldPayload != 0 {
		s.memory.arenaPayload -= oldPayload
	}
	if oldBlock != 0 {
		s.memory.arenaLiveBlocks -= oldBlock
	}
	s.memory.mu.Unlock()

	if !e.ref.IsInline() {
		sh.arena.Free(e.ref)
	}
	e.rawStable = true
	e.ref = arena.Ref{}
	sh.entries[id] = e.entryData
	sh.setHotHash(id, h)
	return h, true, nil
}

func (s *Store) freezeHotHashLocked(sh *shard, key string, e entry) error {
	if !e.isHotHash() {
		return nil
	}
	h, _, ok := sh.hotHashForKey(key)
	if !ok || h == nil {
		return errors.New("HOT hash sidecar invariant")
	}
	pairs := h.pairs()
	packed, err := encodePackedHash(pairs)
	if err != nil {
		return err
	}
	updated := s.hashEntry(pairs, packed)
	updated.expiresAt = sh.expirationAt(key, e)
	return s.publish(sh, key, updated)
}

func (s *Store) freezeHotHashAndReloadLocked(sh *shard, key string, e entry) (entry, error) {
	if !e.isHotHash() {
		return e, nil
	}
	if err := s.freezeHotHashLocked(sh, key, e); err != nil {
		return entry{}, err
	}
	next, ok := sh.get(key)
	if !ok {
		return entry{}, errors.New("HASH disappeared while freezing HOT value")
	}
	return next, nil
}

func (s *Store) mutateHotHashLocked(h *hotHash, mutate func()) {
	before := h.memoryBytes()
	mutate()
	after := h.memoryBytes()
	s.accountHotHashResize(before, after)
}
