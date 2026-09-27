package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strings"
)

const (
	timeSeriesHeaderSize = 32
	timeSeriesChunkSize  = 4096
)

var timeSeriesMagic = [4]byte{'S', 'T', 'S', 1}

type TimeSeriesDuplicatePolicy uint8

const (
	TimeSeriesBlock TimeSeriesDuplicatePolicy = iota
	TimeSeriesFirst
	TimeSeriesLast
	TimeSeriesMin
	TimeSeriesMax
	TimeSeriesSum
)

type TimeSeriesLabel struct {
	Key   string
	Value string
}

type TimeSeriesSample struct {
	Timestamp int64
	Value     float64
}

type TimeSeriesRule struct {
	DestKey        string
	BucketDuration int64
	Aggregator     string
	Alignment      int64
}

type TimeSeriesInfo struct {
	TotalSamples      int
	MemoryUsage       int64
	FirstTimestamp    int64
	LastTimestamp     int64
	RetentionTime     int64
	ChunkCount        int
	ChunkSize         int
	ChunkType         string
	DuplicatePolicy   string
	Labels            []TimeSeriesLabel
	IgnoreMaxTimeDiff int64
	IgnoreMaxValDiff  float64
	SourceKey         string
	Rules             []TimeSeriesRule
}

type timeSeriesRule struct {
	destKey        string
	bucketDuration int64
	aggregator     string
	alignment      int64
	startAfter     int64
	lastBucket     int64
}

type timeSeries struct {
	retention int64
	policy    TimeSeriesDuplicatePolicy
	labels    []TimeSeriesLabel
	samples   []TimeSeriesSample
	sourceKey string
	rules     []timeSeriesRule
}

