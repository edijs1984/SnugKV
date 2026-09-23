package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
)

const (
	cuckooHeaderSize       = 40
	cuckooSubHeaderSize    = 16
	cuckooDefaultCapacity  = uint64(1024)
	cuckooDefaultBucket    = uint16(2)
	cuckooDefaultMaxIter   = uint16(20)
	cuckooDefaultExpansion = uint16(1)
	cuckooMaxCapacity      = uint64(1 << 30)
	cuckooInfoOverhead     = uint64(56)
)

var cuckooMagic = [4]byte{'S', 'C', 'F', 1}

type CuckooInfo struct {
	Size          uint64
	NumBuckets    uint64
	NumFilters    uint64
	NumItems      uint64
	NumDeletes    uint64
	BucketSize    uint16
	Expansion     uint16
	MaxIterations uint16
}

type cuckooSubFilter struct {
	numBuckets uint64
	data       []byte
}

type cuckooFilter struct {
	numBuckets    uint64
	numItems      uint64
	numDeletes    uint64
	bucketSize    uint16
	maxIterations uint16
	expansion     uint16
	filters       []cuckooSubFilter
}

type cuckooLookup struct {
	h1 uint64
	h2 uint64
	fp byte
}

func cuckooWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func nextPowerOfTwo(n uint64) uint64 {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	return n + 1
}

func cuckooHash64(data []byte) uint64 {
	const m uint64 = 0xc6a4a7935bd1e995
	const r = 47

	h := uint64(len(data)) * m
	i := 0
	for ; i+8 <= len(data); i += 8 {
		k := binary.LittleEndian.Uint64(data[i : i+8])
		k *= m
		k ^= k >> r
		k *= m
		h ^= k
		h *= m
	}
	tail := data[i:]
	switch len(tail) {
	case 7:
		h ^= uint64(tail[6]) << 48
		fallthrough
	case 6:
		h ^= uint64(tail[5]) << 40
		fallthrough
	case 5:
		h ^= uint64(tail[4]) << 32
		fallthrough
	case 4:
		h ^= uint64(tail[3]) << 24
		fallthrough
	case 3:
		h ^= uint64(tail[2]) << 16
		fallthrough
	case 2:
		h ^= uint64(tail[1]) << 8
		fallthrough
	case 1:
		h ^= uint64(tail[0])
		h *= m
	}
	h ^= h >> r
	h *= m
	h ^= h >> r
	return h
}

func cuckooAltHash(fp byte, index uint64) uint64 {
	return index ^ (uint64(fp) * 0x5bd1e995)
}

func cuckooLookupFor(item []byte) cuckooLookup {
	hash := cuckooHash64(item)
	fp := byte(hash%255 + 1)
	return cuckooLookup{
		h1: hash,
		h2: cuckooAltHash(fp, hash),
		fp: fp,
	}
}

func newCuckoo(capacity uint64) (*cuckooFilter, error) {
	if capacity < uint64(2*cuckooDefaultBucket) || capacity > cuckooMaxCapacity {
		return nil, errors.New("Capacity must be in the range [2 * BUCKETSIZE, 1073741824]")
	}
	baseBuckets := nextPowerOfTwo(capacity / uint64(cuckooDefaultBucket))
	if baseBuckets == 0 {
		baseBuckets = 1
	}
	cf := &cuckooFilter{
		numBuckets:    baseBuckets,
		bucketSize:    cuckooDefaultBucket,
		maxIterations: cuckooDefaultMaxIter,
		expansion:     cuckooDefaultExpansion,
	}
	if err := cuckooGrow(cf); err != nil {
		return nil, err
	}
	return cf, nil
}

func cuckooGrow(cf *cuckooFilter) error {
	if len(cf.filters) >= 65535 {
		return errors.New("ERR maximum Cuckoo filter expansions reached")
	}
	growth := uint64(1)
	for i := 0; i < len(cf.filters); i++ {
		if cf.expansion == 0 {
			return errors.New("ERR Cuckoo filter is full")
		}
		if growth > ^uint64(0)/uint64(cf.expansion) {
			return errors.New("ERR Cuckoo filter is full")
		}
		growth *= uint64(cf.expansion)
	}
	if cf.numBuckets > ^uint64(0)/growth {
		return errors.New("ERR Cuckoo filter is full")
	}
	buckets := cf.numBuckets * growth
	if buckets > cuckooMaxCapacity {
		return errors.New("ERR Cuckoo filter is full")
	}
	bytesNeeded := buckets * uint64(cf.bucketSize)
	if bytesNeeded > 32<<20 {
		return errors.New("ERR Cuckoo filter exceeds 32 MiB limit")
	}
	cf.filters = append(cf.filters, cuckooSubFilter{
		numBuckets: buckets,
		data:       make([]byte, int(bytesNeeded)),
	})
	return nil
}

