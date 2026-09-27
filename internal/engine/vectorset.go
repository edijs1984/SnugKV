package engine

import (
	"bytes"
	"encoding/json"
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

const vectorSetHeaderSize = 12

var vectorSetMagic = [4]byte{'S', 'V', 'S', 1}

type vectorSetElement struct {
	vector []float32
	attrs  []byte
}

type vectorSet struct {
	dim     int
	members map[string]vectorSetElement
}

func vectorSetWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func vectorSetPreparedEntry(value []byte) preparedEntry {
	return preparedEntry{
		entry: entry{entryData: entryData{
			codecID: 0, valueType: TypeVectorSet, rawLength: uint32(len(value)),
		}},
		data: append([]byte(nil), value...),
	}
}

func encodeVectorSet(vs *vectorSet) []byte {
	names := make([]string, 0, len(vs.members))
	size := vectorSetHeaderSize
	for name, member := range vs.members {
		names = append(names, name)
		size += 8 + len(name) + 4*len(member.vector) + len(member.attrs)
	}
	sort.Strings(names)

	out := make([]byte, size)
	copy(out[:4], vectorSetMagic[:])
	binary.LittleEndian.PutUint32(out[4:8], uint32(vs.dim))
	binary.LittleEndian.PutUint32(out[8:12], uint32(len(names)))

	off := vectorSetHeaderSize
	for _, name := range names {
		member := vs.members[name]
		binary.LittleEndian.PutUint32(out[off:off+4], uint32(len(name)))
		binary.LittleEndian.PutUint32(out[off+4:off+8], uint32(len(member.attrs)))
		off += 8
		copy(out[off:], name)
		off += len(name)
		for _, value := range member.vector {
			binary.LittleEndian.PutUint32(out[off:off+4], math.Float32bits(value))
			off += 4
		}
		copy(out[off:], member.attrs)
		off += len(member.attrs)
	}
	return out
}

func decodeVectorSet(value []byte) (*vectorSet, error) {
	if len(value) < vectorSetHeaderSize || !bytes.Equal(value[:4], vectorSetMagic[:]) {
		return nil, errors.New("invalid vector set")
	}
	dim := int(binary.LittleEndian.Uint32(value[4:8]))
	count := int(binary.LittleEndian.Uint32(value[8:12]))
	if dim <= 0 {
		return nil, errors.New("invalid vector set")
	}

	vs := &vectorSet{
		dim:     dim,
		members: make(map[string]vectorSetElement, count),
	}
	off := vectorSetHeaderSize
	for i := 0; i < count; i++ {
		if off+8 > len(value) {
			return nil, errors.New("invalid vector set")
		}
		nameLen := int(binary.LittleEndian.Uint32(value[off : off+4]))
		attrLen := int(binary.LittleEndian.Uint32(value[off+4 : off+8]))
		off += 8
		vectorBytes := dim * 4
		if nameLen < 0 || attrLen < 0 || off+nameLen+vectorBytes+attrLen > len(value) {
			return nil, errors.New("invalid vector set")
		}
		name := string(value[off : off+nameLen])
		off += nameLen
		vector := make([]float32, dim)
		for j := range vector {
			vector[j] = math.Float32frombits(binary.LittleEndian.Uint32(value[off : off+4]))
			off += 4
		}
		attrs := append([]byte(nil), value[off:off+attrLen]...)
		off += attrLen
		vs.members[name] = vectorSetElement{vector: vector, attrs: attrs}
	}
	if off != len(value) {
		return nil, errors.New("invalid vector set")
	}
	return vs, nil
}

func vectorToFloat32(vector []float64) ([]float32, error) {
	if len(vector) == 0 {
		return nil, errors.New("ERR vector dimension must be greater than 0")
	}
	out := make([]float32, len(vector))
	for i, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("ERR invalid vector value")
		}
		out[i] = float32(value)
	}
	return out, nil
}

func (s *Store) VectorSetAdd(key, element string, vector []float64, attrs []byte) (bool, error) {
	converted, err := vectorToFloat32(vector)
	if err != nil {
		return false, err
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		vs := &vectorSet{
			dim:     len(converted),
			members: map[string]vectorSetElement{},
		}
		vs.members[element] = vectorSetElement{
			vector: converted,
			attrs:  append([]byte(nil), attrs...),
		}
		return true, s.publish(sh, key, vectorSetPreparedEntry(encodeVectorSet(vs)))
	}
	if e.valueType != TypeVectorSet {
		return false, vectorSetWrongType()
	}

	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return false, err
	}
	if len(converted) != vs.dim {
		return false, errors.New("ERR vector dimension mismatch")
	}

	old, existed := vs.members[element]
	if attrs == nil && existed {
		attrs = old.attrs
	}
	vs.members[element] = vectorSetElement{
		vector: converted,
		attrs:  append([]byte(nil), attrs...),
	}

	p := vectorSetPreparedEntry(encodeVectorSet(vs))
	p.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, p); err != nil {
		return false, err
	}
	return !existed, nil
}

func (s *Store) VectorSetCard(key string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, nil
	}
	if e.valueType != TypeVectorSet {
		return 0, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return 0, err
	}
	return int64(len(vs.members)), nil
}

