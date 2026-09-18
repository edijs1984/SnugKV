package engine

import (
	"bytes"
	"errors"
	"snugkv/internal/codec"
	"snugkv/internal/codec/jsonshape"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var ErrOOM = errors.New("OOM command not allowed when used memory > 'maxmemory'.")

type memoryAdmission uint8

const (
	enforceMemoryLimit memoryAdmission = iota
	allowOverMemoryLimit
)

func (s *Store) exceedsMemoryLimitLocked(next uint64, admission memoryAdmission) bool {
	return admission == enforceMemoryLimit &&
		s.memory.max > 0 &&
		next > s.memory.max
}

// Reservations track owned engine allocations, not total process RSS.
var entryStructBytes = uint64(unsafe.Sizeof(entry{}))
var entryMetaBytes = uint64(unsafe.Sizeof(entryMeta{}))

type Options struct {
	Shards        int
	MaxMemory     uint64
	Encoding      bool
	ShapeEncoding bool
	Compression   bool
}
type MemoryStats struct {
	AccountedBytes,
	MaxBytes,
	IndexReservedBytes,
	EntryBytes,
	ArenaBytes,
	ArenaPayloadBytes,
	ArenaLiveBlockBytes,
	SchemaBytes,
	MetaBytes uint64
}

type LayoutStats struct {
	EntryStructBytes     uint64
	EntryMetaStructBytes uint64
	IndexSlotBytes       uint64
	EntryCapacity        uint64
	EntryStorageBytes    uint64
}

func (s *Store) Layout() LayoutStats {
	var entryCapacity uint64
	var indexSlotBytes uint64

	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()

		entryCapacity += uint64(cap(sh.entries))

		if indexSlotBytes == 0 {
			indexSlotBytes = sh.data.EntryBytes()
		}

		sh.mu.RUnlock()
	}

	return LayoutStats{
		EntryStructBytes:     entryStructBytes,
		EntryMetaStructBytes: entryMetaBytes,
		IndexSlotBytes:       indexSlotBytes,
		EntryCapacity:        entryCapacity,
		EntryStorageBytes:    entryCapacity * entryStructBytes,
	}
}

type accounting struct {
	mu                                                                               sync.Mutex
	used, index, entries, max, arenas, arenaPayload, arenaLiveBlocks, schemas, metas uint64
}

func (s *Store) Memory() MemoryStats {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	return MemoryStats{
		AccountedBytes:      s.memory.used,
		MaxBytes:            s.memory.max,
		IndexReservedBytes:  s.memory.index,
		EntryBytes:          s.memory.entries,
		ArenaBytes:          s.memory.arenas,
		ArenaPayloadBytes:   s.memory.arenaPayload,
		ArenaLiveBlockBytes: s.memory.arenaLiveBlocks,
		SchemaBytes:         s.memory.schemas,
		MetaBytes:           s.memory.metas,
	}
}

// entryCharge is the live key-byte charge. Entry struct storage itself is
// charged by reserved []entry capacity, because deleted slots remain allocated
// and reusable.
func entryCharge(key string, _ any) uint64 {
	return uint64(len(key))
}

func metadataCharge(e entry) uint64 {
	if e.entryMeta == nil {
		return 0
	}
	return entryMetaBytes
}
