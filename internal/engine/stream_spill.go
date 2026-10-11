package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Disk spill for stream entry logs.
//
// When a spill directory is configured, the oldest sealed chunks of a stream
// that exceeds its in-memory budget are written to immutable segment files and
// dropped from memory; reads fetch them back on demand. The segment files are a
// cache of cold data, not the source of truth: the AOF or snapshot still holds
// every entry, the directory is emptied at startup, and replaying the AOF
// spills again as it goes. A segment file is removed once no chunk of any
// stream refers to it, which is what happens when trimming or deleting
// acknowledged messages drops its chunks.

const streamSegmentPrefix = "seg-"

type streamSpill struct {
	dir      string
	ramLimit uint64 // per stream
	budget   uint64 // all streams together, zero for no shared budget
	streams  *atomic.Int64
	next     atomic.Uint64

	files   atomic.Int64
	written atomic.Uint64 // bytes written to segments, for INFO
}

// StreamSpillStats describes the spill directory.
type StreamSpillStats struct {
	Enabled      bool
	Segments     int64
	BytesWritten uint64
}

type streamSegment struct {
	path string
}

// newStreamSpill prepares dir for use by one store. Leftover segments from an
// earlier run are removed.
func newStreamSpill(dir string, ramLimit, budget uint64, streams *atomic.Int64) (*streamSpill, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), streamSegmentPrefix) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return nil, err
			}
		}
	}
	if ramLimit == 0 {
		ramLimit = 64 << 20
	}
	if ramLimit < 4*streamChunkTarget {
		ramLimit = 4 * streamChunkTarget
	}
	return &streamSpill{dir: dir, ramLimit: ramLimit, budget: budget, streams: streams}, nil
}

// limit is the memory one stream may hold. With a shared budget it is an equal
// share of it, never above the per-stream limit and never below four chunks.
func (sp *streamSpill) limit() uint64 {
	limit := sp.ramLimit
	if sp.budget > 0 {
		n := uint64(1)
		if sp.streams != nil && sp.streams.Load() > 1 {
			n = uint64(sp.streams.Load())
		}
		if share := sp.budget / n; share < limit {
			limit = share
		}
	}
	if limit < 4*streamChunkTarget {
		limit = 4 * streamChunkTarget
	}
	return limit
}

func (c *streamChunk) size() int {
	if c.seg != nil {
		return c.segLen
	}
	return len(c.data)
}

// get returns the chunk bytes, reading them from disk for a spilled chunk. The
// result must not be modified.
func (c *streamChunk) get() []byte {
	if c.seg == nil {
		return c.data
	}
	f, err := os.Open(c.seg.path)
	if err != nil {
		panic(fmt.Sprintf("stream segment unavailable: %v", err))
	}
	defer f.Close()
	buf := make([]byte, c.segLen)
	if _, err := f.ReadAt(buf, c.segOff); err != nil {
		panic(fmt.Sprintf("stream segment unreadable: %v", err))
	}
	return buf
}

// unspill brings a chunk back into memory so it can be modified.
func (b *streamBody) unspill(c *streamChunk) {
	if c.seg == nil {
		return
	}
	c.data = c.get()
	c.seg = nil
	b.dataCap += uint64(cap(c.data))
	b.segDirty = true
}

// spillCold writes the oldest sealed chunks to a segment file when the stream
// holds more than its memory budget. It does nothing while a journal
// transaction watches the log, because rollback may need the chunks as they
// were; the transaction's end calls it again.
func (b *streamBody) spillCold() {
	sp := b.sp
	if sp == nil || b.j != nil || len(b.chunks) < 2 {
		return
	}
	limit := sp.limit()
	if b.dataCap <= limit {
		return
	}
	target := limit / 2
	resident := b.dataCap
	var picks []int
	for i := 0; i < len(b.chunks)-1 && resident > target; i++ {
		c := &b.chunks[i]
		if c.seg != nil || len(c.data) == 0 {
			continue
		}
		picks = append(picks, i)
		resident -= uint64(cap(c.data))
	}
	if len(picks) == 0 {
		return
	}
	seg := &streamSegment{path: filepath.Join(sp.dir, fmt.Sprintf("%s%d.dat", streamSegmentPrefix, sp.next.Add(1)))}
	f, err := os.OpenFile(seg.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	offsets := make([]int64, len(picks))
	var pos int64
	for k, i := range picks {
		data := b.chunks[i].data
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(seg.path)
			return
		}
		offsets[k] = pos
		pos += int64(len(data))
	}
	if err := f.Close(); err != nil {
		os.Remove(seg.path)
		return
	}
	for k, i := range picks {
		c := &b.chunks[i]
		b.dataCap -= uint64(cap(c.data))
		c.seg, c.segOff, c.segLen = seg, offsets[k], len(c.data)
		c.data = nil
	}
	if b.segs == nil {
		b.segs = make(map[*streamSegment]struct{})
	}
	b.segs[seg] = struct{}{}
	sp.files.Add(1)
	sp.written.Add(uint64(pos))
}

// gcSegments removes segment files no chunk refers to any more. It is skipped
// while a journal transaction is active: a rollback may bring dropped chunks
// back.
func (b *streamBody) gcSegments() {
	if !b.segDirty || b.j != nil {
		return
	}
	b.segDirty = false
	if len(b.segs) == 0 {
		return
	}
	used := make(map[*streamSegment]struct{}, len(b.segs))
	for i := range b.chunks {
		if s := b.chunks[i].seg; s != nil {
			used[s] = struct{}{}
		}
	}
	for seg := range b.segs {
		if _, ok := used[seg]; !ok {
			b.removeSegment(seg)
		}
	}
}

func (b *streamBody) removeSegment(seg *streamSegment) {
	os.Remove(seg.path)
	delete(b.segs, seg)
	if b.sp != nil {
		b.sp.files.Add(-1)
	}
}

// release deletes every segment the log owns. The log must not be used again.
func (b *streamBody) release() {
	for seg := range b.segs {
		b.removeSegment(seg)
	}
	b.segs = nil
}

// settle runs the work that was deferred while a transaction watched the log.
func (b *streamBody) settle() {
	b.gcSegments()
	b.spillCold()
}

// SpillStats reports the spill directory usage.
func (s *Store) StreamSpillStats() StreamSpillStats {
	sp := s.streamSpill
	if sp == nil {
		return StreamSpillStats{}
	}
	return StreamSpillStats{Enabled: true, Segments: sp.files.Load(), BytesWritten: sp.written.Load()}
}
