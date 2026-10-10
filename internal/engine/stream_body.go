package engine

import (
	"encoding/binary"
	"sort"
	"unsafe"
)

// streamChunkTarget is the size at which the open tail chunk is sealed and a
// new one started. Chunks keep entries in the same packed byte layout that the
// logical stream format uses, so exporting a stream is a concatenation.
const streamChunkTarget = 32 << 10

// streamChunk is a run of consecutive stream entries stored as packed bytes.
// Live entries start at data[head:]; head is non-zero only after entries were
// trimmed from the front, which avoids rewriting the chunk for every trim.
type streamChunk struct {
	first, last StreamID
	count       int
	head        int
	lastOff     int
	data        []byte
}

// streamBody holds the entries of one stream as a list of chunks. Appending an
// entry only touches the newest chunk, so the cost of XADD no longer depends on
// how many entries the stream already holds.
//
// The body also owns the lifetime counters (last ID, entries added, maximum
// deleted ID) because they change on every append. Consumer groups stay in the
// small stored value next to it.
type streamBody struct {
	chunks       []streamChunk
	length       int
	dataBytes    uint64 // live entry bytes, equal to their logical encoded size
	dataCap      uint64 // allocated bytes across all chunk buffers
	LastID       StreamID
	MaxDeletedID StreamID
	EntriesAdded uint64
}

var streamBodyBaseBytes = uint64(unsafe.Sizeof(streamBody{}))
var streamChunkStructBytes = uint64(unsafe.Sizeof(streamChunk{}))

func newStreamBody() *streamBody { return &streamBody{} }

// memory is the number of bytes this body owns.
func (b *streamBody) memory() uint64 {
	return streamBodyBaseBytes + b.dataCap + uint64(cap(b.chunks))*streamChunkStructBytes
}

func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func streamEntrySize(fields []StreamField) int {
	n := 16 + uvarintLen(uint64(len(fields)))
	for _, f := range fields {
		n += uvarintLen(uint64(len(f.Field))) + len(f.Field) + uvarintLen(uint64(len(f.Value))) + len(f.Value)
	}
	return n
}

func appendStreamEntryBytes(dst []byte, id StreamID, fields []StreamField) []byte {
	dst = appendStreamID(dst, id)
	dst = appendStreamUvarint(dst, uint64(len(fields)))
	for _, f := range fields {
		dst = appendStreamUvarint(dst, uint64(len(f.Field)))
		dst = append(dst, f.Field...)
		dst = appendStreamUvarint(dst, uint64(len(f.Value)))
		dst = append(dst, f.Value...)
	}
	return dst
}

func chunkUvarint(data []byte, p int) (uint64, int) {
	v, n := binary.Uvarint(data[p:])
	if n <= 0 {
		panic("corrupt stream chunk")
	}
	return v, p + n
}

// streamEntryEnd returns the offset just past the entry that starts at off.
func streamEntryEnd(data []byte, off int) int {
	p := off + 16
	fields, p := chunkUvarint(data, p)
	for i := uint64(0); i < fields*2; i++ {
		var l uint64
		l, p = chunkUvarint(data, p)
		p += int(l)
	}
	if p > len(data) {
		panic("corrupt stream chunk")
	}
	return p
}

func streamEntryIDAt(data []byte, off int) StreamID {
	return StreamID{
		Millis:   binary.BigEndian.Uint64(data[off : off+8]),
		Sequence: binary.BigEndian.Uint64(data[off+8 : off+16]),
	}
}

// decodeStreamEntryAt returns an entry whose bytes do not alias the chunk.
func decodeStreamEntryAt(data []byte, off int) (StreamEntry, int) {
	id := streamEntryIDAt(data, off)
	p := off + 16
	n, p := chunkUvarint(data, p)
	fields := make([]StreamField, 0, int(n))
	for i := uint64(0); i < n; i++ {
		var fl, vl uint64
		fl, p = chunkUvarint(data, p)
		field := append([]byte(nil), data[p:p+int(fl)]...)
		p += int(fl)
		vl, p = chunkUvarint(data, p)
		value := append([]byte(nil), data[p:p+int(vl)]...)
		p += int(vl)
		fields = append(fields, StreamField{Field: field, Value: value})
	}
	return StreamEntry{ID: id, Fields: fields}, p
}

