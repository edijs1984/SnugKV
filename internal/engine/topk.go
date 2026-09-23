package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"math/rand"
	"sort"
)

const (
	topKDefaultWidth = uint32(8)
	topKDefaultDepth = uint32(7)
	topKDefaultDecay = 0.9
	topKHashSeed     = uint32(1919)
	topKHeaderSize   = 24
)

var topKMagic = [4]byte{'S', 'T', 'K', 1}

type TopKInfo struct {
	K     uint32
	Width uint32
	Depth uint32
	Decay float64
}

type TopKListEntry struct {
	Item  []byte
	Count uint32
}

type TopKAddResult struct {
	Expelled    []byte
	HasExpelled bool
}

type topKBucket struct {
	fp    uint32
	count uint32
}

type topKHeapBucket struct {
	fp      uint32
	count   uint32
	item    []byte
	present bool
}

type topKSketch struct {
	k     uint32
	width uint32
	depth uint32
	decay float64
	data  []topKBucket
	heap  []topKHeapBucket
}

func topKWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func newTopK(k, width, depth uint32, decay float64) (*topKSketch, error) {
	if k < 1 {
		return nil, errors.New("TopK: invalid k")
	}
	if width < 1 {
		return nil, errors.New("TopK: invalid width")
	}
	if depth < 1 {
		return nil, errors.New("TopK: invalid depth")
	}
	if !(decay > 0 && decay <= 1) || math.IsNaN(decay) {
		return nil, errors.New("TopK: invalid decay value. must be '<= 1' & '> 0'")
	}
	cells := uint64(width) * uint64(depth)
	if cells > uint64((32<<20-topKHeaderSize)/8) {
		return nil, errors.New("ERR Insufficient memory to create topk data structure")
	}
	return &topKSketch{
		k:     k,
		width: width,
		depth: depth,
		decay: decay,
		data:  make([]topKBucket, int(cells)),
		heap:  make([]topKHeapBucket, int(k)),
	}, nil
}

func topKPreparedEntry(value []byte) preparedEntry {
	return preparedEntry{
		entry: entry{entryData: entryData{
			codecID: 0, valueType: TypeTopK, rawLength: uint32(len(value)),
		}},
		data: append([]byte(nil), value...),
	}
}

func encodeTopK(topk *topKSketch) []byte {
	size := topKHeaderSize + len(topk.data)*8
	for i := range topk.heap {
		size += 13 + len(topk.heap[i].item)
	}
	out := make([]byte, size)
	copy(out[:4], topKMagic[:])
	binary.LittleEndian.PutUint32(out[4:8], topk.k)
	binary.LittleEndian.PutUint32(out[8:12], topk.width)
	binary.LittleEndian.PutUint32(out[12:16], topk.depth)
	binary.LittleEndian.PutUint64(out[16:24], math.Float64bits(topk.decay))

	offset := topKHeaderSize
	for _, bucket := range topk.data {
		binary.LittleEndian.PutUint32(out[offset:offset+4], bucket.fp)
		binary.LittleEndian.PutUint32(out[offset+4:offset+8], bucket.count)
		offset += 8
	}
	for _, bucket := range topk.heap {
		if bucket.present {
			out[offset] = 1
		}
		binary.LittleEndian.PutUint32(out[offset+1:offset+5], bucket.fp)
		binary.LittleEndian.PutUint32(out[offset+5:offset+9], bucket.count)
		binary.LittleEndian.PutUint32(out[offset+9:offset+13], uint32(len(bucket.item)))
		offset += 13
		copy(out[offset:offset+len(bucket.item)], bucket.item)
		offset += len(bucket.item)
	}
	return out
}