func cuckooPreparedEntry(value []byte) preparedEntry {
	return preparedEntry{
		entry: entry{entryData: entryData{
			codecID:   0,
			valueType: TypeCuckoo,
			rawLength: uint32(len(value)),
		}},
		data: append([]byte(nil), value...),
	}
}

func encodeCuckoo(cf *cuckooFilter) []byte {
	size := cuckooHeaderSize
	for i := range cf.filters {
		size += cuckooSubHeaderSize + len(cf.filters[i].data)
	}
	out := make([]byte, size)
	copy(out[:4], cuckooMagic[:])
	binary.LittleEndian.PutUint64(out[4:12], cf.numBuckets)
	binary.LittleEndian.PutUint64(out[12:20], cf.numItems)
	binary.LittleEndian.PutUint64(out[20:28], cf.numDeletes)
	binary.LittleEndian.PutUint16(out[28:30], cf.bucketSize)
	binary.LittleEndian.PutUint16(out[30:32], cf.maxIterations)
	binary.LittleEndian.PutUint16(out[32:34], cf.expansion)
	binary.LittleEndian.PutUint16(out[34:36], uint16(len(cf.filters)))

	offset := cuckooHeaderSize
	for i := range cf.filters {
		sub := &cf.filters[i]
		binary.LittleEndian.PutUint64(out[offset:offset+8], sub.numBuckets)
		binary.LittleEndian.PutUint64(out[offset+8:offset+16], uint64(len(sub.data)))
		offset += cuckooSubHeaderSize
		copy(out[offset:offset+len(sub.data)], sub.data)
		offset += len(sub.data)
	}
	return out
}

func decodeCuckoo(value []byte) (*cuckooFilter, error) {
	if len(value) < cuckooHeaderSize || !bytes.Equal(value[:4], cuckooMagic[:]) {
		return nil, errors.New("invalid Cuckoo filter")
	}
	cf := &cuckooFilter{
		numBuckets:    binary.LittleEndian.Uint64(value[4:12]),
		numItems:      binary.LittleEndian.Uint64(value[12:20]),
		numDeletes:    binary.LittleEndian.Uint64(value[20:28]),
		bucketSize:    binary.LittleEndian.Uint16(value[28:30]),
		maxIterations: binary.LittleEndian.Uint16(value[30:32]),
		expansion:     binary.LittleEndian.Uint16(value[32:34]),
	}
	count := binary.LittleEndian.Uint16(value[34:36])
	if cf.numBuckets == 0 || cf.bucketSize == 0 || cf.maxIterations == 0 || count == 0 {
		return nil, errors.New("invalid Cuckoo filter")
	}

	offset := cuckooHeaderSize
	cf.filters = make([]cuckooSubFilter, 0, count)
	for i := uint16(0); i < count; i++ {
		if offset+cuckooSubHeaderSize > len(value) {
			return nil, errors.New("invalid Cuckoo filter")
		}
		buckets := binary.LittleEndian.Uint64(value[offset : offset+8])
		dataLen := binary.LittleEndian.Uint64(value[offset+8 : offset+16])
		offset += cuckooSubHeaderSize
		if buckets == 0 ||
			dataLen != buckets*uint64(cf.bucketSize) ||
			dataLen > uint64(len(value)-offset) {
			return nil, errors.New("invalid Cuckoo filter")
		}
		cf.filters = append(cf.filters, cuckooSubFilter{
			numBuckets: buckets,
			data:       append([]byte(nil), value[offset:offset+int(dataLen)]...),
		})
		offset += int(dataLen)
	}
	if offset != len(value) {
		return nil, errors.New("invalid Cuckoo filter")
	}
	return cf, nil
}

func cuckooBucketOffset(sub *cuckooSubFilter, bucketSize uint16, hash uint64) int {
	return int((hash % sub.numBuckets) * uint64(bucketSize))
}

func cuckooBucketFind(data []byte, offset int, bucketSize uint16, fp byte) bool {
	for i := 0; i < int(bucketSize); i++ {
		if data[offset+i] == fp {
			return true
		}
	}
	return false
}

func cuckooBucketCount(data []byte, offset int, bucketSize uint16, fp byte) uint64 {
	var count uint64
	for i := 0; i < int(bucketSize); i++ {
		if data[offset+i] == fp {
			count++
		}
	}
	return count
}

