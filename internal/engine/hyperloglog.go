package engine

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
)

const (
	hllPrecision             = 14
	hllRegisters             = 1 << hllPrecision
	hllIndexMask             = hllRegisters - 1
	hllQ                     = 64 - hllPrecision
	hllBits                  = 6
	hllRegisterMax           = (1 << hllBits) - 1
	hllHeaderSize            = 16
	hllDenseBytes            = (hllRegisters*hllBits + 7) / 8
	hllDenseSize             = hllHeaderSize + hllDenseBytes
	hllDenseEncoding         = byte(0)
	hllSparseEncoding        = byte(1)
	hllSparseMaxBytes        = 3000 // Redis default hll-sparse-max-bytes.
	hllSparseValueMax        = 32
	hllSparseValueRun        = 4
	hllSparseZeroRun         = 64
	hllSparseXZeroRun        = 16384
	hllAlphaInfinity         = 0.721347520444481703680
	hllHashSeed       uint64 = 0xadc83b19
)

var (
	errHLLWrongType = errors.New("WRONGTYPE Key is not a valid HyperLogLog string value.")
	errHLLCorrupt   = errors.New("INVALIDOBJ Corrupted HLL object detected")
	errHLLContainer = errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
)

func newEmptyHLL() []byte {
	value := make([]byte, hllHeaderSize+2)
	copy(value[:4], "HYLL")
	value[4] = hllSparseEncoding
	// One XZERO opcode covering all 16384 registers. XZERO stores len-1.
	value[16] = 0x7f
	value[17] = 0xff
	return value
}

func hllInvalidateCache(value []byte) {
	if len(value) >= hllHeaderSize {
		value[15] |= 0x80
	}
}

func hllCachedCardinality(value []byte) (uint64, bool) {
	if len(value) < hllHeaderSize || value[15]&0x80 != 0 {
		return 0, false
	}
	return binary.LittleEndian.Uint64(value[8:16]), true
}

func hllSetCachedCardinality(value []byte, cardinality uint64) {
	binary.LittleEndian.PutUint64(value[8:16], cardinality)
	value[15] &^= 0x80
}

func validateHLLHeader(value []byte) (byte, error) {
	if len(value) < hllHeaderSize || string(value[:4]) != "HYLL" {
		return 0, errHLLWrongType
	}
	encoding := value[4]
	if encoding != hllDenseEncoding && encoding != hllSparseEncoding {
		return 0, errHLLWrongType
	}
	if encoding == hllDenseEncoding && len(value) != hllDenseSize {
		return 0, errHLLWrongType
	}
	return encoding, nil
}

func hllDenseGet(registers []byte, register int) byte {
	bit := register * hllBits
	byteIndex := bit / 8
	firstBit := uint(bit & 7)
	v := uint16(registers[byteIndex]) >> firstBit
	if byteIndex+1 < len(registers) {
		v |= uint16(registers[byteIndex+1]) << (8 - firstBit)
	}
	return byte(v & hllRegisterMax)
}

func hllDenseSet(registers []byte, register int, value byte) {
	bit := register * hllBits
	byteIndex := bit / 8
	firstBit := uint(bit & 7)
	firstBits := uint16(hllRegisterMax) << firstBit
	registers[byteIndex] &^= byte(firstBits)
	registers[byteIndex] |= byte(uint16(value) << firstBit)

	if byteIndex+1 < len(registers) {
		remaining := uint(8) - firstBit
		secondMask := uint16(hllRegisterMax) >> remaining
		registers[byteIndex+1] &^= byte(secondMask)
		registers[byteIndex+1] |= byte(uint16(value) >> remaining)
	}
}

func decodeSparseHLL(body []byte, raw []byte) error {
	index := 0
	for p := 0; p < len(body); {
		b := body[p]
		var run int
		var value byte

		switch b & 0xc0 {
		case 0x00: // ZERO: 00xxxxxx
			run = int(b&0x3f) + 1
			p++
		case 0x40: // XZERO: 01xxxxxx yyyyyyyy
			if p+1 >= len(body) {
				return errHLLCorrupt
			}
			run = (int(b&0x3f)<<8 | int(body[p+1])) + 1
			p += 2
		default: // VAL: 1vvvvvxx
			value = ((b >> 2) & 0x1f) + 1
			run = int(b&0x03) + 1
			p++
		}

		if run <= 0 || index+run > hllRegisters {
			return errHLLCorrupt
		}
		if value != 0 {
			for i := 0; i < run; i++ {
				raw[index+i] = value
			}
		}
		index += run
	}
	if index != hllRegisters {
		return errHLLCorrupt
	}
	return nil
}

func decodeHLL(value []byte) ([]byte, byte, error) {
	encoding, err := validateHLLHeader(value)
	if err != nil {
		return nil, 0, err
	}
	raw := make([]byte, hllRegisters)
	if encoding == hllDenseEncoding {
		registers := value[hllHeaderSize:]
		for i := 0; i < hllRegisters; i++ {
			raw[i] = hllDenseGet(registers, i)
		}
		return raw, encoding, nil
	}
	if err := decodeSparseHLL(value[hllHeaderSize:], raw); err != nil {
		return nil, 0, err
	}
	return raw, encoding, nil
}

