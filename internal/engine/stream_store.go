package engine

import (
	"errors"
	"sync"

	"snugkv/internal/index"
)

// A STREAM key is stored in two parts:
//
//   - the stored value (a small packed blob) holds the consumer groups, their
//     consumers and pending entries, which change on reads and acknowledgements;
//   - the shard's streams map holds the entry log as chunks, together with the
//     counters that change on every XADD.
//
// The logical value that persistence, replication, DUMP and RENAME exchange is
// unchanged: it is still the packed stream format with every entry inline, and
// streamLogicalValue / installStreamLocked convert between the two forms.

// streamState is the working view of one stream inside a single operation.
// Groups is a private copy, so changing it never touches the stored value until
// the operation publishes it. log is the shared entry log.
type streamState struct {
	LastID       StreamID
	EntriesAdded uint64
	MaxDeletedID StreamID
	Groups       []streamGroup
	log          *streamBody
}

var errStreamLogMissing = errors.New("ERR stream entry log is missing")
var errStreamTooLarge = errors.New("ERR stream exceeds 32 MiB limit")

func (s *Store) streamStateFromEntry(sh *shard, key string, e entry) (streamState, error) {
	stub, err := decodePackedStream(sh.encoded(e))
	if err != nil {
		return streamState{}, err
	}
	body := s.streamLogs.get(key)
	if body == nil {
		return streamState{}, errStreamLogMissing
	}
	return streamState{
		LastID:       body.LastID,
		EntriesAdded: body.EntriesAdded,
		MaxDeletedID: body.MaxDeletedID,
		Groups:       stub.Groups,
		log:          body,
	}, nil
}

// syncLog copies the counters of a working state back into the shared log.
func (st *streamState) syncLog() {
	st.log.LastID = st.LastID
	st.log.EntriesAdded = st.EntriesAdded
	st.log.MaxDeletedID = st.MaxDeletedID
}

func encodeStreamStub(st streamState) ([]byte, error) {
	return encodePackedStream(packedStream{
		LastID:       st.LastID,
		EntriesAdded: st.EntriesAdded,
		MaxDeletedID: st.MaxDeletedID,
		Groups:       st.Groups,
	})
}

// streamLogicalSize is the size of the logical value for a stream whose stored
// part encodes to stubLen bytes.
func streamLogicalSize(st streamState, stubLen int) uint64 {
	return st.log.dataBytes + uint64(stubLen)
}

func (s *Store) streamLogicalValue(sh *shard, key string, e entry) ([]byte, error) {
	st, err := s.streamStateFromEntry(sh, key, e)
	if err != nil {
		return nil, err
	}
	return encodeLogicalStream(st)
}

// encodeLogicalStream produces the packed stream format with entries inline.
func encodeLogicalStream(st streamState) ([]byte, error) {
	if st.EntriesAdded < uint64(st.log.length) || st.LastID.less(st.MaxDeletedID) {
		return nil, errors.New("ERR invalid stream lifetime metadata")
	}
	if last, ok := st.log.LastEntryID(); ok && st.LastID.less(last) {
		return nil, errors.New("ERR stream last-generated ID precedes an entry")
	}
	out := make([]byte, 0, int(st.log.dataBytes)+64)
	out = append(out, packedStreamHeaderV3[:]...)
	out = appendStreamID(out, st.LastID)
	out = appendStreamUvarint(out, st.EntriesAdded)
	out = appendStreamID(out, st.MaxDeletedID)
	out = appendStreamUvarint(out, uint64(st.log.length))
	out = st.log.packedEntries(out)
	out, err := appendStreamGroups(out, st.Groups)
	if err != nil {
		return nil, err
	}
	if len(out) > maxPackedStreamBytes {
		return nil, errStreamTooLarge
	}
	return out, nil
}

func (s *Store) publishStreamStateLocked(sh *shard, key string, old entry, state streamState) error {
	stub, err := encodeStreamStub(state)
	if err != nil {
		return err
	}
	if streamLogicalSize(state, len(stub)) > maxPackedStreamBytes {
		return errStreamTooLarge
	}
	updated := streamPreparedEntry(stub)
	updated.keepStream = true
	updated.expiresAt = sh.expirationAt(key, old)
	return s.publish(sh, key, updated)
}

