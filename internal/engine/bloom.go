package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
)

const (
	bloomChainHeaderSize   = 32
	bloomFilterHeaderSize  = 32
	bloomLegacyHeaderSize  = 48
	bloomDefaultCapacity   = uint64(100)
	bloomDefaultError      = 0.01
	bloomDefaultExpansion  = uint32(2)
	bloomMaxCapacity       = uint64(1 << 30)
	bloomTighteningRatio   = 0.5
	bloomInfoChainOverhead = uint64(32)
	bloomInfoFilterOverhead = uint64(64)
)

var (
	bloomMagic       = [4]byte{'S', 'B', 'F', 2}
	bloomLegacyMagic = [4]byte{'S', 'B', 'F', 1}
	errBloomFull     = errors.New("ERR non scaling filter is full")
)

type BloomInfo struct {
	Capacity  uint64
	Size      uint64
	Filters   uint64
	Items     uint64
	Expansion uint32
	Scaling   bool
}

type BloomOptions struct {
	Expansion  uint32
	NonScaling bool
}

type BloomInsertResult struct {
	Added bool
	Err   error
}

type bloomSubFilter struct {
	capacity  uint64
	items     uint64
	bits      uint64
	hashes    uint32
	errorRate float64
	data      []byte
}

type bloomFilter struct {
	errorRate float64
	expansion uint32
	scaling   bool
	items     uint64
	filters   []bloomSubFilter
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

func bloomSubFilterFor(capacity uint64, errorRate float64) (bloomSubFilter, error) {
	if capacity < 1 || capacity > bloomMaxCapacity {
		return bloomSubFilter{}, errors.New("ERR capacity must be in the range [1, 1073741824]")
	}
	bits, hashes := bloomParameters(capacity, errorRate)
	bytesNeeded := (bits + 7) / 8
	if bytesNeeded > 32<<20-bloomChainHeaderSize-bloomFilterHeaderSize {
		return bloomSubFilter{}, errors.New("ERR bloom filter exceeds 32 MiB limit")
	}
	return bloomSubFilter{
		capacity: capacity,
		bits: bits,
		hashes: hashes,
		errorRate: errorRate,
		data: make([]byte, bytesNeeded),
	}, nil
}

func newBloom(capacity uint64, errorRate float64, options BloomOptions) (*bloomFilter, error) {
	if !(errorRate > 0 && errorRate < 1) {
		return nil, errors.New("ERR error rate must be in the range (0.000000, 1.000000)")
	}
	if capacity < 1 || capacity > bloomMaxCapacity {
		return nil, errors.New("ERR capacity must be in the range [1, 1073741824]")
	}

	expansion := options.Expansion
	scaling := !options.NonScaling
	if expansion == 0 {
		scaling = false
	}
	if scaling && expansion == 0 {
		expansion = bloomDefaultExpansion
	}
	if scaling && expansion > 32768 {
		return nil, errors.New("ERR expansion must be in the range [0, 32768]")
	}

	firstError := errorRate
	if scaling {
		firstError *= bloomTighteningRatio
	}
	first, err := bloomSubFilterFor(capacity, firstError)
	if err != nil {
		return nil, err
	}
	return &bloomFilter{
		errorRate: errorRate,
		expansion: expansion,
		scaling: scaling,
		filters: []bloomSubFilter{first},
	}, nil
}

func encodeBloom(filter *bloomFilter) []byte {
	size := bloomChainHeaderSize
	for i := range filter.filters {
		size += bloomFilterHeaderSize + len(filter.filters[i].data)
	}
	out := make([]byte, size)
	copy(out[:4], bloomMagic[:])
	binary.LittleEndian.PutUint64(out[4:12], math.Float64bits(filter.errorRate))
	binary.LittleEndian.PutUint32(out[12:16], filter.expansion)
	if filter.scaling {
		binary.LittleEndian.PutUint32(out[16:20], 1)
	}
	binary.LittleEndian.PutUint32(out[20:24], uint32(len(filter.filters)))
	binary.LittleEndian.PutUint64(out[24:32], filter.items)

	offset := bloomChainHeaderSize
	for i := range filter.filters {
		sub := &filter.filters[i]
		binary.LittleEndian.PutUint64(out[offset:offset+8], sub.capacity)
		binary.LittleEndian.PutUint64(out[offset+8:offset+16], sub.items)
		binary.LittleEndian.PutUint64(out[offset+16:offset+24], sub.bits)
		binary.LittleEndian.PutUint32(out[offset+24:offset+28], sub.hashes)
		binary.LittleEndian.PutUint32(out[offset+28:offset+32], uint32(len(sub.data)))
		offset += bloomFilterHeaderSize
		copy(out[offset:offset+len(sub.data)], sub.data)
		offset += len(sub.data)
	}
	return out
}

func decodeLegacyBloom(value []byte) (*bloomFilter, error) {
	if len(value) < bloomLegacyHeaderSize || !bytes.Equal(value[:4], bloomLegacyMagic[:]) {
		return nil, errors.New("invalid bloom filter")
	}
	capacity := binary.LittleEndian.Uint64(value[4:12])
	items := binary.LittleEndian.Uint64(value[12:20])
	bits := binary.LittleEndian.Uint64(value[20:28])
	hashes := binary.LittleEndian.Uint32(value[28:32])
	errorRate := math.Float64frombits(binary.LittleEndian.Uint64(value[32:40]))
	expansion := binary.LittleEndian.Uint32(value[40:44])
	data := append([]byte(nil), value[bloomLegacyHeaderSize:]...)
	if capacity < 1 || capacity > bloomMaxCapacity ||
		!(errorRate > 0 && errorRate < 1) || bits < 8 || hashes < 1 ||
		uint64(len(data)) != (bits+7)/8 {
		return nil, errors.New("invalid bloom filter")
	}
	if expansion == 0 {
		expansion = bloomDefaultExpansion
	}
	return &bloomFilter{
		errorRate: errorRate,
		expansion: expansion,
		scaling: true,
		items: items,
		filters: []bloomSubFilter{{
			capacity: capacity,
			items: items,
			bits: bits,
			hashes: hashes,
			errorRate: errorRate,
			data: data,
		}},
	}, nil
}

func decodeBloom(value []byte) (*bloomFilter, error) {
	if len(value) >= 4 && bytes.Equal(value[:4], bloomLegacyMagic[:]) {
		return decodeLegacyBloom(value)
	}
	if len(value) < bloomChainHeaderSize || !bytes.Equal(value[:4], bloomMagic[:]) {
		return nil, errors.New("invalid bloom filter")
	}

	filter := &bloomFilter{
		errorRate: math.Float64frombits(binary.LittleEndian.Uint64(value[4:12])),
		expansion: binary.LittleEndian.Uint32(value[12:16]),
		scaling: binary.LittleEndian.Uint32(value[16:20]) != 0,
		items: binary.LittleEndian.Uint64(value[24:32]),
	}
	count := binary.LittleEndian.Uint32(value[20:24])
	if !(filter.errorRate > 0 && filter.errorRate < 1) || count == 0 ||
		(filter.scaling && (filter.expansion == 0 || filter.expansion > 32768)) {
		return nil, errors.New("invalid bloom filter")
	}

	offset := bloomChainHeaderSize
	var counted uint64
	filter.filters = make([]bloomSubFilter, 0, count)
	for i := uint32(0); i < count; i++ {
		if offset+bloomFilterHeaderSize > len(value) {
			return nil, errors.New("invalid bloom filter")
		}
		sub := bloomSubFilter{
			capacity: binary.LittleEndian.Uint64(value[offset:offset+8]),
			items: binary.LittleEndian.Uint64(value[offset+8:offset+16]),
			bits: binary.LittleEndian.Uint64(value[offset+16:offset+24]),
			hashes: binary.LittleEndian.Uint32(value[offset+24:offset+28]),
		}
		dataLen := binary.LittleEndian.Uint32(value[offset+28:offset+32])
		offset += bloomFilterHeaderSize
		if sub.capacity < 1 || sub.capacity > bloomMaxCapacity ||
			sub.items > sub.capacity || sub.bits < 8 || sub.hashes < 1 ||
			uint64(dataLen) != (sub.bits+7)/8 || offset+int(dataLen) > len(value) {
			return nil, errors.New("invalid bloom filter")
		}
		sub.data = append([]byte(nil), value[offset:offset+int(dataLen)]...)
		offset += int(dataLen)
		counted += sub.items
		filter.filters = append(filter.filters, sub)
	}
	if offset != len(value) || counted != filter.items {
		return nil, errors.New("invalid bloom filter")
	}

	for i := range filter.filters {
		errRate := filter.errorRate
		if filter.scaling {
			errRate *= math.Pow(bloomTighteningRatio, float64(i+1))
		}
		filter.filters[i].errorRate = errRate
	}
	return filter, nil
}

func bloomPreparedEntry(value []byte) preparedEntry {
	return preparedEntry{
		entry: entry{entryData: entryData{
			codecID: 0, valueType: TypeBloom, rawLength: uint32(len(value)),
		}},
		data: append([]byte(nil), value...),
	}
}

func bloomMix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func bloomPositions(filter *bloomSubFilter, item []byte, visit func(uint64) bool) bool {
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

func bloomSubContains(filter *bloomSubFilter, item []byte) bool {
	return bloomPositions(filter, item, func(pos uint64) bool {
		return filter.data[pos>>3]&(1<<uint(pos&7)) != 0
	})
}

func bloomContains(filter *bloomFilter, item []byte) bool {
	for i := range filter.filters {
		if bloomSubContains(&filter.filters[i], item) {
			return true
		}
	}
	return false
}

func bloomGrow(filter *bloomFilter) error {
	if !filter.scaling || filter.expansion == 0 {
		return errBloomFull
	}
	last := &filter.filters[len(filter.filters)-1]
	if last.capacity > bloomMaxCapacity/uint64(filter.expansion) {
		return errors.New("ERR bloom filter capacity exceeds maximum")
	}
	capacity := last.capacity * uint64(filter.expansion)
	errorRate := filter.errorRate * math.Pow(bloomTighteningRatio, float64(len(filter.filters)+1))
	next, err := bloomSubFilterFor(capacity, errorRate)
	if err != nil {
		return err
	}
	filter.filters = append(filter.filters, next)
	return nil
}

func bloomInsert(filter *bloomFilter, item []byte) (bool, error) {
	if bloomContains(filter, item) {
		return false, nil
	}
	last := &filter.filters[len(filter.filters)-1]
	if last.items >= last.capacity {
		if err := bloomGrow(filter); err != nil {
			return false, err
		}
		last = &filter.filters[len(filter.filters)-1]
	}
	bloomPositions(last, item, func(pos uint64) bool {
		last.data[pos>>3] |= 1 << uint(pos&7)
		return true
	})
	last.items++
	filter.items++
	return true, nil
}

func (s *Store) BloomReserveWithOptions(key string, errorRate float64, capacity uint64, options BloomOptions) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if e, ok := sh.get(key); ok && !sh.expired(key, e, s.now()) {
		return errors.New("ERR item exists")
	}
	filter, err := newBloom(capacity, errorRate, options)
	if err != nil {
		return err
	}
	return s.publish(sh, key, bloomPreparedEntry(encodeBloom(filter)))
}

func (s *Store) BloomReserve(key string, errorRate float64, capacity uint64) error {
	return s.BloomReserveWithOptions(key, errorRate, capacity, BloomOptions{Expansion: bloomDefaultExpansion})
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

	var filter *bloomFilter
	var exp stamp
	var err error
	if !ok {
		filter, err = newBloom(bloomDefaultCapacity, bloomDefaultError, BloomOptions{Expansion: bloomDefaultExpansion})
	} else {
		if e.valueType != TypeBloom {
			return false, bloomWrongType()
		}
		filter, err = decodeBloom(s.decode(sh, e))
		exp = sh.expirationAt(key, e)
	}
	if err != nil {
		return false, err
	}

	added, err := bloomInsert(filter, item)
	if err != nil || !added {
		return added, err
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

func roundBloomInfoBytes(n uint64) uint64 {
	return (n + 7) &^ 7
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

	info := BloomInfo{
		Size: bloomInfoChainOverhead,
		Filters: uint64(len(filter.filters)),
		Items: filter.items,
		Expansion: filter.expansion,
		Scaling: filter.scaling,
	}
	for i := range filter.filters {
		sub := &filter.filters[i]
		info.Capacity += sub.capacity
		info.Size += bloomInfoFilterOverhead + roundBloomInfoBytes(uint64(len(sub.data)))
	}
	return info, nil
}

func (s *Store) BloomInsertWithOptions(key string, capacity uint64, errorRate float64, options BloomOptions, items [][]byte) ([]BloomInsertResult, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if ok && sh.expired(key, e, now) {
		s.remove(sh, key)
		ok = false
	}

	var filter *bloomFilter
	var exp stamp
	var err error
	if !ok {
		if capacity == 0 {
			capacity = bloomDefaultCapacity
		}
		if errorRate == 0 {
			errorRate = bloomDefaultError
		}
		if options.Expansion == 0 && !options.NonScaling {
			options.Expansion = bloomDefaultExpansion
		}
		filter, err = newBloom(capacity, errorRate, options)
	} else {
		if e.valueType != TypeBloom {
			return nil, bloomWrongType()
		}
		filter, err = decodeBloom(s.decode(sh, e))
		exp = sh.expirationAt(key, e)
	}
	if err != nil {
		return nil, err
	}

	results := make([]BloomInsertResult, len(items))
	changed := false
	for i, item := range items {
		added, insertErr := bloomInsert(filter, item)
		results[i] = BloomInsertResult{Added: added, Err: insertErr}
		if insertErr == nil && added {
			changed = true
		}
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

func (s *Store) BloomInsert(key string, capacity uint64, errorRate float64, items [][]byte) ([]bool, error) {
	results, err := s.BloomInsertWithOptions(key, capacity, errorRate, BloomOptions{Expansion: bloomDefaultExpansion}, items)
	if err != nil {
		return nil, err
	}
	out := make([]bool, len(results))
	for i := range results {
		if results[i].Err != nil {
			return nil, results[i].Err
		}
		out[i] = results[i].Added
	}
	return out, nil
}