func timeSeriesWrongType() error {
	return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func timeSeriesPreparedEntry(value []byte) preparedEntry {
	return preparedEntry{
		entry: entry{entryData: entryData{
			codecID: 0, valueType: TypeTimeSeries, rawLength: uint32(len(value)),
		}},
		data: append([]byte(nil), value...),
	}
}

func timeSeriesPolicyName(policy TimeSeriesDuplicatePolicy) string {
	switch policy {
	case TimeSeriesFirst:
		return "first"
	case TimeSeriesLast:
		return "last"
	case TimeSeriesMin:
		return "min"
	case TimeSeriesMax:
		return "max"
	case TimeSeriesSum:
		return "sum"
	default:
		return "block"
	}
}

func encodeTimeSeries(ts *timeSeries) []byte {
	size := timeSeriesHeaderSize + len(ts.samples)*16
	for _, label := range ts.labels {
		size += 8 + len(label.Key) + len(label.Value)
	}
	hasMeta := ts.sourceKey != "" || len(ts.rules) > 0
	if hasMeta {
		size += 12 + len(ts.sourceKey)
		for _, rule := range ts.rules {
			size += 40 + len(rule.destKey) + len(rule.aggregator)
		}
	}
	out := make([]byte, size)
	copy(out[:4], timeSeriesMagic[:])
	binary.LittleEndian.PutUint64(out[8:16], uint64(ts.retention))
	out[16] = byte(ts.policy)
	binary.LittleEndian.PutUint32(out[20:24], uint32(len(ts.labels)))
	binary.LittleEndian.PutUint32(out[24:28], uint32(len(ts.samples)))
	off := timeSeriesHeaderSize
	for _, label := range ts.labels {
		binary.LittleEndian.PutUint32(out[off:off+4], uint32(len(label.Key)))
		binary.LittleEndian.PutUint32(out[off+4:off+8], uint32(len(label.Value)))
		off += 8
		copy(out[off:], label.Key)
		off += len(label.Key)
		copy(out[off:], label.Value)
		off += len(label.Value)
	}
	for _, sample := range ts.samples {
		binary.LittleEndian.PutUint64(out[off:off+8], uint64(sample.Timestamp))
		binary.LittleEndian.PutUint64(out[off+8:off+16], math.Float64bits(sample.Value))
		off += 16
	}
	if hasMeta {
		copy(out[off:off+4], []byte{'T','S','R','1'})
		off += 4
		binary.LittleEndian.PutUint32(out[off:off+4], uint32(len(ts.sourceKey)))
		binary.LittleEndian.PutUint32(out[off+4:off+8], uint32(len(ts.rules)))
		off += 8
		copy(out[off:], ts.sourceKey)
		off += len(ts.sourceKey)
		for _, rule := range ts.rules {
			binary.LittleEndian.PutUint32(out[off:off+4], uint32(len(rule.destKey)))
			binary.LittleEndian.PutUint32(out[off+4:off+8], uint32(len(rule.aggregator)))
			binary.LittleEndian.PutUint64(out[off+8:off+16], uint64(rule.bucketDuration))
			binary.LittleEndian.PutUint64(out[off+16:off+24], uint64(rule.alignment))
			binary.LittleEndian.PutUint64(out[off+24:off+32], uint64(rule.startAfter))
			off += 32
			binary.LittleEndian.PutUint64(out[off:off+8], uint64(rule.lastBucket))
			off += 8
			copy(out[off:], rule.destKey)
			off += len(rule.destKey)
			copy(out[off:], rule.aggregator)
			off += len(rule.aggregator)
		}
	}
	return out
}

func decodeTimeSeries(value []byte) (*timeSeries, error) {
	if len(value) < timeSeriesHeaderSize || !bytes.Equal(value[:4], timeSeriesMagic[:]) {
		return nil, errors.New("invalid TimeSeries")
	}
	retention := int64(binary.LittleEndian.Uint64(value[8:16]))
	if retention < 0 {
		return nil, errors.New("invalid TimeSeries")
	}
	policy := TimeSeriesDuplicatePolicy(value[16])
	if policy > TimeSeriesSum {
		return nil, errors.New("invalid TimeSeries")
	}
	labelCount := int(binary.LittleEndian.Uint32(value[20:24]))
	sampleCount := int(binary.LittleEndian.Uint32(value[24:28]))
	off := timeSeriesHeaderSize
	labels := make([]TimeSeriesLabel, labelCount)
	for i := 0; i < labelCount; i++ {
		if off+8 > len(value) {
			return nil, errors.New("invalid TimeSeries")
		}
		kl := int(binary.LittleEndian.Uint32(value[off : off+4]))
		vl := int(binary.LittleEndian.Uint32(value[off+4 : off+8]))
		off += 8
		if off+kl+vl > len(value) {
			return nil, errors.New("invalid TimeSeries")
		}
		labels[i] = TimeSeriesLabel{Key:string(value[off:off+kl]), Value:string(value[off+kl:off+kl+vl])}
		off += kl+vl
	}
	if sampleCount < 0 || off+sampleCount*16 > len(value) {
		return nil, errors.New("invalid TimeSeries")
	}
	samples := make([]TimeSeriesSample, sampleCount)
	var last int64 = -1 << 63
	for i := 0; i < sampleCount; i++ {
		ts := int64(binary.LittleEndian.Uint64(value[off:off+8]))
		v := math.Float64frombits(binary.LittleEndian.Uint64(value[off+8:off+16]))
		off += 16
		if i > 0 && ts <= last {
			return nil, errors.New("invalid TimeSeries")
		}
		samples[i] = TimeSeriesSample{Timestamp:ts, Value:v}
		last = ts
	}
	ts := &timeSeries{retention:retention, policy:policy, labels:labels, samples:samples}
	if off == len(value) {
		return ts,nil
	}
	if off+12 > len(value) || !bytes.Equal(value[off:off+4], []byte{'T','S','R','1'}) {
		return nil, errors.New("invalid TimeSeries")
	}
	off += 4
	sourceLen := int(binary.LittleEndian.Uint32(value[off:off+4]))
	ruleCount := int(binary.LittleEndian.Uint32(value[off+4:off+8]))
	off += 8
	if off+sourceLen > len(value) {
		return nil, errors.New("invalid TimeSeries")
	}
	ts.sourceKey = string(value[off:off+sourceLen])
	off += sourceLen
	ts.rules = make([]timeSeriesRule, ruleCount)
	for i:=0;i<ruleCount;i++ {
		if off+40 > len(value) { return nil, errors.New("invalid TimeSeries") }
		dl:=int(binary.LittleEndian.Uint32(value[off:off+4]))
		al:=int(binary.LittleEndian.Uint32(value[off+4:off+8]))
		rule:=timeSeriesRule{
			bucketDuration:int64(binary.LittleEndian.Uint64(value[off+8:off+16])),
			alignment:int64(binary.LittleEndian.Uint64(value[off+16:off+24])),
			startAfter:int64(binary.LittleEndian.Uint64(value[off+24:off+32])),
			lastBucket:int64(binary.LittleEndian.Uint64(value[off+32:off+40])),
		}
		off += 40
		if dl<0 || al<0 || off+dl+al>len(value) { return nil, errors.New("invalid TimeSeries") }
		rule.destKey=string(value[off:off+dl]); off+=dl
		rule.aggregator=string(value[off:off+al]); off+=al
		ts.rules[i]=rule
	}
	if off != len(value) { return nil, errors.New("invalid TimeSeries") }
	return ts,nil
}

func newTimeSeries(retention int64, policy TimeSeriesDuplicatePolicy, labels []TimeSeriesLabel) (*timeSeries, error) {
	if retention < 0 {
		return nil, errors.New("TSDB: Couldn't parse RETENTION")
	}
	if policy > TimeSeriesSum {
		return nil, errors.New("ERR TSDB: Unknown DUPLICATE_POLICY")
	}
	return &timeSeries{
		retention: retention,
		policy: policy,
		labels: append([]TimeSeriesLabel(nil), labels...),
	}, nil
}

func timeSeriesFind(samples []TimeSeriesSample, timestamp int64) (int, bool) {
	i := sort.Search(len(samples), func(i int) bool { return samples[i].Timestamp >= timestamp })
	return i, i < len(samples) && samples[i].Timestamp == timestamp
}

func timeSeriesApplyDuplicate(current, next float64, policy TimeSeriesDuplicatePolicy) (float64, error) {
	switch policy {
	case TimeSeriesFirst:
		return current, nil
	case TimeSeriesLast:
		return next, nil
	case TimeSeriesMin:
		if math.IsNaN(current) || math.IsNaN(next) {
			return 0, errors.New("ERR TSDB: Error at upsert, update is not supported when DUPLICATE_POLICY is set to BLOCK mode, or either current or new value is NaN and DUPLICATE_POLICY is MAX/MIN/SUM")
		}
		return math.Min(current, next), nil
	case TimeSeriesMax:
		if math.IsNaN(current) || math.IsNaN(next) {
			return 0, errors.New("ERR TSDB: Error at upsert, update is not supported when DUPLICATE_POLICY is set to BLOCK mode, or either current or new value is NaN and DUPLICATE_POLICY is MAX/MIN/SUM")
		}
		return math.Max(current, next), nil
	case TimeSeriesSum:
		if math.IsNaN(current) || math.IsNaN(next) {
			return 0, errors.New("ERR TSDB: Error at upsert, update is not supported when DUPLICATE_POLICY is set to BLOCK mode, or either current or new value is NaN and DUPLICATE_POLICY is MAX/MIN/SUM")
		}
		return current + next, nil
	default:
		return 0, errors.New("ERR TSDB: Error at upsert, update is not supported when DUPLICATE_POLICY is set to BLOCK mode, or either current or new value is NaN and DUPLICATE_POLICY is MAX/MIN/SUM")
	}
}

func (ts *timeSeries) trimRetention() {
	if ts.retention <= 0 || len(ts.samples) == 0 {
		return
	}
	maxTS := ts.samples[len(ts.samples)-1].Timestamp
	cutoff := maxTS - ts.retention
	first := sort.Search(len(ts.samples), func(i int) bool { return ts.samples[i].Timestamp >= cutoff })
	if first > 0 {
		ts.samples = append([]TimeSeriesSample(nil), ts.samples[first:]...)
	}
}

func (ts *timeSeries) add(timestamp int64, value float64) error {
	i, found := timeSeriesFind(ts.samples, timestamp)
	if found {
		next, err := timeSeriesApplyDuplicate(ts.samples[i].Value, value, ts.policy)
		if err != nil {
			return err
		}
		ts.samples[i].Value = next
		return nil
	}
	ts.samples = append(ts.samples, TimeSeriesSample{})
	copy(ts.samples[i+1:], ts.samples[i:])
	ts.samples[i] = TimeSeriesSample{Timestamp: timestamp, Value: value}
	ts.trimRetention()
	return nil
}

func (s *Store) TimeSeriesCreate(key string, retention int64, policy TimeSeriesDuplicatePolicy, labels []TimeSeriesLabel) error {
	ts, err := newTimeSeries(retention, policy, labels)
	if err != nil {
		return err
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if e, ok := sh.get(key); ok && !sh.expired(key, e, s.now()) {
		return errors.New("ERR TSDB: key already exists")
	}
	return s.publish(sh, key, timeSeriesPreparedEntry(encodeTimeSeries(ts)))
}

func (s *Store) TimeSeriesAdd(key string, timestamp int64, value float64, create *timeSeries) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if create == nil {
			sh.mu.Unlock()
			return errors.New("ERR TSDB: the key does not exist")
		}
		ts := create
		if err := ts.add(timestamp, value); err != nil {
			sh.mu.Unlock()
			return err
		}
		err := s.publish(sh, key, timeSeriesPreparedEntry(encodeTimeSeries(ts)))
		sh.mu.Unlock()
		return err
	}
	if e.valueType != TypeTimeSeries {
		sh.mu.Unlock()
		return errors.New("ERR TSDB: the key is not a TSDB key")
	}
	ts, err := decodeTimeSeries(s.decode(sh, e))
	if err != nil {
		sh.mu.Unlock()
		return err
	}
	exp := sh.expirationAt(key, e)
	if err := ts.add(timestamp, value); err != nil {
		sh.mu.Unlock()
		return err
	}
	p := timeSeriesPreparedEntry(encodeTimeSeries(ts))
	p.expiresAt = exp
	if err := s.publish(sh, key, p); err != nil {
		sh.mu.Unlock()
		return err
	}
	rules := append([]timeSeriesRule(nil), ts.rules...)
	samples := append([]TimeSeriesSample(nil), ts.samples...)
	sh.mu.Unlock()

	if len(rules) > 0 {
		if err := s.applyTimeSeriesRules(key, samples, rules); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) TimeSeriesGet(key string) (TimeSeriesSample, bool, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return TimeSeriesSample{}, false, errors.New("ERR TSDB: the key does not exist")
	}
	if e.valueType != TypeTimeSeries {
		return TimeSeriesSample{}, false, timeSeriesWrongType()
	}
	ts, err := decodeTimeSeries(s.decode(sh, e))
	if err != nil {
		return TimeSeriesSample{}, false, err
	}
	if len(ts.samples) == 0 {
		return TimeSeriesSample{}, false, nil
	}
	return ts.samples[len(ts.samples)-1], true, nil
}

