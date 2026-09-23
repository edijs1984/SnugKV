package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
)

const (
	cmsHeaderSize = 32
	cmsCellSize   = uint64(4)
)

var cmsMagic = [4]byte{'S', 'C', 'M', 1}

type CMSInfo struct {
	Width uint64
	Depth uint64
	Count uint64
}

type cmsSketch struct {
	width uint64
	depth uint64
	count uint64
	cells []uint32
}

func cmsWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func newCMS(width, depth uint64) (*cmsSketch, error) {
	if width < 1 {
		return nil, errors.New("CMS: invalid width")
	}
	if depth < 1 {
		return nil, errors.New("CMS: invalid depth")
	}
	if width > math.MaxInt64/depth || width*depth > (32<<20-cmsHeaderSize)/cmsCellSize {
		return nil, errors.New("CMS: invalid init arguments")
	}
	return &cmsSketch{
		width: width,
		depth: depth,
		cells: make([]uint32, int(width*depth)),
	}, nil
}

func cmsDimsFromProb(overestimation, probability float64) (uint64, uint64, error) {
	if !(overestimation > 0 && overestimation < 1) {
		return 0, 0, errors.New("CMS: invalid overestimation value")
	}
	if !(probability > 0 && probability < 1) {
		return 0, 0, errors.New("CMS: invalid prob value")
	}
	width := uint64(math.Ceil(2 / overestimation))
	depth := uint64(math.Ceil(math.Log10(probability) / math.Log10(0.5)))
	if width < 1 || depth < 1 {
		return 0, 0, errors.New("CMS: invalid init arguments")
	}
	return width, depth, nil
}

func cmsPreparedEntry(value []byte) preparedEntry {
	return preparedEntry{
		entry: entry{entryData: entryData{
			codecID: 0, valueType: TypeCMS, rawLength: uint32(len(value)),
		}},
		data: append([]byte(nil), value...),
	}
}

func encodeCMS(cms *cmsSketch) []byte {
	out := make([]byte, cmsHeaderSize+len(cms.cells)*4)
	copy(out[:4], cmsMagic[:])
	binary.LittleEndian.PutUint64(out[4:12], cms.width)
	binary.LittleEndian.PutUint64(out[12:20], cms.depth)
	binary.LittleEndian.PutUint64(out[20:28], cms.count)
	offset := cmsHeaderSize
	for _, cell := range cms.cells {
		binary.LittleEndian.PutUint32(out[offset:offset+4], cell)
		offset += 4
	}
	return out
}

func decodeCMS(value []byte) (*cmsSketch, error) {
	if len(value) < cmsHeaderSize || !bytes.Equal(value[:4], cmsMagic[:]) {
		return nil, errors.New("invalid CMS sketch")
	}
	width := binary.LittleEndian.Uint64(value[4:12])
	depth := binary.LittleEndian.Uint64(value[12:20])
	count := binary.LittleEndian.Uint64(value[20:28])
	if width < 1 || depth < 1 || width > math.MaxInt64/depth {
		return nil, errors.New("invalid CMS sketch")
	}
	cellCount := width * depth
	if cellCount > uint64((len(value)-cmsHeaderSize)/4) ||
		cmsHeaderSize+int(cellCount)*4 != len(value) ||
		count > math.MaxInt64 {
		return nil, errors.New("invalid CMS sketch")
	}
	cms := &cmsSketch{
		width: width,
		depth: depth,
		count: count,
		cells: make([]uint32, int(cellCount)),
	}
	offset := cmsHeaderSize
	for i := range cms.cells {
		cms.cells[i] = binary.LittleEndian.Uint32(value[offset:offset+4])
		offset += 4
	}
	return cms, nil
}

func murmurHash2CMS(data []byte, seed uint32) uint32 {
	const m uint32 = 0x5bd1e995
	const r = 24

	h := seed ^ uint32(len(data))
	i := 0
	for ; i+4 <= len(data); i += 4 {
		k := binary.LittleEndian.Uint32(data[i:i+4])
		k *= m
		k ^= k >> r
		k *= m
		h *= m
		h ^= k
	}
	tail := data[i:]
	switch len(tail) {
	case 3:
		h ^= uint32(tail[2]) << 16
		fallthrough
	case 2:
		h ^= uint32(tail[1]) << 8
		fallthrough
	case 1:
		h ^= uint32(tail[0])
		h *= m
	}
	h ^= h >> 13
	h *= m
	h ^= h >> 15
	return h
}

func cmsQuery(cms *cmsSketch, item []byte) uint64 {
	min := uint64(math.MaxUint32)
	for row := uint64(0); row < cms.depth; row++ {
		hash := murmurHash2CMS(item, uint32(row))
		loc := row*cms.width + uint64(hash)%cms.width
		value := uint64(cms.cells[loc])
		if value < min {
			min = value
		}
	}
	return min
}

func cmsIncrement(cms *cmsSketch, item []byte, value uint64) (uint64, error) {
	if value == 0 {
		return cmsQuery(cms, item), nil
	}
	if value > math.MaxInt64-cms.count {
		return 0, errors.New("CMS: INCRBY overflow")
	}
	locs := make([]uint64, cms.depth)
	for row := uint64(0); row < cms.depth; row++ {
		hash := murmurHash2CMS(item, uint32(row))
		loc := row*cms.width + uint64(hash)%cms.width
		if value > math.MaxUint32-uint64(cms.cells[loc]) {
			return 0, errors.New("CMS: INCRBY overflow")
		}
		locs[row] = loc
	}
	min := uint64(math.MaxUint32)
	for _, loc := range locs {
		cms.cells[loc] += uint32(value)
		if uint64(cms.cells[loc]) < min {
			min = uint64(cms.cells[loc])
		}
	}
	cms.count += value
	return min, nil
}