func (b *streamBody) Len() int { return b.length }

func (c *streamChunk) live() []byte { return c.data[c.head:] }

// firstChunkAtOrAfter returns the index of the first chunk that may hold an
// entry >= id (inclusive) or > id (exclusive).
func (b *streamBody) firstChunkAtOrAfter(id StreamID, inclusive bool) int {
	return sort.Search(len(b.chunks), func(i int) bool {
		last := b.chunks[i].last
		if inclusive {
			return !last.less(id)
		}
		return id.less(last)
	})
}

func (b *streamBody) First() (StreamEntry, bool) {
	if b.length == 0 {
		return StreamEntry{}, false
	}
	c := &b.chunks[0]
	e, _ := decodeStreamEntryAt(c.data, c.head)
	return e, true
}

func (b *streamBody) Last() (StreamEntry, bool) {
	if b.length == 0 {
		return StreamEntry{}, false
	}
	c := &b.chunks[len(b.chunks)-1]
	e, _ := decodeStreamEntryAt(c.data, c.lastOff)
	return e, true
}

// FirstID and LastEntryID are cheap accessors that do not decode payloads.
func (b *streamBody) FirstID() (StreamID, bool) {
	if b.length == 0 {
		return StreamID{}, false
	}
	return b.chunks[0].first, true
}

func (b *streamBody) LastEntryID() (StreamID, bool) {
	if b.length == 0 {
		return StreamID{}, false
	}
	return b.chunks[len(b.chunks)-1].last, true
}

// Get returns the entry with exactly this ID.
func (b *streamBody) Get(id StreamID) (StreamEntry, bool) {
	ci := b.firstChunkAtOrAfter(id, true)
	if ci >= len(b.chunks) || id.less(b.chunks[ci].first) {
		return StreamEntry{}, false
	}
	c := &b.chunks[ci]
	for off, k := c.head, 0; k < c.count; k++ {
		eid := streamEntryIDAt(c.data, off)
		if eid.equal(id) {
			e, _ := decodeStreamEntryAt(c.data, off)
			return e, true
		}
		if id.less(eid) {
			break
		}
		off = streamEntryEnd(c.data, off)
	}
	return StreamEntry{}, false
}

func (b *streamBody) Has(id StreamID) bool {
	ci := b.firstChunkAtOrAfter(id, true)
	if ci >= len(b.chunks) || id.less(b.chunks[ci].first) {
		return false
	}
	c := &b.chunks[ci]
	for off, k := c.head, 0; k < c.count; k++ {
		eid := streamEntryIDAt(c.data, off)
		if eid.equal(id) {
			return true
		}
		if id.less(eid) {
			return false
		}
		off = streamEntryEnd(c.data, off)
	}
	return false
}

// ForEachFrom visits entries in ascending order starting at the first entry
// >= start (inclusive) or > start (exclusive) until fn returns false. Entries
// are copied, so fn may keep them.
func (b *streamBody) ForEachFrom(start StreamID, inclusive bool, fn func(StreamEntry) bool) {
	for ci := b.firstChunkAtOrAfter(start, inclusive); ci < len(b.chunks); ci++ {
		c := &b.chunks[ci]
		off := c.head
		for k := 0; k < c.count; k++ {
			id := streamEntryIDAt(c.data, off)
			pass := !id.less(start)
			if !inclusive {
				pass = start.less(id)
			}
			if !pass {
				off = streamEntryEnd(c.data, off)
				continue
			}
			e, next := decodeStreamEntryAt(c.data, off)
			if !fn(e) {
				return
			}
			off = next
		}
	}
}

