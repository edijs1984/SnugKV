package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
)

const (
	bloomHeaderSize      = 48
	bloomDefaultCapacity = uint64(100)
	bloomDefaultError    = 0.01
	bloomDefaultExpansion = uint32(2)
	bloomMaxCapacity     = uint64(1 << 30)
)

var bloomMagic = [4]byte{'S', 'B', 'F', 1}

type BloomInfo struct {
	Capacity  uint64
	Size      uint64
	Filters   uint64
	Items     uint64
	Expansion uint32
}

type bloomFilter struct {
	capacity  uint64
	items     uint64
	bits      uint64
	hashes    uint32
	errorRate float64
	expansion uint32
	data      []byte
}

func bloomWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func bloomParameters(capacity uint64, errorRate float64) (uint64, uint32) {
	ln2 := math.Ln2
	bits := uint64(math.Ceil(-float64(capacity) * math.Log(errorRate) / (ln2 * ln2)))
	if bits < 8 {
		bits = 8
	}
	hashes := uint32(math.Round(float64(bits) / float64(capacity) * ln2))
	if hashes < 1 {
		hashes = 1
	}
	return bits, hashes
}

func newBloom(capacity uint64, errorRate float64, expansion uint32) (*bloomFilter, error) {
	if !(errorRate > 0 && errorRate < 1) {
		return nil, errors.New("ERR error rate must be in the range (0.000000, 1.000000)")
	}
	if capacity < 1 || capacity > bloomMaxCapacity {
		return nil, errors.New("ERR capacity must be in the range [1, 1073741824]")
	}
	if expansion == 0 {
		expansion = bloomDefaultExpansion
	}

	bits, hashes := bloomParameters(capacity, errorRate)
	bytesNeeded := (bits + 7) / 8
	if bytesNeeded > 32<<20-bloomHeaderSize {
		return nil, errors.New("ERR bloom filter exceeds 32 MiB limit")
	}
	return &bloomFilter{
		capacity: capacity,
		bits: bits,
		hashes: hashes,
		errorRate: errorRate,
		expansion: expansion,
		data: make([]byte, bytesNeeded),
	}, nil
}

func encodeBloom(filter *bloomFilter) []byte {
	out := make([]byte, bloomHeaderSize+len(filter.data))
	copy(out[:4], bloomMagic[:])
	binary.LittleEndian.PutUint64(out[4:12], filter.capacity)
	binary.LittleEndian.PutUint64(out[12:20], filter.items)
	binary.LittleEndian.PutUint64(out[20:28], filter.bits)
	binary.LittleEndian.PutUint32(out[28:32], filter.hashes)
	binary.LittleEndian.PutUint64(out[32:40], math.Float64bits(filter.errorRate))
	binary.LittleEndian.PutUint32(out[40:44], filter.expansion)
	copy(out[bloomHeaderSize:], filter.data)
	return out
}

func decodeBloom(value []byte) (*bloomFilter, error) {
	if len(value) < bloomHeaderSize || !bytes.Equal(value[:4], bloomMagic[:]) {
		return nil, errors.New("invalid bloom filter")
	}
	filter := &bloomFilter{
		capacity: binary.LittleEndian.Uint64(value[4:12]),
		items: binary.LittleEndian.Uint64(value[12:20]),
		bits: binary.LittleEndian.Uint64(value[20:28]),
		hashes: binary.LittleEndian.Uint32(value[28:32]),
		errorRate: math.Float64frombits(binary.LittleEndian.Uint64(value[32:40])),
		expansion: binary.LittleEndian.Uint32(value[40:44]),
		data: append([]byte(nil), value[bloomHeaderSize:]...),
	}
	if filter.capacity < 1 || filter.capacity > bloomMaxCapacity ||
		!(filter.errorRate > 0 && filter.errorRate < 1) ||
		filter.bits < 8 || filter.hashes < 1 || filter.expansion < 1 ||
		uint64(len(filter.data)) != (filter.bits+7)/8 {
		return nil, errors.New("invalid bloom filter")
	}
	return filter, nil
}

func bloomPreparedEntry(value []byte) preparedEntry {
	e := preparedEntry{
		entry: entry{entryData: entryData{
			codecID:   0,
			valueType: TypeBloom,
			rawLength: uint32(len(value)),
		}},
		data: append([]byte(nil), value...),
	}
	return e
}

func bloomMix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func bloomPositions(filter *bloomFilter, item []byte, visit func(uint64) bool) bool {
	h1 := murmurHash64A(item)
	h2 := bloomMix64(h1 ^ uint64(len(item)))
	if h2 == 0 {
		h2 = 0x9e3779b97f4a7c15
	}
	for i := uint32(0); i < filter.hashes; i++ {
		pos := (h1 + uint64(i)*h2) % filter.bits
		if !visit(pos) {
			return false
		}
	}
	return true
}