// createStreamLocked stores a stream that does not exist yet. The log is
// attached only once the stored value was published.
func (s *Store) createStreamLocked(sh *shard, key string, state streamState, expiresAt stamp) error {
	stub, err := encodeStreamStub(state)
	if err != nil {
		return err
	}
	if streamLogicalSize(state, len(stub)) > maxPackedStreamBytes {
		return errStreamTooLarge
	}
	bytes := state.log.memory()
	if err := s.reserveStreamMemory(bytes, enforceMemoryLimit); err != nil {
		return err
	}
	updated := streamPreparedEntry(stub)
	updated.keepStream = true
	updated.expiresAt = expiresAt
	if err := s.publish(sh, key, updated); err != nil {
		s.releaseStreamMemory(bytes)
		return err
	}
	s.streamLogs.set(key, state.log)
	return nil
}

// installStreamLocked replaces key with the stream described by a logical
// value. It is the inverse of streamLogicalValue.
func (s *Store) installStreamLocked(sh *shard, key string, packed packedStream, expiresAt stamp, admission memoryAdmission) error {
	body := streamBodyFromEntries(packed.Entries, packed.LastID, packed.MaxDeletedID, packed.EntriesAdded)
	state := streamState{
		LastID: packed.LastID, EntriesAdded: packed.EntriesAdded, MaxDeletedID: packed.MaxDeletedID,
		Groups: packed.Groups, log: body,
	}
	stub, err := encodeStreamStub(state)
	if err != nil {
		return err
	}
	bytes := body.memory()
	if err := s.reserveStreamMemory(bytes, admission); err != nil {
		return err
	}
	updated := streamPreparedEntry(stub)
	updated.keepStream = true
	updated.expiresAt = expiresAt
	// publish drops the log of a stream this key held before; the new one is
	// attached afterwards.
	if err := s.publishRecord(sh, key, updated, admission); err != nil {
		s.releaseStreamMemory(bytes)
		return err
	}
	if old := s.streamLogs.get(key); old != nil {
		s.dropStreamBody(sh, key)
	}
	s.streamLogs.set(key, body)
	return nil
}

func streamPreparedEntry(packed []byte) preparedEntry {
	return preparedEntry{
		entry: entry{entryData: entryData{valueType: TypeStream, rawLength: uint32(len(packed))}},
		data:  append([]byte(nil), packed...),
	}
}

// dropStreamBody releases the entry log of key and its memory charge.
func (s *Store) dropStreamBody(sh *shard, key string) {
	s.memory.mu.Lock()
	s.dropStreamBodyLocked(sh, key)
	s.memory.mu.Unlock()
}

// dropStreamBodyLocked is dropStreamBody for callers holding the memory lock.
func (s *Store) dropStreamBodyLocked(sh *shard, key string) {
	body := s.streamLogs.get(key)
	if body == nil {
		return
	}
	bytes := body.memory()
	s.streamLogs.del(key)
	s.memory.used -= bytes
	s.memory.streams -= bytes
}

// reserveStreamMemory charges bytes for a new log, honouring maxmemory.
func (s *Store) reserveStreamMemory(bytes uint64, admission memoryAdmission) error {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	next := s.memory.used + bytes
	if s.exceedsMemoryLimitLocked(next, admission) {
		return ErrOOM
	}
	s.memory.used = next
	s.memory.streams += bytes
	return nil
}

func (s *Store) releaseStreamMemory(bytes uint64) {
	s.memory.mu.Lock()
	s.memory.used -= bytes
	s.memory.streams -= bytes
	s.memory.mu.Unlock()
}

// admitStreamGrowth checks, without charging, that a log may grow by bytes.
func (s *Store) admitStreamGrowth(bytes uint64) error {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	if s.exceedsMemoryLimitLocked(s.memory.used+bytes, enforceMemoryLimit) {
		return ErrOOM
	}
	return nil
}