func decodeTopK(value []byte) (*topKSketch, error) {
	if len(value) < topKHeaderSize || !bytes.Equal(value[:4], topKMagic[:]) {
		return nil, errors.New("invalid TopK value")
	}
	k := binary.LittleEndian.Uint32(value[4:8])
	width := binary.LittleEndian.Uint32(value[8:12])
	depth := binary.LittleEndian.Uint32(value[12:16])
	decay := math.Float64frombits(binary.LittleEndian.Uint64(value[16:24]))
	if k < 1 || width < 1 || depth < 1 || !(decay > 0 && decay <= 1) || math.IsNaN(decay) {
		return nil, errors.New("invalid TopK value")
	}
	cells := uint64(width) * uint64(depth)
	if cells > uint64((len(value)-topKHeaderSize)/8) {
		return nil, errors.New("invalid TopK value")
	}
	offset := topKHeaderSize
	data := make([]topKBucket, int(cells))
	for i := range data {
		if offset+8 > len(value) {
			return nil, errors.New("invalid TopK value")
		}
		data[i] = topKBucket{
			fp:    binary.LittleEndian.Uint32(value[offset : offset+4]),
			count: binary.LittleEndian.Uint32(value[offset+4 : offset+8]),
		}
		offset += 8
	}
	heap := make([]topKHeapBucket, int(k))
	for i := range heap {
		if offset+13 > len(value) {
			return nil, errors.New("invalid TopK value")
		}
		present := value[offset] != 0
		fp := binary.LittleEndian.Uint32(value[offset+1 : offset+5])
		count := binary.LittleEndian.Uint32(value[offset+5 : offset+9])
		itemLen := binary.LittleEndian.Uint32(value[offset+9 : offset+13])
		offset += 13
		if uint64(itemLen) > uint64(len(value)-offset) {
			return nil, errors.New("invalid TopK value")
		}
		item := append([]byte(nil), value[offset:offset+int(itemLen)]...)
		offset += int(itemLen)
		if !present && (fp != 0 || count != 0 || itemLen != 0) {
			return nil, errors.New("invalid TopK value")
		}
		heap[i] = topKHeapBucket{fp: fp, count: count, item: item, present: present}
	}
	if offset != len(value) {
		return nil, errors.New("invalid TopK value")
	}
	return &topKSketch{k: k, width: width, depth: depth, decay: decay, data: data, heap: heap}, nil
}

func topKHeapifyDown(array []topKHeapBucket, start int) {
	length := len(array)
	if length < 2 || (length-2)/2 < start {
		return
	}
	child := 2*start + 1
	if child+1 < length && array[child].count > array[child+1].count {
		child++
	}
	if array[child].count > array[start].count {
		return
	}
	top := array[start]
	for {
		array[start] = array[child]
		start = child
		if (length-2)/2 < child {
			break
		}
		child = 2*child + 1
		if child+1 < length && array[child].count > array[child+1].count {
			child++
		}
		if array[child].count >= top.count {
			break
		}
	}
	array[start] = top
}

func topKFindHeap(topk *topKSketch, item []byte, fp uint32) int {
	for i := len(topk.heap) - 1; i >= 0; i-- {
		bucket := &topk.heap[i]
		if bucket.present && bucket.fp == fp && bytes.Equal(bucket.item, item) {
			return i
		}
	}
	return -1
}

func topKDecayChance(decay float64, count uint32) float64 {
	return math.Pow(decay, float64(count))
}

func topKAdd(topk *topKSketch, item []byte, increment uint32) TopKAddResult {
	fp := murmurHash2CMS(item, topKHashSeed)
	heapMin := topk.heap[0].count
	var maxCount uint32

	for row := uint32(0); row < topk.depth; row++ {
		loc := murmurHash2CMS(item, row)%topk.width + row*topk.width
		bucket := &topk.data[loc]
		switch {
		case bucket.count == 0:
			bucket.fp = fp
			bucket.count = increment
			if bucket.count > maxCount {
				maxCount = bucket.count
			}
		case bucket.fp == fp:
			bucket.count += increment
			if bucket.count > maxCount {
				maxCount = bucket.count
			}
		default:
			local := increment
			for local > 0 {
				if rand.Float64() < topKDecayChance(topk.decay, bucket.count) {
					bucket.count--
					if bucket.count == 0 {
						bucket.fp = fp
						bucket.count = local
						if bucket.count > maxCount {
							maxCount = bucket.count
						}
						break
					}
				}
				local--
			}
		}
	}

	if maxCount >= heapMin {
		if existing := topKFindHeap(topk, item, fp); existing >= 0 {
			topk.heap[existing].count = maxCount
			topKHeapifyDown(topk.heap, existing)
			return TopKAddResult{}
		}
		result := TopKAddResult{}
		if topk.heap[0].present {
			result.Expelled = append([]byte(nil), topk.heap[0].item...)
			result.HasExpelled = true
		}
		topk.heap[0] = topKHeapBucket{
			fp: fp, count: maxCount, item: append([]byte(nil), item...), present: true,
		}
		topKHeapifyDown(topk.heap, 0)
		return result
	}
	return TopKAddResult{}
}