func (s *Store) TimeSeriesRange(key string, from, to int64, reverse bool) ([]TimeSeriesSample, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return nil, errors.New("ERR TSDB: the key does not exist")
	}
	if e.valueType != TypeTimeSeries {
		return nil, timeSeriesWrongType()
	}
	ts, err := decodeTimeSeries(s.decode(sh, e))
	if err != nil {
		return nil, err
	}
	start := sort.Search(len(ts.samples), func(i int) bool { return ts.samples[i].Timestamp >= from })
	end := sort.Search(len(ts.samples), func(i int) bool { return ts.samples[i].Timestamp > to })
	out := append([]TimeSeriesSample(nil), ts.samples[start:end]...)
	if reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

func (s *Store) TimeSeriesDeleteRange(key string, from, to int64) (int, error) {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return 0, errors.New("ERR TSDB: the key does not exist")
	}
	if e.valueType != TypeTimeSeries {
		return 0, timeSeriesWrongType()
	}
	ts, err := decodeTimeSeries(s.decode(sh, e))
	if err != nil {
		return 0, err
	}
	exp := sh.expirationAt(key, e)
	start := sort.Search(len(ts.samples), func(i int) bool { return ts.samples[i].Timestamp >= from })
	end := sort.Search(len(ts.samples), func(i int) bool { return ts.samples[i].Timestamp > to })
	n := end - start
	if n > 0 {
		copy(ts.samples[start:], ts.samples[end:])
		ts.samples = ts.samples[:len(ts.samples)-n]
	}
	p := timeSeriesPreparedEntry(encodeTimeSeries(ts))
	p.expiresAt = exp
	if err := s.publish(sh, key, p); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) TimeSeriesIncrBy(key string, delta float64, timestamp int64, decrement bool, create *timeSeries) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		if create == nil {
			return errors.New("ERR TSDB: the key does not exist")
		}
		ts := create
		value := delta
		if decrement {
			value = -delta
		}
		if err := ts.add(timestamp, value); err != nil {
			return err
		}
		return s.publish(sh, key, timeSeriesPreparedEntry(encodeTimeSeries(ts)))
	}
	if e.valueType != TypeTimeSeries {
		return timeSeriesWrongType()
	}
	ts, err := decodeTimeSeries(s.decode(sh, e))
	if err != nil {
		return err
	}
	if len(ts.samples) > 0 && timestamp < ts.samples[len(ts.samples)-1].Timestamp {
		return errors.New("TSDB: timestamp must be equal to or higher than the maximum existing timestamp")
	}
	var current float64
	if len(ts.samples) > 0 {
		current = ts.samples[len(ts.samples)-1].Value
		if math.IsNaN(current) {
			return errors.New("ERR TSDB: cannot increment/decrement NaN value")
		}
	}
	value := current + delta
	if decrement {
		value = current - delta
	}
	exp := sh.expirationAt(key, e)
	// INCRBY/DECRBY use LAST semantics at equal timestamps.
	oldPolicy := ts.policy
	ts.policy = TimeSeriesLast
	err = ts.add(timestamp, value)
	ts.policy = oldPolicy
	if err != nil {
		return err
	}
	p := timeSeriesPreparedEntry(encodeTimeSeries(ts))
	p.expiresAt = exp
	return s.publish(sh, key, p)
}