func encodeSparseHLL(raw []byte) ([]byte, bool) {
	body := make([]byte, 0, 256)
	for i := 0; i < hllRegisters; {
		value := raw[i]
		if value == 0 {
			j := i + 1
			for j < hllRegisters && raw[j] == 0 {
				j++
			}
			run := j - i
			for run > 0 {
				if run > hllSparseZeroRun {
					n := run
					if n > hllSparseXZeroRun {
						n = hllSparseXZeroRun
					}
					x := n - 1
					body = append(body, byte(x>>8)|0x40, byte(x))
					run -= n
				} else {
					n := run
					if n > hllSparseZeroRun {
						n = hllSparseZeroRun
					}
					body = append(body, byte(n-1))
					run -= n
				}
			}
			i = j
			continue
		}

		if value > hllSparseValueMax {
			return nil, false
		}
		j := i + 1
		for j < hllRegisters && raw[j] == value && j-i < hllSparseValueRun {
			j++
		}
		run := j - i
		body = append(body, 0x80|((value-1)<<2)|byte(run-1))
		i = j
	}
	if hllHeaderSize+len(body) > hllSparseMaxBytes {
		return nil, false
	}
	return body, true
}

func hllHeaderFrom(base []byte, encoding byte) []byte {
	header := make([]byte, hllHeaderSize)
	copy(header[:4], "HYLL")
	header[4] = encoding
	if len(base) >= hllHeaderSize {
		copy(header[8:16], base[8:16])
	}
	hllInvalidateCache(header)
	return header
}

func encodeHLL(raw []byte, base []byte, forceDense bool) []byte {
	if !forceDense {
		if body, ok := encodeSparseHLL(raw); ok {
			out := hllHeaderFrom(base, hllSparseEncoding)
			out = append(out, body...)
			return out
		}
	}
	out := make([]byte, hllDenseSize)
	copy(out, hllHeaderFrom(base, hllDenseEncoding))
	registers := out[hllHeaderSize:]
	for i, value := range raw {
		hllDenseSet(registers, i, value)
	}
	return out
}

func murmurHash64A(key []byte) uint64 {
	const multiplier uint64 = 0xc6a4a7935bd1e995
	const shift = 47
	h := hllHashSeed ^ (uint64(len(key)) * multiplier)

	i := 0
	for ; i+8 <= len(key); i += 8 {
		k := binary.LittleEndian.Uint64(key[i : i+8])
		k *= multiplier
		k ^= k >> shift
		k *= multiplier
		h ^= k
		h *= multiplier
	}

	tail := key[i:]
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
		h *= multiplier
	}

	h ^= h >> shift
	h *= multiplier
	h ^= h >> shift
	return h
}

func hllAddRaw(raw []byte, element []byte) bool {
	hash := murmurHash64A(element)
	index := int(hash & hllIndexMask)
	hash >>= hllPrecision
	hash |= uint64(1) << hllQ
	count := byte(bits.TrailingZeros64(hash) + 1)
	if count > raw[index] {
		raw[index] = count
		return true
	}
	return false
}

func hllSigma(x float64) float64 {
	if x == 1 {
		return math.Inf(1)
	}
	y := 1.0
	z := x
	for {
		x *= x
		previous := z
		z += x * y
		y += y
		if previous == z {
			return z
		}
	}
}

func hllTau(x float64) float64 {
	if x == 0 || x == 1 {
		return 0
	}
	y := 1.0
	z := 1 - x
	for {
		x = math.Sqrt(x)
		previous := z
		y *= 0.5
		d := 1 - x
		z -= d * d * y
		if previous == z {
			return z / 3
		}
	}
}

func hllCardinality(raw []byte) uint64 {
	var histogram [64]int
	for _, value := range raw {
		histogram[value]++
	}
	m := float64(hllRegisters)
	z := m * hllTau((m-float64(histogram[hllQ+1]))/m)
	for j := hllQ; j >= 1; j-- {
		z += float64(histogram[j])
		z *= 0.5
	}
	z += m * hllSigma(float64(histogram[0])/m)
	estimate := math.Round(hllAlphaInfinity * m * m / z)
	if estimate <= 0 || math.IsNaN(estimate) {
		return 0
	}
	if estimate >= float64(^uint64(0)) {
		return ^uint64(0)
	}
	return uint64(estimate)
}

func mergeHLLRaw(target, source []byte) {
	for i, value := range source {
		if value > target[i] {
			target[i] = value
		}
	}
}

func hllEntryValue(s *Store, sh *shard, key string, e entry) ([]byte, byte, error) {
	if isNativeContainerType(e.valueType) {
		return nil, 0, errHLLContainer
	}
	value := s.decode(sh, e)
	encoding, err := validateHLLHeader(value)
	if err != nil {
		return nil, 0, err
	}
	return value, encoding, nil
}