func cuckooBucketAvailable(data []byte, offset int, bucketSize uint16) int {
	for i := 0; i < int(bucketSize); i++ {
		if data[offset+i] == 0 {
			return offset + i
		}
	}
	return -1
}

func cuckooFilterFind(sub *cuckooSubFilter, bucketSize uint16, lookup cuckooLookup) bool {
	loc1 := cuckooBucketOffset(sub, bucketSize, lookup.h1)
	loc2 := cuckooBucketOffset(sub, bucketSize, lookup.h2)
	return cuckooBucketFind(sub.data, loc1, bucketSize, lookup.fp) ||
		cuckooBucketFind(sub.data, loc2, bucketSize, lookup.fp)
}

func cuckooContains(cf *cuckooFilter, lookup cuckooLookup) bool {
	for i := range cf.filters {
		if cuckooFilterFind(&cf.filters[i], cf.bucketSize, lookup) {
			return true
		}
	}
	return false
}

func cuckooCount(cf *cuckooFilter, lookup cuckooLookup) uint64 {
	var total uint64
	for i := range cf.filters {
		sub := &cf.filters[i]
		loc1 := cuckooBucketOffset(sub, cf.bucketSize, lookup.h1)
		loc2 := cuckooBucketOffset(sub, cf.bucketSize, lookup.h2)
		total += cuckooBucketCount(sub.data, loc1, cf.bucketSize, lookup.fp)
		total += cuckooBucketCount(sub.data, loc2, cf.bucketSize, lookup.fp)
	}
	return total
}

func cuckooFindAvailable(sub *cuckooSubFilter, bucketSize uint16, lookup cuckooLookup) int {
	loc1 := cuckooBucketOffset(sub, bucketSize, lookup.h1)
	if slot := cuckooBucketAvailable(sub.data, loc1, bucketSize); slot >= 0 {
		return slot
	}
	loc2 := cuckooBucketOffset(sub, bucketSize, lookup.h2)
	return cuckooBucketAvailable(sub.data, loc2, bucketSize)
}

func cuckooKickInsert(cf *cuckooFilter, sub *cuckooSubFilter, lookup cuckooLookup) bool {
	fp := lookup.fp
	bucket := lookup.h1 % sub.numBuckets
	victim := 0

	for counter := uint16(0); counter < cf.maxIterations; counter++ {
		offset := int(bucket*uint64(cf.bucketSize)) + victim
		sub.data[offset], fp = fp, sub.data[offset]
		bucket = cuckooAltHash(fp, bucket) % sub.numBuckets
		target := int(bucket * uint64(cf.bucketSize))
		if slot := cuckooBucketAvailable(sub.data, target, cf.bucketSize); slot >= 0 {
			sub.data[slot] = fp
			return true
		}
		victim = (victim + 1) % int(cf.bucketSize)
	}

	// Roll back exactly like RedisBloom.
	for counter := uint16(0); counter < cf.maxIterations; counter++ {
		victim = (victim + int(cf.bucketSize) - 1) % int(cf.bucketSize)
		bucket = cuckooAltHash(fp, bucket) % sub.numBuckets
		offset := int(bucket*uint64(cf.bucketSize)) + victim
		sub.data[offset], fp = fp, sub.data[offset]
	}
	return false
}

func cuckooInsert(cf *cuckooFilter, item []byte, unique bool) (bool, error) {
	lookup := cuckooLookupFor(item)
	if unique && cuckooContains(cf, lookup) {
		return false, nil
	}

	for i := len(cf.filters) - 1; i >= 0; i-- {
		if slot := cuckooFindAvailable(&cf.filters[i], cf.bucketSize, lookup); slot >= 0 {
			cf.filters[i].data[slot] = lookup.fp
			cf.numItems++
			return true, nil
		}
	}

	last := &cf.filters[len(cf.filters)-1]
	if cuckooKickInsert(cf, last, lookup) {
		cf.numItems++
		return true, nil
	}

	if cf.expansion == 0 {
		return false, errors.New("ERR Cuckoo filter is full")
	}
	if err := cuckooGrow(cf); err != nil {
		return false, err
	}
	return cuckooInsert(cf, item, unique)
}