// settleStreamMemory records that a log changed from before to after bytes.
func (s *Store) settleStreamMemory(before, after uint64) {
	if before == after {
		return
	}
	s.memory.mu.Lock()
	if after > before {
		delta := after - before
		s.memory.used += delta
		s.memory.streams += delta
	} else {
		delta := before - after
		s.memory.used -= delta
		s.memory.streams -= delta
	}
	s.memory.mu.Unlock()
}

// streamAppendMark remembers the end of a log so one Append can be undone.
type streamAppendMark struct {
	chunks   int
	dataLen  int
	count    int
	first    StreamID
	last     StreamID
	lastOff  int
	length   int
	bytes    uint64
	lastID   StreamID
	maxDel   StreamID
	added    uint64
	memory   uint64
	hadChunk bool
}

func (b *streamBody) mark() streamAppendMark {
	m := streamAppendMark{
		chunks: len(b.chunks), length: b.length, bytes: b.dataBytes,
		lastID: b.LastID, maxDel: b.MaxDeletedID, added: b.EntriesAdded, memory: b.memory(),
	}
	if n := len(b.chunks); n > 0 {
		c := &b.chunks[n-1]
		m.hadChunk = true
		m.dataLen, m.count, m.first, m.last, m.lastOff = len(c.data), c.count, c.first, c.last, c.lastOff
	}
	return m
}

// rollback undoes the Append calls made since mark. Nothing else may have
// changed the log in between.
func (b *streamBody) rollback(m streamAppendMark) {
	for len(b.chunks) > m.chunks {
		last := len(b.chunks) - 1
		b.dataCap -= uint64(cap(b.chunks[last].data))
		b.chunks[last] = streamChunk{}
		b.chunks = b.chunks[:last]
	}
	if m.hadChunk {
		c := &b.chunks[m.chunks-1]
		c.data = c.data[:m.dataLen]
		c.count, c.first, c.last, c.lastOff = m.count, m.first, m.last, m.lastOff
	}
	b.length, b.dataBytes = m.length, m.bytes
	b.LastID, b.MaxDeletedID, b.EntriesAdded = m.lastID, m.maxDel, m.added
	b.recountCapacity()
}

func (b *streamBody) recountCapacity() {
	var total uint64
	for i := range b.chunks {
		total += uint64(cap(b.chunks[i].data))
	}
	b.dataCap = total
}

// attachStreamBody makes body the entry log of key, replacing and releasing
// any log the key had, and charges its memory.
func (s *Store) attachStreamBody(sh *shard, key string, body *streamBody) {
	s.dropStreamBody(sh, key)
	bytes := body.memory()
	s.memory.mu.Lock()
	s.memory.used += bytes
	s.memory.streams += bytes
	s.memory.mu.Unlock()
	s.streamLogs.set(key, body)
}

// streamRegistry maps stream keys to their entry logs. It is striped so that
// streams in different shards do not contend, and it keeps the per-shard
// struct small for stores that hold no streams at all.
type streamRegistry struct {
	stripes [32]streamStripe
}

type streamStripe struct {
	mu sync.RWMutex
	m  map[string]*streamBody
}

func (r *streamRegistry) stripe(key string) *streamStripe {
	return &r.stripes[index.Hash(key)&31]
}

func (r *streamRegistry) get(key string) *streamBody {
	st := r.stripe(key)
	st.mu.RLock()
	b := st.m[key]
	st.mu.RUnlock()
	return b
}

func (r *streamRegistry) set(key string, b *streamBody) {
	st := r.stripe(key)
	st.mu.Lock()
	if st.m == nil {
		st.m = make(map[string]*streamBody)
	}
	st.m[key] = b
	st.mu.Unlock()
}

func (r *streamRegistry) del(key string) {
	st := r.stripe(key)
	st.mu.Lock()
	delete(st.m, key)
	st.mu.Unlock()
}

func (r *streamRegistry) clear() {
	for i := range r.stripes {
		st := &r.stripes[i]
		st.mu.Lock()
		st.m = nil
		st.mu.Unlock()
	}
}