func (s *Store) CMSInitByDim(key string, width, depth uint64) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if e, ok := sh.get(key); ok && !sh.expired(key, e, s.now()) {
		return errors.New("CMS: key already exists")
	}
	cms, err := newCMS(width, depth)
	if err != nil {
		return err
	}
	return s.publish(sh, key, cmsPreparedEntry(encodeCMS(cms)))
}

func (s *Store) CMSInitByProb(key string, overestimation, probability float64) error {
	width, depth, err := cmsDimsFromProb(overestimation, probability)
	if err != nil {
		return err
	}
	return s.CMSInitByDim(key, width, depth)
}

func (s *Store) CMSIncrBy(key string, items [][]byte, values []uint64) ([]uint64, error) {
	if len(items) != len(values) {
		return nil, errors.New("CMS: invalid increment")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, errors.New("CMS: key does not exist")
	}
	if e.valueType != TypeCMS {
		return nil, cmsWrongType()
	}
	cms, err := decodeCMS(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	exp := sh.expirationAt(key, e)

	results := make([]uint64, len(items))
	for i := range items {
		results[i], err = cmsIncrement(cms, items[i], values[i])
		if err != nil {
			return nil, err
		}
	}
	prepared := cmsPreparedEntry(encodeCMS(cms))
	prepared.expiresAt = exp
	if err := s.publish(sh, key, prepared); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) CMSQuery(key string, items [][]byte) ([]uint64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, errors.New("CMS: key does not exist")
	}
	if e.valueType != TypeCMS {
		return nil, cmsWrongType()
	}
	cms, err := decodeCMS(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	results := make([]uint64, len(items))
	for i := range items {
		results[i] = cmsQuery(cms, items[i])
	}
	return results, nil
}

func (s *Store) CMSInfo(key string) (CMSInfo, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return CMSInfo{}, errors.New("CMS: key does not exist")
	}
	if e.valueType != TypeCMS {
		return CMSInfo{}, cmsWrongType()
	}
	cms, err := decodeCMS(s.decode(sh, e))
	if err != nil {
		return CMSInfo{}, err
	}
	return CMSInfo{Width: cms.width, Depth: cms.depth, Count: cms.count}, nil
}

func (s *Store) CMSMerge(destination string, sources []string, weights []int64) error {
	if len(sources) == 0 || len(sources) != len(weights) {
		return errors.New("CMS: wrong number of keys/weights")
	}

	unlock := s.lockAll()
	defer unlock()
	now := s.now()

	dstShard := s.shardFor(destination)
	dstEntry, ok := dstShard.get(destination)
	if !ok || dstShard.expired(destination, dstEntry, now) {
		return errors.New("CMS: key does not exist")
	}
	if dstEntry.valueType != TypeCMS {
		return cmsWrongType()
	}
	dest, err := decodeCMS(s.decode(dstShard, dstEntry))
	if err != nil {
		return err
	}

	sourceSketches := make([]*cmsSketch, len(sources))
	for i, key := range sources {
		sh := s.shardFor(key)
		e, ok := sh.get(key)
		if !ok || sh.expired(key, e, now) {
			return errors.New("CMS: key does not exist")
		}
		if e.valueType != TypeCMS {
			return cmsWrongType()
		}
		src, err := decodeCMS(s.decode(sh, e))
		if err != nil {
			return err
		}
		if src.width != dest.width || src.depth != dest.depth {
			return errors.New("CMS: width/depth is not equal")
		}
		sourceSketches[i] = src
	}

	merged := &cmsSketch{
		width: dest.width,
		depth: dest.depth,
		cells: make([]uint32, len(dest.cells)),
	}
	var total int64
	for i, src := range sourceSketches {
		if weights[i] != 0 && src.count > uint64(math.MaxInt64/absInt64(weights[i])) {
			return errors.New("CMS: MERGE overflow")
		}
		part := int64(src.count) * weights[i]
		if (part > 0 && total > math.MaxInt64-part) || (part < 0 && total < math.MinInt64-part) {
			return errors.New("CMS: MERGE overflow")
		}
		total += part
	}
	if total < 0 {
		return errors.New("CMS: MERGE overflow")
	}
	merged.count = uint64(total)

	for cell := range merged.cells {
		var value int64
		for i, src := range sourceSketches {
			part := int64(src.cells[cell]) * weights[i]
			if (part > 0 && value > math.MaxInt64-part) || (part < 0 && value < math.MinInt64-part) {
				return errors.New("CMS: MERGE overflow")
			}
			value += part
		}
		if value < 0 || value > math.MaxUint32 {
			return errors.New("CMS: MERGE overflow")
		}
		merged.cells[cell] = uint32(value)
	}

	prepared := cmsPreparedEntry(encodeCMS(merged))
	prepared.expiresAt = dstShard.expirationAt(destination, dstEntry)
	return s.publish(dstShard, destination, prepared)
}

func absInt64(v int64) int64 {
	if v < 0 {
		if v == math.MinInt64 {
			return math.MaxInt64
		}
		return -v
	}
	return v
}