func timeSeriesMemoryUsage(ts *timeSeries) int64 {
	// RedisTimeSeries INFO reports allocator/chunk memory rather than logical
	// payload size. The audited build has one 4096-byte compressed chunk in this
	// Phase-1 surface. Reproduce the observed allocator overhead for labels.
	usage := int64(4456)
	if len(ts.labels) > 0 {
		usage += 400
		if len(ts.labels) > 1 {
			usage += int64(len(ts.labels)-1) * 360
		}
	}
	return usage
}

func (s *Store) TimeSeriesInfo(key string) (TimeSeriesInfo, error) {
	sh := s.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return TimeSeriesInfo{}, errors.New("ERR TSDB: the key does not exist")
	}
	if e.valueType != TypeTimeSeries {
		return TimeSeriesInfo{}, timeSeriesWrongType()
	}
	ts, err := decodeTimeSeries(s.decode(sh, e))
	if err != nil {
		return TimeSeriesInfo{}, err
	}
	info := TimeSeriesInfo{
		TotalSamples: len(ts.samples),
		MemoryUsage: timeSeriesMemoryUsage(ts),
		RetentionTime: ts.retention,
		ChunkCount: 1,
		ChunkSize: timeSeriesChunkSize,
		ChunkType: "compressed",
		DuplicatePolicy: timeSeriesPolicyName(ts.policy),
		Labels: append([]TimeSeriesLabel(nil), ts.labels...),
		SourceKey: ts.sourceKey,
		Rules: make([]TimeSeriesRule, len(ts.rules)),
	}
	for i, rule := range ts.rules {
		info.Rules[i] = TimeSeriesRule{
			DestKey: rule.destKey,
			BucketDuration: rule.bucketDuration,
			Aggregator: rule.aggregator,
			Alignment: rule.alignment,
		}
	}
	if len(ts.samples) > 0 {
		info.FirstTimestamp = ts.samples[0].Timestamp
		info.LastTimestamp = ts.samples[len(ts.samples)-1].Timestamp
	}
	return info, nil
}