// ForEachReverse visits entries in descending order starting at the last entry
// <= end until fn returns false.
func (b *streamBody) ForEachReverse(end StreamID, fn func(StreamEntry) bool) {
	ci := sort.Search(len(b.chunks), func(i int) bool { return end.less(b.chunks[i].first) }) - 1
	for ; ci >= 0; ci-- {
		c := &b.chunks[ci]
		offsets := make([]int, 0, c.count)
		for off, k := c.head, 0; k < c.count; k++ {
			offsets = append(offsets, off)
			off = streamEntryEnd(c.data, off)
		}
		for i := len(offsets) - 1; i >= 0; i-- {
			if end.less(streamEntryIDAt(c.data, offsets[i])) {
				continue
			}
			e, _ := decodeStreamEntryAt(c.data, offsets[i])
			if !fn(e) {
				return
			}
		}
	}
}

// All returns every entry in order. It is meant for rare whole-stream
// operations such as snapshots and XINFO FULL.
func (b *streamBody) All() []StreamEntry {
	out := make([]StreamEntry, 0, b.length)
	for i := range b.chunks {
		c := &b.chunks[i]
		off := c.head
		for k := 0; k < c.count; k++ {
			var e StreamEntry
			e, off = decodeStreamEntryAt(c.data, off)
			out = append(out, e)
		}
	}
	return out
}

// packedEntries appends the logical encoding of every live entry.
func (b *streamBody) packedEntries(dst []byte) []byte {
	for i := range b.chunks {
		dst = append(dst, b.chunks[i].live()...)
	}
	return dst
}

// Append adds an entry whose ID is greater than every existing ID.
func (b *streamBody) Append(id StreamID, fields []StreamField) {
	size := streamEntrySize(fields)
	n := len(b.chunks)
	if n == 0 || len(b.chunks[n-1].data) >= streamChunkTarget {
		if n > 0 {
			b.seal(n - 1)
		}
		b.chunks = append(b.chunks, streamChunk{first: id, data: make([]byte, 0, size)})
		b.dataCap += uint64(size)
		n++
	}
	c := &b.chunks[n-1]
	if c.count == 0 {
		c.first = id
	}
	oldCap := cap(c.data)
	c.lastOff = len(c.data)
	c.data = appendStreamEntryBytes(c.data, id, fields)
	b.dataCap += uint64(cap(c.data) - oldCap)
	c.last = id
	c.count++
	b.length++
	b.dataBytes += uint64(size)
}

// seal trims the spare capacity of a chunk that will not be appended to again.
func (b *streamBody) seal(i int) {
	c := &b.chunks[i]
	if cap(c.data)-len(c.data) <= len(c.data)/16 {
		return
	}
	exact := make([]byte, len(c.data))
	copy(exact, c.data)
	b.dataCap -= uint64(cap(c.data) - len(exact))
	c.data = exact
}

func (b *streamBody) dropChunk(i int) {
	c := &b.chunks[i]
	b.dataCap -= uint64(cap(c.data))
	b.dataBytes -= uint64(len(c.data) - c.head)
	b.length -= c.count
	copy(b.chunks[i:], b.chunks[i+1:])
	b.chunks[len(b.chunks)-1] = streamChunk{}
	b.chunks = b.chunks[:len(b.chunks)-1]
}

// TrimFront removes up to n of the oldest entries and returns how many went.
func (b *streamBody) TrimFront(n int) int {
	removed := 0
	for n > 0 && len(b.chunks) > 0 {
		c := &b.chunks[0]
		if c.count <= n {
			n -= c.count
			removed += c.count
			b.dataCap -= uint64(cap(c.data))
			b.dataBytes -= uint64(len(c.data) - c.head)
			b.length -= c.count
			b.chunks[0] = streamChunk{}
			b.chunks = b.chunks[1:]
			continue
		}
		off := c.head
		for k := 0; k < n; k++ {
			off = streamEntryEnd(c.data, off)
		}
		b.dataBytes -= uint64(off - c.head)
		b.length -= n
		removed += n
		c.count -= n
		c.head = off
		c.first = streamEntryIDAt(c.data, off)
		n = 0
		if c.head > len(c.data)/2 {
			live := make([]byte, len(c.data)-c.head)
			copy(live, c.data[c.head:])
			b.dataCap -= uint64(cap(c.data) - len(live))
			c.lastOff -= c.head
			c.data = live
			c.head = 0
		}
	}
	return removed
}