// HLLAdd implements PFADD semantics. HyperLogLogs remain Redis-visible string
// values: the bytes are a Redis-compatible HYLL sparse/dense representation.
func (s *Store) HLLAdd(key string, elements [][]byte) (bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, exists := sh.get(key)
	if exists && sh.expired(key, e, now) {
		s.remove(sh, key)
		exists = false
		e = entry{}
	}

	if !exists {
		value := newEmptyHLL()
		raw := make([]byte, hllRegisters)
		for _, element := range elements {
			hllAddRaw(raw, element)
		}
		if len(elements) > 0 {
			value = encodeHLL(raw, value, false)
		} else {
			hllInvalidateCache(value)
		}
		prepared := s.makeEntryForShard(sh, value)
		if err := s.publish(sh, key, prepared); err != nil {
			return false, err
		}
		return true, nil
	}

	value, encoding, err := hllEntryValue(s, sh, key, e)
	if err != nil {
		return false, err
	}
	// Redis validates the HLL header but performs no structural sparse scan
	// when PFADD is called with no elements.
	if len(elements) == 0 {
		return false, nil
	}

	raw, _, err := decodeHLL(value)
	if err != nil {
		return false, err
	}
	changed := false
	for _, element := range elements {
		if hllAddRaw(raw, element) {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}

	updatedValue := encodeHLL(raw, value, encoding == hllDenseEncoding)
	prepared := s.makeEntryForShard(sh, updatedValue)
	prepared.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, prepared); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) hllCountSingle(key string) (uint64, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, exists := sh.get(key)
	if !exists || sh.expired(key, e, s.now()) {
		return 0, nil
	}
	value, _, err := hllEntryValue(s, sh, key, e)
	if err != nil {
		return 0, err
	}
	if cached, valid := hllCachedCardinality(value); valid {
		return cached, nil
	}
	raw, _, err := decodeHLL(value)
	if err != nil {
		return 0, err
	}
	cardinality := hllCardinality(raw)

	// Redis caches PFCOUNT's single-key result inside the HYLL string. The cache
	// is non-semantic, so if max-memory prevents this same-size refresh we can
	// still return the correct cardinality and leave the cache invalid.
	updatedValue := append([]byte(nil), value...)
	hllSetCachedCardinality(updatedValue, cardinality)
	prepared := s.makeEntryForShard(sh, updatedValue)
	prepared.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, prepared); err != nil && !errors.Is(err, ErrOOM) {
		return 0, err
	}
	return cardinality, nil
}

// HLLCount implements PFCOUNT for one or more keys. Multi-key counting merges
// registers into a temporary raw sketch and does not mutate source keys.
func (s *Store) HLLCount(keys []string) (uint64, error) {
	if len(keys) == 1 {
		return s.hllCountSingle(keys[0])
	}
	unlock := s.lockAll()
	defer unlock()

	target := make([]byte, hllRegisters)
	now := s.now()
	for _, key := range keys {
		sh := s.shardFor(key)
		e, exists := sh.get(key)
		if !exists || sh.expired(key, e, now) {
			continue
		}
		value, _, err := hllEntryValue(s, sh, key, e)
		if err != nil {
			return 0, err
		}
		raw, _, err := decodeHLL(value)
		if err != nil {
			return 0, err
		}
		mergeHLLRaw(target, raw)
	}
	return hllCardinality(target), nil
}

// HLLMerge implements PFMERGE. The existing destination participates in the
// union, its TTL is preserved, and a missing destination is created persistent.
func (s *Store) HLLMerge(destination string, sources []string) error {
	unlock := s.lockAll()
	defer unlock()

	target := make([]byte, hllRegisters)
	keys := make([]string, 0, len(sources)+1)
	keys = append(keys, destination)
	keys = append(keys, sources...)
	now := s.now()
	forceDense := false
	var destinationEntry entry
	var destinationValue []byte
	destinationExists := false

	for _, key := range keys {
		sh := s.shardFor(key)
		e, exists := sh.get(key)
		if !exists || sh.expired(key, e, now) {
			continue
		}
		value, encoding, err := hllEntryValue(s, sh, key, e)
		if err != nil {
			return err
		}
		raw, _, err := decodeHLL(value)
		if err != nil {
			return err
		}
		if encoding == hllDenseEncoding {
			forceDense = true
		}
		mergeHLLRaw(target, raw)
		if key == destination && !destinationExists {
			destinationExists = true
			destinationEntry = e
			destinationValue = append([]byte(nil), value...)
		}
	}

	if destinationValue == nil {
		destinationValue = newEmptyHLL()
	}
	updatedValue := encodeHLL(target, destinationValue, forceDense)
	destinationShard := s.shardFor(destination)
	prepared := s.makeEntryForShard(destinationShard, updatedValue)
	if destinationExists {
		prepared.expiresAt = destinationShard.expirationAt(destination, destinationEntry)
	}
	return s.publish(destinationShard, destination, prepared)
}