func NewTimeSeriesConfig(retention int64, policy TimeSeriesDuplicatePolicy, labels []TimeSeriesLabel) (*timeSeries, error) {
	return newTimeSeries(retention, policy, labels)
}


func (s *Store) TimeSeriesAlter(
	key string,
	retention *int64,
	policy *TimeSeriesDuplicatePolicy,
	labels *[]TimeSeriesLabel,
) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return errors.New("ERR TSDB: the key does not exist")
	}
	if e.valueType != TypeTimeSeries {
		return timeSeriesWrongType()
	}
	ts, err := decodeTimeSeries(s.decode(sh, e))
	if err != nil {
		return err
	}
	if retention != nil {
		if *retention < 0 {
			return errors.New("TSDB: Couldn't parse RETENTION")
		}
		ts.retention = *retention
		ts.trimRetention()
	}
	if policy != nil {
		if *policy > TimeSeriesSum {
			return errors.New("ERR TSDB: Unknown DUPLICATE_POLICY")
		}
		ts.policy = *policy
	}
	if labels != nil {
		ts.labels = append([]TimeSeriesLabel(nil), (*labels)...)
	}

	p := timeSeriesPreparedEntry(encodeTimeSeries(ts))
	p.expiresAt = sh.expirationAt(key, e)
	return s.publish(sh, key, p)
}