func cuckooDelete(cf *cuckooFilter, item []byte) bool {
	lookup := cuckooLookupFor(item)
	for i := len(cf.filters) - 1; i >= 0; i-- {
		sub := &cf.filters[i]
		loc1 := cuckooBucketOffset(sub, cf.bucketSize, lookup.h1)
		for j := 0; j < int(cf.bucketSize); j++ {
			if sub.data[loc1+j] == lookup.fp {
				sub.data[loc1+j] = 0
				cf.numItems--
				cf.numDeletes++
				return true
			}
		}
		loc2 := cuckooBucketOffset(sub, cf.bucketSize, lookup.h2)
		for j := 0; j < int(cf.bucketSize); j++ {
			if sub.data[loc2+j] == lookup.fp {
				sub.data[loc2+j] = 0
				cf.numItems--
				cf.numDeletes++
				return true
			}
		}
	}
	return false
}

func (s *Store) CuckooReserve(key string, capacity uint64) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if e, ok := sh.get(key); ok && !sh.expired(key, e, s.now()) {
		return errors.New("ERR item exists")
	}
	cf, err := newCuckoo(capacity)
	if err != nil {
		return err
	}
	return s.publish(sh, key, cuckooPreparedEntry(encodeCuckoo(cf)))
}

func (s *Store) cuckooMutate(key string, create bool, capacity uint64, fn func(*cuckooFilter) error) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.get(key)
	if ok && sh.expired(key, e, now) {
		s.remove(sh, key)
		ok = false
	}

	var cf *cuckooFilter
	var exp stamp
	var err error
	if !ok {
		if !create {
			return errors.New("ERR not found")
		}
		if capacity == 0 {
			capacity = cuckooDefaultCapacity
		}
		cf, err = newCuckoo(capacity)
	} else {
		if e.valueType != TypeCuckoo {
			return cuckooWrongType()
		}
		cf, err = decodeCuckoo(s.decode(sh, e))
		exp = sh.expirationAt(key, e)
	}
	if err != nil {
		return err
	}
	if err := fn(cf); err != nil {
		return err
	}
	prepared := cuckooPreparedEntry(encodeCuckoo(cf))
	prepared.expiresAt = exp
	return s.publish(sh, key, prepared)
}

func (s *Store) CuckooAdd(key string, item []byte, unique bool) (bool, error) {
	var added bool
	err := s.cuckooMutate(key, true, 0, func(cf *cuckooFilter) error {
		var err error
		added, err = cuckooInsert(cf, item, unique)
		return err
	})
	return added, err
}

func (s *Store) CuckooInsert(key string, capacity uint64, items [][]byte, unique bool) ([]bool, error) {
	results := make([]bool, len(items))
	err := s.cuckooMutate(key, true, capacity, func(cf *cuckooFilter) error {
		for i, item := range items {
			added, err := cuckooInsert(cf, item, unique)
			if err != nil {
				return err
			}
			results[i] = added
		}
		return nil
	})
	return results, err
}

func (s *Store) CuckooExists(key string, item []byte) (bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return false, nil
	}
	if e.valueType != TypeCuckoo {
		return false, nil
	}
	cf, err := decodeCuckoo(s.decode(sh, e))
	if err != nil {
		return false, err
	}
	return cuckooContains(cf, cuckooLookupFor(item)), nil
}

func (s *Store) CuckooCount(key string, item []byte) (uint64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, nil
	}
	if e.valueType != TypeCuckoo {
		return 0, nil
	}
	cf, err := decodeCuckoo(s.decode(sh, e))
	if err != nil {
		return 0, err
	}
	return cuckooCount(cf, cuckooLookupFor(item)), nil
}

func (s *Store) CuckooDelete(key string, item []byte) (bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return false, errors.New("Not found")
	}
	if e.valueType != TypeCuckoo {
		return false, errors.New("Not found")
	}
	cf, err := decodeCuckoo(s.decode(sh, e))
	if err != nil {
		return false, err
	}
	deleted := cuckooDelete(cf, item)
	if !deleted {
		return false, nil
	}
	prepared := cuckooPreparedEntry(encodeCuckoo(cf))
	prepared.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, prepared); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CuckooInfo(key string) (CuckooInfo, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return CuckooInfo{}, errors.New("ERR not found")
	}
	if e.valueType != TypeCuckoo {
		return CuckooInfo{}, cuckooWrongType()
	}
	cf, err := decodeCuckoo(s.decode(sh, e))
	if err != nil {
		return CuckooInfo{}, err
	}
	info := CuckooInfo{
		Size:          cuckooInfoOverhead,
		NumBuckets:    cf.numBuckets,
		NumFilters:    uint64(len(cf.filters)),
		NumItems:      cf.numItems,
		NumDeletes:    cf.numDeletes,
		BucketSize:    cf.bucketSize,
		Expansion:     cf.expansion,
		MaxIterations: cf.maxIterations,
	}
	for i := range cf.filters {
		info.Size += uint64(len(cf.filters[i].data))
	}
	return info, nil
}