func topKQuery(topk *topKSketch, item []byte) bool {
	fp := murmurHash2CMS(item, topKHashSeed)
	return topKFindHeap(topk, item, fp) >= 0
}

func topKCount(topk *topKSketch, item []byte) uint32 {
	fp := murmurHash2CMS(item, topKHashSeed)
	heapMin := topk.heap[0].count
	inHeap := topKFindHeap(topk, item, fp) >= 0
	var result uint32
	for row := uint32(0); row < topk.depth; row++ {
		loc := murmurHash2CMS(item, row)%topk.width + row*topk.width
		bucket := topk.data[loc]
		if bucket.fp == fp && (!inHeap || bucket.count >= heapMin) && bucket.count > result {
			result = bucket.count
		}
	}
	return result
}

func topKList(topk *topKSketch) []TopKListEntry {
	entries := make([]TopKListEntry, 0, len(topk.heap))
	for _, bucket := range topk.heap {
		if bucket.present && bucket.count != 0 {
			entries = append(entries, TopKListEntry{
				Item: append([]byte(nil), bucket.item...), Count: bucket.count,
			})
		}
	}
	// RedisBloom's C qsort comparator only compares count, leaving equal-count
	// order implementation-dependent. Fingerprint-descending reproduces the
	// audited build's observed tie order while keeping SnugKV deterministic.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return murmurHash2CMS(entries[i].Item, topKHashSeed) >
			murmurHash2CMS(entries[j].Item, topKHashSeed)
	})
	return entries
}

func (s *Store) TopKReserve(key string, k uint32, custom bool, width, depth uint32, decay float64) error {
	if !custom {
		width, depth, decay = topKDefaultWidth, topKDefaultDepth, topKDefaultDecay
	}
	topk, err := newTopK(k, width, depth, decay)
	if err != nil {
		return err
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if e, ok := sh.get(key); ok && !sh.expired(key, e, s.now()) {
		return errors.New("TopK: key already exists")
	}
	return s.publish(sh, key, topKPreparedEntry(encodeTopK(topk)))
}

func (s *Store) TopKAdd(key string, items [][]byte, increments []uint32) ([]TopKAddResult, error) {
	if len(items) != len(increments) {
		return nil, errors.New("TopK: invalid increment")
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, errors.New("TopK: key does not exist")
	}
	if e.valueType != TypeTopK {
		return nil, topKWrongType()
	}
	topk, err := decodeTopK(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	exp := sh.expirationAt(key, e)
	results := make([]TopKAddResult, len(items))
	for i := range items {
		results[i] = topKAdd(topk, items[i], increments[i])
	}
	prepared := topKPreparedEntry(encodeTopK(topk))
	prepared.expiresAt = exp
	if err := s.publish(sh, key, prepared); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) TopKQuery(key string, items [][]byte) ([]bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, errors.New("TopK: key does not exist")
	}
	if e.valueType != TypeTopK {
		return nil, topKWrongType()
	}
	topk, err := decodeTopK(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	results := make([]bool, len(items))
	for i := range items {
		results[i] = topKQuery(topk, items[i])
	}
	return results, nil
}

func (s *Store) TopKCount(key string, items [][]byte) ([]uint32, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, errors.New("TopK: key does not exist")
	}
	if e.valueType != TypeTopK {
		return nil, topKWrongType()
	}
	topk, err := decodeTopK(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	results := make([]uint32, len(items))
	for i := range items {
		results[i] = topKCount(topk, items[i])
	}
	return results, nil
}

func (s *Store) TopKList(key string) ([]TopKListEntry, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, errors.New("TopK: key does not exist")
	}
	if e.valueType != TypeTopK {
		return nil, topKWrongType()
	}
	topk, err := decodeTopK(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	return topKList(topk), nil
}

func (s *Store) TopKInfo(key string) (TopKInfo, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return TopKInfo{}, errors.New("TopK: key does not exist")
	}
	if e.valueType != TypeTopK {
		return TopKInfo{}, topKWrongType()
	}
	topk, err := decodeTopK(s.decode(sh, e))
	if err != nil {
		return TopKInfo{}, err
	}
	return TopKInfo{K: topk.k, Width: topk.width, Depth: topk.depth, Decay: topk.decay}, nil
}