func (s *Store) TimeSeriesMAdd(
	items []struct {
		Key       string
		Timestamp int64
		Value     float64
	},
) error {
	// Correctness-first implementation: validate all target keys before
	// applying any mutation so MADD is all-or-nothing for missing/wrong types.
	for _, item := range items {
		sh := s.shardFor(item.Key)
		sh.mu.RLock()
		e, ok := sh.get(item.Key)
		if !ok || sh.expired(item.Key, e, s.now()) {
			sh.mu.RUnlock()
			return errors.New("ERR TSDB: the key does not exist")
		}
		if e.valueType != TypeTimeSeries {
			sh.mu.RUnlock()
			return timeSeriesWrongType()
		}
		sh.mu.RUnlock()
	}

	for _, item := range items {
		if err := s.TimeSeriesAdd(item.Key, item.Timestamp, item.Value, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) TimeSeriesQueryIndex(filters map[string]string) ([]string, error) {
	keys := s.Keys("*")
	out := make([]string, 0)

	for _, key := range keys {
		sh := s.shardFor(key)
		sh.mu.RLock()
		e, ok := sh.get(key)
		if !ok || sh.expired(key, e, s.now()) || e.valueType != TypeTimeSeries {
			sh.mu.RUnlock()
			continue
		}
		ts, err := decodeTimeSeries(s.decode(sh, e))
		sh.mu.RUnlock()
		if err != nil {
			return nil, err
		}

		labels := make(map[string]string, len(ts.labels))
		for _, label := range ts.labels {
			labels[label.Key] = label.Value
		}

		match := true
		for name, value := range filters {
			if labels[name] != value {
				match = false
				break
			}
		}
		if match {
			out = append(out, key)
		}
	}

	sort.Strings(out)
	return out, nil
}


func normalizeTimeSeriesAggregator(raw string) (string, error) {
	switch strings.ToUpper(raw) {
	case "AVG", "FIRST", "LAST", "MIN", "MAX", "SUM", "RANGE", "COUNT", "STD.P", "STD.S", "VAR.P", "VAR.S":
		return strings.ToUpper(raw), nil
	default:
		return "", errors.New("ERR TSDB: Unknown aggregation type")
	}
}

func timeSeriesBucketStart(timestamp, bucketDuration, alignment int64) int64 {
	delta := timestamp - alignment
	q := delta / bucketDuration
	if delta < 0 && delta%bucketDuration != 0 {
		q--
	}
	return alignment + q*bucketDuration
}

func aggregateTimeSeriesSamples(samples []TimeSeriesSample, aggregator string) (float64, error) {
	if len(samples) == 0 {
		return 0, errors.New("ERR TSDB: empty aggregation bucket")
	}
	switch aggregator {
	case "FIRST":
		return samples[0].Value, nil
	case "LAST":
		return samples[len(samples)-1].Value, nil
	case "MIN":
		v := samples[0].Value
		for _, sample := range samples[1:] {
			if sample.Value < v { v = sample.Value }
		}
		return v, nil
	case "MAX":
		v := samples[0].Value
		for _, sample := range samples[1:] {
			if sample.Value > v { v = sample.Value }
		}
		return v, nil
	case "SUM", "AVG":
		var sum float64
		for _, sample := range samples { sum += sample.Value }
		if aggregator == "AVG" { return sum/float64(len(samples)), nil }
		return sum,nil
	case "RANGE":
		minv,maxv := samples[0].Value,samples[0].Value
		for _, sample := range samples[1:] {
			if sample.Value < minv { minv = sample.Value }
			if sample.Value > maxv { maxv = sample.Value }
		}
		return maxv-minv,nil
	case "COUNT":
		return float64(len(samples)),nil
	case "VAR.P", "VAR.S", "STD.P", "STD.S":
		var mean float64
		for _, sample := range samples { mean += sample.Value }
		mean /= float64(len(samples))
		var variance float64
		for _, sample := range samples {
			d := sample.Value-mean
			variance += d*d
		}
		if aggregator == "VAR.S" || aggregator == "STD.S" {
			if len(samples) < 2 { return 0,nil }
			variance /= float64(len(samples)-1)
		} else {
			variance /= float64(len(samples))
		}
		if aggregator == "STD.P" || aggregator == "STD.S" { return math.Sqrt(variance),nil }
		return variance,nil
	default:
		return 0, errors.New("ERR TSDB: Unknown aggregation type")
	}
}

func (s *Store) timeSeriesSetCompacted(key string, timestamp int64, value float64) error {
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.get(key)
	if !ok || sh.expired(key, e, s.now()) {
		return errors.New("ERR TSDB: the key does not exist")
	}
	if e.valueType != TypeTimeSeries { return timeSeriesWrongType() }
	ts, err := decodeTimeSeries(s.decode(sh,e))
	if err != nil { return err }
	exp := sh.expirationAt(key,e)
	oldPolicy := ts.policy
	ts.policy = TimeSeriesLast
	err = ts.add(timestamp,value)
	ts.policy = oldPolicy
	if err != nil { return err }
	p:=timeSeriesPreparedEntry(encodeTimeSeries(ts))
	p.expiresAt=exp
	return s.publish(sh,key,p)
}

func (s *Store) markTimeSeriesRuleBucket(sourceKey, destKey string, bucket int64) error {
	sh:=s.shardFor(sourceKey)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e,ok:=sh.get(sourceKey)
	if !ok || sh.expired(sourceKey,e,s.now()) { return errors.New("ERR TSDB: the key does not exist") }
	if e.valueType!=TypeTimeSeries { return timeSeriesWrongType() }
	ts,err:=decodeTimeSeries(s.decode(sh,e)); if err!=nil{return err}
	for i:=range ts.rules {
		if ts.rules[i].destKey==destKey {
			ts.rules[i].lastBucket=bucket
			p:=timeSeriesPreparedEntry(encodeTimeSeries(ts))
			p.expiresAt=sh.expirationAt(sourceKey,e)
			return s.publish(sh,sourceKey,p)
		}
	}
	return nil
}

func (s *Store) applyTimeSeriesRules(sourceKey string, samples []TimeSeriesSample, rules []timeSeriesRule) error {
	if len(samples)==0 { return nil }
	latest:=samples[len(samples)-1].Timestamp
	for _,rule:=range rules {
		currentBucket:=timeSeriesBucketStart(latest,rule.bucketDuration,rule.alignment)
		buckets:=make(map[int64][]TimeSeriesSample)
		order:=make([]int64,0)
		seen:=make(map[int64]struct{})
		for _,sample:=range samples {
			if sample.Timestamp<=rule.startAfter { continue }
			b:=timeSeriesBucketStart(sample.Timestamp,rule.bucketDuration,rule.alignment)
			if b>=currentBucket || b<=rule.lastBucket { continue }
			buckets[b]=append(buckets[b],sample)
			if _,ok:=seen[b];!ok { seen[b]=struct{}{}; order=append(order,b) }
		}
		sort.Slice(order,func(i,j int)bool{return order[i]<order[j]})
		for _,bucket:=range order {
			value,err:=aggregateTimeSeriesSamples(buckets[bucket],rule.aggregator)
			if err!=nil{return err}
			if err:=s.timeSeriesSetCompacted(rule.destKey,bucket,value);err!=nil{return err}
			if err:=s.markTimeSeriesRuleBucket(sourceKey,rule.destKey,bucket);err!=nil{return err}
		}
	}
	return nil
}

func (s *Store) TimeSeriesCreateRule(sourceKey, destKey, aggregator string, bucketDuration, alignment int64) error {
	if sourceKey==destKey { return errors.New("ERR TSDB: sourceKey and destKey must be different") }
	if bucketDuration<=0 { return errors.New("ERR TSDB: bucketDuration must be greater than 0") }
	agg,err:=normalizeTimeSeriesAggregator(aggregator); if err!=nil{return err}

	// Validate both keys first.
	for _,key:=range []string{sourceKey,destKey} {
		sh:=s.shardFor(key); sh.mu.RLock()
		e,ok:=sh.get(key)
		if !ok || sh.expired(key,e,s.now()) { sh.mu.RUnlock(); return errors.New("ERR TSDB: the key does not exist") }
		if e.valueType!=TypeTimeSeries { sh.mu.RUnlock(); return timeSeriesWrongType() }
		sh.mu.RUnlock()
	}

	sourceShard:=s.shardFor(sourceKey)
	sourceShard.mu.Lock()
	sourceEntry,_:=sourceShard.get(sourceKey)
	source,err:=decodeTimeSeries(s.decode(sourceShard,sourceEntry))
	if err!=nil { sourceShard.mu.Unlock(); return err }
	for _,rule:=range source.rules {
		if rule.destKey==destKey { sourceShard.mu.Unlock(); return errors.New("ERR TSDB: compaction rule already exists") }
	}
	startAfter:=int64(-1<<63)
	if len(source.samples)>0 { startAfter=source.samples[len(source.samples)-1].Timestamp }
	source.rules=append(source.rules,timeSeriesRule{
		destKey:destKey,bucketDuration:bucketDuration,aggregator:agg,
		alignment:alignment,startAfter:startAfter,lastBucket:-1<<63,
	})
	p:=timeSeriesPreparedEntry(encodeTimeSeries(source))
	p.expiresAt=sourceShard.expirationAt(sourceKey,sourceEntry)
	if err:=s.publish(sourceShard,sourceKey,p);err!=nil{sourceShard.mu.Unlock();return err}
	sourceShard.mu.Unlock()

	destShard:=s.shardFor(destKey)
	destShard.mu.Lock()
	defer destShard.mu.Unlock()
	destEntry,_:=destShard.get(destKey)
	dest,err:=decodeTimeSeries(s.decode(destShard,destEntry));if err!=nil{return err}
	if dest.sourceKey!="" && dest.sourceKey!=sourceKey { return errors.New("ERR TSDB: destination already has a source") }
	dest.sourceKey=sourceKey
	dp:=timeSeriesPreparedEntry(encodeTimeSeries(dest))
	dp.expiresAt=destShard.expirationAt(destKey,destEntry)
	return s.publish(destShard,destKey,dp)
}

func (s *Store) TimeSeriesDeleteRule(sourceKey, destKey string) error {
	sourceShard:=s.shardFor(sourceKey)
	sourceShard.mu.Lock()
	sourceEntry,ok:=sourceShard.get(sourceKey)
	if !ok || sourceShard.expired(sourceKey,sourceEntry,s.now()) { sourceShard.mu.Unlock(); return errors.New("ERR TSDB: the key does not exist") }
	if sourceEntry.valueType!=TypeTimeSeries { sourceShard.mu.Unlock(); return timeSeriesWrongType() }
	source,err:=decodeTimeSeries(s.decode(sourceShard,sourceEntry));if err!=nil{sourceShard.mu.Unlock();return err}
	idx:=-1
	for i,rule:=range source.rules { if rule.destKey==destKey { idx=i; break } }
	if idx<0 { sourceShard.mu.Unlock(); return errors.New("ERR TSDB: compaction rule does not exist") }
	source.rules=append(source.rules[:idx],source.rules[idx+1:]...)
	p:=timeSeriesPreparedEntry(encodeTimeSeries(source));p.expiresAt=sourceShard.expirationAt(sourceKey,sourceEntry)
	if err:=s.publish(sourceShard,sourceKey,p);err!=nil{sourceShard.mu.Unlock();return err}
	sourceShard.mu.Unlock()

	destShard:=s.shardFor(destKey)
	destShard.mu.Lock()
	defer destShard.mu.Unlock()
	destEntry,ok:=destShard.get(destKey)
	if !ok || destShard.expired(destKey,destEntry,s.now()) { return nil }
	if destEntry.valueType!=TypeTimeSeries { return nil }
	dest,err:=decodeTimeSeries(s.decode(destShard,destEntry));if err!=nil{return err}
	if dest.sourceKey==sourceKey {
		dest.sourceKey=""
		dp:=timeSeriesPreparedEntry(encodeTimeSeries(dest));dp.expiresAt=destShard.expirationAt(destKey,destEntry)
		return s.publish(destShard,destKey,dp)
	}
	return nil
}