// CountBefore returns how many of the oldest entries have an ID below minID,
// stopping at limit when limit > 0.
func (b *streamBody) CountBefore(minID StreamID, limit int) int {
	count := 0
	for ci := range b.chunks {
		c := &b.chunks[ci]
		if c.last.less(minID) {
			count += c.count
			if limit > 0 && count >= limit {
				return limit
			}
			continue
		}
		off := c.head
		for k := 0; k < c.count; k++ {
			if !streamEntryIDAt(c.data, off).less(minID) {
				return count
			}
			count++
			if limit > 0 && count >= limit {
				return limit
			}
			off = streamEntryEnd(c.data, off)
		}
		return count
	}
	return count
}

// Delete removes the listed entries and returns how many existed.
func (b *streamBody) Delete(ids map[StreamID]struct{}) int {
	if len(ids) == 0 || b.length == 0 {
		return 0
	}
	byChunk := make(map[int]map[StreamID]struct{})
	for id := range ids {
		ci := b.firstChunkAtOrAfter(id, true)
		if ci >= len(b.chunks) || id.less(b.chunks[ci].first) {
			continue
		}
		set := byChunk[ci]
		if set == nil {
			set = make(map[StreamID]struct{})
			byChunk[ci] = set
		}
		set[id] = struct{}{}
	}
	order := make([]int, 0, len(byChunk))
	for ci := range byChunk {
		order = append(order, ci)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(order)))
	removed := 0
	for _, ci := range order {
		removed += b.rewriteChunk(ci, byChunk[ci])
	}
	return removed
}

func (b *streamBody) rewriteChunk(ci int, drop map[StreamID]struct{}) int {
	c := &b.chunks[ci]
	kept := make([]byte, 0, len(c.data)-c.head)
	count, removed := 0, 0
	var first, last StreamID
	lastOff := 0
	off := c.head
	for k := 0; k < c.count; k++ {
		end := streamEntryEnd(c.data, off)
		id := streamEntryIDAt(c.data, off)
		if _, gone := drop[id]; gone {
			removed++
		} else {
			if count == 0 {
				first = id
			}
			last = id
			lastOff = len(kept)
			kept = append(kept, c.data[off:end]...)
			count++
		}
		off = end
	}
	if removed == 0 {
		return 0
	}
	if count == 0 {
		b.dropChunk(ci)
		return removed
	}
	b.dataCap -= uint64(cap(c.data))
	b.dataBytes -= uint64(len(c.data) - c.head)
	b.length -= removed
	c.data, c.head, c.count = kept, 0, count
	c.first, c.last, c.lastOff = first, last, lastOff
	b.dataCap += uint64(cap(kept))
	b.dataBytes += uint64(len(kept))
	return removed
}

// streamBodyFromEntries builds a body from already ordered entries.
func streamBodyFromEntries(entries []StreamEntry, last, maxDeleted StreamID, added uint64) *streamBody {
	b := newStreamBody()
	for _, e := range entries {
		b.Append(e.ID, e.Fields)
	}
	if n := len(b.chunks); n > 0 {
		b.seal(n - 1)
	}
	b.LastID, b.MaxDeletedID, b.EntriesAdded = last, maxDeleted, added
	return b
}

// ForEachID visits entry IDs in ascending order without decoding payloads.
func (b *streamBody) ForEachID(fn func(StreamID) bool) {
	for ci := range b.chunks {
		c := &b.chunks[ci]
		off := c.head
		for k := 0; k < c.count; k++ {
			if !fn(streamEntryIDAt(c.data, off)) {
				return
			}
			off = streamEntryEnd(c.data, off)
		}
	}
}