func bloomContains(filter *bloomFilter, item []byte) bool {
	return bloomPositions(filter, item, func(pos uint64) bool {
		return filter.data[pos>>3]&(1<<uint(pos&7)) != 0
	})
}

func bloomInsert(filter *bloomFilter, item []byte) bool {
	if bloomContains(filter, item) {
		return false
	}
	bloomPositions(filter, item, func(pos uint64) bool {
		filter.data[pos>>3] |= 1 << uint(pos&7)
		return true
	})
	filter.items++
	return true
}

func (s *Store) BloomReserve(key string, errorRate float64, capacity uint64) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if e, ok := sh.get(key); ok && !sh.expired(key, e, s.now()) {
		return errors.New("ERR item exists")
	}

	filter, err := newBloom(capacity, errorRate, bloomDefaultExpansion)
	if err != nil {
		return err
	}
	return s.publish(sh, key, bloomPreparedEntry(encodeBloom(filter)))
}

func (s *Store) BloomAdd(key string, item []byte) (bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if ok && sh.expired(key, e, now) {
		s.remove(sh, key)
		ok = false
	}

	var (
		filter *bloomFilter
		exp stamp
		err error
	)
	if !ok {
		filter, err = newBloom(bloomDefaultCapacity, bloomDefaultError, bloomDefaultExpansion)
		if err != nil {
			return false, err
		}
	} else {
		if e.valueType != TypeBloom {
			return false, bloomWrongType()
		}
		filter, err = decodeBloom(s.decode(sh, e))
		if err != nil {
			return false, err
		}
		exp = sh.expirationAt(key, e)
	}

	added := bloomInsert(filter, item)
	if !added {
		return false, nil
	}

	prepared := bloomPreparedEntry(encodeBloom(filter))
	prepared.expiresAt = exp
	if err := s.publish(sh, key, prepared); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) BloomExists(key string, item []byte) (bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return false, nil
	}
	// RedisBloom returns 0 rather than WRONGTYPE for BF.EXISTS on an ordinary
	// string key.
	if e.valueType != TypeBloom {
		return false, nil
	}
	filter, err := decodeBloom(s.decode(sh, e))
	if err != nil {
		return false, err
	}
	return bloomContains(filter, item), nil
}

func (s *Store) BloomCard(key string) (uint64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, errors.New("ERR not found")
	}
	if e.valueType != TypeBloom {
		return 0, bloomWrongType()
	}
	filter, err := decodeBloom(s.decode(sh, e))
	if err != nil {
		return 0, err
	}
	return filter.items, nil
}

func (s *Store) BloomInfo(key string) (BloomInfo, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return BloomInfo{}, errors.New("ERR not found")
	}
	if e.valueType != TypeBloom {
		return BloomInfo{}, bloomWrongType()
	}
	filter, err := decodeBloom(s.decode(sh, e))
	if err != nil {
		return BloomInfo{}, err
	}
	// RedisBloom's SIZE includes module/filter bookkeeping beyond the raw bitset.
	// The audited FLAT-like single-filter case is bitset bytes + 120 bytes.
	return BloomInfo{
		Capacity: filter.capacity,
		Size: uint64(len(filter.data)) + 120,
		Filters: 1,
		Items: filter.items,
		Expansion: filter.expansion,
	}, nil
}

func (s *Store) BloomInsert(key string, capacity uint64, errorRate float64, items [][]byte) ([]bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if ok && sh.expired(key, e, now) {
		s.remove(sh, key)
		ok = false
	}

	var (
		filter *bloomFilter
		exp stamp
		err error
	)
	if !ok {
		if capacity == 0 {
			capacity = bloomDefaultCapacity
		}
		if errorRate == 0 {
			errorRate = bloomDefaultError
		}
		filter, err = newBloom(capacity, errorRate, bloomDefaultExpansion)
		if err != nil {
			return nil, err
		}
	} else {
		if e.valueType != TypeBloom {
			return nil, bloomWrongType()
		}
		filter, err = decodeBloom(s.decode(sh, e))
		if err != nil {
			return nil, err
		}
		exp = sh.expirationAt(key, e)
	}

	results := make([]bool, len(items))
	changed := false
	for i, item := range items {
		results[i] = bloomInsert(filter, item)
		changed = changed || results[i]
	}
	if !changed {
		return results, nil
	}
	prepared := bloomPreparedEntry(encodeBloom(filter))
	prepared.expiresAt = exp
	if err := s.publish(sh, key, prepared); err != nil {
		return nil, err
	}
	return results, nil
}