func (s *Store) VectorSetDim(key string) (int64, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, errors.New("ERR vector set not found")
	}
	if e.valueType != TypeVectorSet {
		return 0, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return 0, err
	}
	return int64(vs.dim), nil
}

func (s *Store) VectorSetEmb(key, element string) ([]float32, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, false, nil
	}
	if e.valueType != TypeVectorSet {
		return nil, false, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return nil, false, err
	}
	member, found := vs.members[element]
	if !found {
		return nil, false, nil
	}
	return append([]float32(nil), member.vector...), true, nil
}

func (s *Store) VectorSetIsMember(key, element string) (bool, error) {
	_, found, err := s.VectorSetEmb(key, element)
	return found, err
}

func (s *Store) VectorSetRemove(key, element string) (bool, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return false, nil
	}
	if e.valueType != TypeVectorSet {
		return false, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return false, err
	}
	if _, found := vs.members[element]; !found {
		return false, nil
	}
	delete(vs.members, element)
	if len(vs.members) == 0 {
		s.remove(sh, key)
		return true, nil
	}

	p := vectorSetPreparedEntry(encodeVectorSet(vs))
	p.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, p); err != nil {
		return false, err
	}
	return true, nil
}


func (s *Store) VectorSetGetAttr(key, element string) ([]byte, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, false, nil
	}
	if e.valueType != TypeVectorSet {
		return nil, false, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return nil, false, err
	}
	member, found := vs.members[element]
	if !found || len(member.attrs) == 0 {
		return nil, false, nil
	}
	return append([]byte(nil), member.attrs...), true, nil
}

func (s *Store) VectorSetSetAttr(key, element string, attrs []byte) (bool, error) {
	if len(attrs) > 0 {
		var value any
		if err := json.Unmarshal(attrs, &value); err != nil {
			return false, errors.New("ERR invalid JSON attributes")
		}
		if _, ok := value.(map[string]any); !ok {
			return false, errors.New("ERR attributes must be a JSON object")
		}
	}

	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return false, nil
	}
	if e.valueType != TypeVectorSet {
		return false, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return false, err
	}
	member, found := vs.members[element]
	if !found {
		return false, nil
	}
	member.attrs = append([]byte(nil), attrs...)
	vs.members[element] = member

	p := vectorSetPreparedEntry(encodeVectorSet(vs))
	p.expiresAt = sh.expirationAt(key, e)
	if err := s.publish(sh, key, p); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) VectorSetMembers(key string) ([]string, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, false, nil
	}
	if e.valueType != TypeVectorSet {
		return nil, false, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return nil, false, err
	}
	names := make([]string, 0, len(vs.members))
	for name := range vs.members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, true, nil
}

type VectorSetInfo struct {
	Dim  int64
	Size int64
}

func (s *Store) VectorSetInfo(key string) (VectorSetInfo, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return VectorSetInfo{}, false, nil
	}
	if e.valueType != TypeVectorSet {
		return VectorSetInfo{}, false, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return VectorSetInfo{}, false, err
	}
	return VectorSetInfo{Dim:int64(vs.dim), Size:int64(len(vs.members))}, true, nil
}


type VectorSetSearchItem struct {
	Name  string
	Score float64
	Attrs []byte
}

func cosineSimilarity32(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, an, bn float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		an += av * av
		bn += bv * bv
	}
	if an == 0 || bn == 0 {
		return 0
	}
	score := dot / (math.Sqrt(an) * math.Sqrt(bn))
	if score > 1 {
		score = 1
	} else if score < -1 {
		score = -1
	}
	return score
}

func (s *Store) VectorSetSearch(
	key string,
	query []float64,
	filter func([]byte) bool,
) ([]VectorSetSearchItem, bool, error) {
	converted, err := vectorToFloat32(query)
	if err != nil {
		return nil, false, err
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, false, nil
	}
	if e.valueType != TypeVectorSet {
		return nil, false, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return nil, false, err
	}
	if len(converted) != vs.dim {
		return nil, false, errors.New("ERR vector dimension mismatch")
	}

	out := make([]VectorSetSearchItem, 0, len(vs.members))
	for name, member := range vs.members {
		if filter != nil && !filter(member.attrs) {
			continue
		}
		out = append(out, VectorSetSearchItem{
			Name:  name,
			Score: cosineSimilarity32(converted, member.vector),
			Attrs: append([]byte(nil), member.attrs...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Name < out[j].Name
		}
		return out[i].Score > out[j].Score
	})
	return out, true, nil
}

func (s *Store) VectorSetLinks(
	key, element string,
	limit int,
) ([]VectorSetSearchItem, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, false, nil
	}
	if e.valueType != TypeVectorSet {
		return nil, false, vectorSetWrongType()
	}
	vs, err := decodeVectorSet(s.decode(sh, e))
	if err != nil {
		return nil, false, err
	}
	source, found := vs.members[element]
	if !found {
		return nil, false, nil
	}

	out := make([]VectorSetSearchItem, 0, len(vs.members)-1)
	for name, member := range vs.members {
		if name == element {
			continue
		}
		out = append(out, VectorSetSearchItem{
			Name:  name,
			Score: cosineSimilarity32(source.vector, member.vector),
			Attrs: append([]byte(nil), member.attrs...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Name < out[j].Name
		}
		return out[i].Score > out[j].Score
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, true, nil
}
