package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

const (
	tDigestDefaultCompression = int64(100)
	tDigestHeaderSize          = 80
)

var tDigestMagic = [4]byte{'S', 'T', 'D', 1}

type TDigestInfo struct {
	Compression       int64
	Capacity          int
	MergedNodes       int
	UnmergedNodes     int
	MergedWeight      int64
	UnmergedWeight    int64
	Observations      int64
	TotalCompressions int64
	MemoryUsage       int64
}

type tDigestNode struct {
	mean   float64
	weight int64
}

type tDigest struct {
	compression       int64
	capacity          int
	min               float64
	max               float64
	mergedNodes       int
	unmergedNodes     int
	totalCompressions int64
	mergedWeight      int64
	unmergedWeight    int64
	nodes             []tDigestNode
}

func tDigestCapacity(compression int64) (int, error) {
	if compression <= 0 {
		return 0, errors.New("ERR T-Digest: compression parameter needs to be a positive integer")
	}
	maxInt := int64(^uint(0) >> 1)
	if compression > (maxInt-10)/6 {
		return 0, errors.New("ERR T-Digest: allocation failed")
	}
	return int(6*compression + 10), nil
}

func newTDigest(compression int64) (*tDigest, error) {
	capacity, err := tDigestCapacity(compression)
	if err != nil {
		return nil, err
	}
	return &tDigest{
		compression: compression,
		capacity: capacity,
		min: math.Inf(1),
		max: math.Inf(-1),
		nodes: make([]tDigestNode, 0, capacity),
	}, nil
}

func tDigestPreparedEntry(value []byte) preparedEntry {
	return preparedEntry{
		entry: entry{entryData: entryData{
			codecID: 0, valueType: TypeTDigest, rawLength: uint32(len(value)),
		}},
		data: append([]byte(nil), value...),
	}
}

func encodeTDigest(td *tDigest) []byte {
	out := make([]byte, tDigestHeaderSize+len(td.nodes)*16)
	copy(out[:4], tDigestMagic[:])
	binary.LittleEndian.PutUint64(out[8:16], uint64(td.compression))
	binary.LittleEndian.PutUint64(out[16:24], uint64(td.capacity))
	binary.LittleEndian.PutUint64(out[24:32], math.Float64bits(td.min))
	binary.LittleEndian.PutUint64(out[32:40], math.Float64bits(td.max))
	binary.LittleEndian.PutUint64(out[40:48], uint64(td.mergedNodes))
	binary.LittleEndian.PutUint64(out[48:56], uint64(td.unmergedNodes))
	binary.LittleEndian.PutUint64(out[56:64], uint64(td.totalCompressions))
	binary.LittleEndian.PutUint64(out[64:72], uint64(td.mergedWeight))
	binary.LittleEndian.PutUint64(out[72:80], uint64(td.unmergedWeight))
	off := tDigestHeaderSize
	for _, n := range td.nodes {
		binary.LittleEndian.PutUint64(out[off:off+8], math.Float64bits(n.mean))
		binary.LittleEndian.PutUint64(out[off+8:off+16], uint64(n.weight))
		off += 16
	}
	return out
}

func decodeTDigest(value []byte) (*tDigest, error) {
	if len(value) < tDigestHeaderSize || !bytes.Equal(value[:4], tDigestMagic[:]) {
		return nil, errors.New("invalid TDigest")
	}
	compression := int64(binary.LittleEndian.Uint64(value[8:16]))
	capacity := int(binary.LittleEndian.Uint64(value[16:24]))
	expectedCap, err := tDigestCapacity(compression)
	if err != nil || capacity != expectedCap {
		return nil, errors.New("invalid TDigest")
	}
	min := math.Float64frombits(binary.LittleEndian.Uint64(value[24:32]))
	max := math.Float64frombits(binary.LittleEndian.Uint64(value[32:40]))
	mergedNodes := int(binary.LittleEndian.Uint64(value[40:48]))
	unmergedNodes := int(binary.LittleEndian.Uint64(value[48:56]))
	totalComp := int64(binary.LittleEndian.Uint64(value[56:64]))
	mergedWeight := int64(binary.LittleEndian.Uint64(value[64:72]))
	unmergedWeight := int64(binary.LittleEndian.Uint64(value[72:80]))
	if mergedNodes < 0 || unmergedNodes < 0 || mergedNodes+unmergedNodes > capacity ||
		mergedWeight < 0 || unmergedWeight < 0 || totalComp < 0 {
		return nil, errors.New("invalid TDigest")
	}
	nodeCount := mergedNodes + unmergedNodes
	if tDigestHeaderSize+nodeCount*16 != len(value) {
		return nil, errors.New("invalid TDigest")
	}
	nodes := make([]tDigestNode, nodeCount)
	off := tDigestHeaderSize
	var sumMerged, sumUnmerged int64
	for i := range nodes {
		mean := math.Float64frombits(binary.LittleEndian.Uint64(value[off : off+8]))
		weight := int64(binary.LittleEndian.Uint64(value[off+8 : off+16]))
		if math.IsNaN(mean) || math.IsInf(mean, 0) || weight <= 0 {
			return nil, errors.New("invalid TDigest")
		}
		nodes[i] = tDigestNode{mean: mean, weight: weight}
		if i < mergedNodes {
			sumMerged += weight
		} else {
			sumUnmerged += weight
		}
		off += 16
	}
	if sumMerged != mergedWeight || sumUnmerged != unmergedWeight {
		return nil, errors.New("invalid TDigest")
	}
	if nodeCount == 0 {
		min, max = math.Inf(1), math.Inf(-1)
	} else if math.IsNaN(min) || math.IsInf(min, 0) || math.IsNaN(max) || math.IsInf(max, 0) || min > max {
		return nil, errors.New("invalid TDigest")
	}
	return &tDigest{
		compression: compression, capacity: capacity, min: min, max: max,
		mergedNodes: mergedNodes, unmergedNodes: unmergedNodes,
		totalCompressions: totalComp, mergedWeight: mergedWeight,
		unmergedWeight: unmergedWeight, nodes: nodes,
	}, nil
}

func (td *tDigest) add(mean float64, weight int64) error {
	if math.IsNaN(mean) || math.IsInf(mean, 0) {
		return errors.New("ERR T-Digest: val parameter needs to be a finite number")
	}
	if len(td.nodes) == td.capacity {
		if err := td.compress(); err != nil {
			return err
		}
	}
	if len(td.nodes) >= td.capacity || weight <= 0 || td.unmergedWeight > math.MaxInt64-weight ||
		td.mergedWeight > math.MaxInt64-(td.unmergedWeight+weight) {
		return errors.New("ERR T-Digest: overflow detected")
	}
	if mean < td.min {
		td.min = mean
	}
	if mean > td.max {
		td.max = mean
	}
	td.nodes = append(td.nodes, tDigestNode{mean: mean, weight: weight})
	td.unmergedNodes++
	td.unmergedWeight += weight
	return nil
}

func (td *tDigest) compress() error {
	if td.unmergedNodes == 0 {
		return nil
	}
	sort.SliceStable(td.nodes, func(i, j int) bool { return td.nodes[i].mean < td.nodes[j].mean })
	total := td.mergedWeight + td.unmergedWeight
	if total <= 1 {
		td.mergedNodes += td.unmergedNodes
		td.mergedWeight = total
		td.unmergedNodes = 0
		td.unmergedWeight = 0
		td.totalCompressions++
		return nil
	}
	denom := 2 * math.Pi * float64(total) * math.Log(float64(total))
	normalizer := float64(td.compression) / denom
	cur := 0
	weightSoFar := float64(0)
	for i := 1; i < len(td.nodes); i++ {
		proposed := td.nodes[cur].weight + td.nodes[i].weight
		z := float64(proposed) * normalizer
		q0 := weightSoFar / float64(total)
		q2 := (weightSoFar + float64(proposed)) / float64(total)
		if z <= q0*(1-q0) && z <= q2*(1-q2) {
			oldWeight := td.nodes[cur].weight
			td.nodes[cur].weight = proposed
			delta := td.nodes[i].mean - td.nodes[cur].mean
			td.nodes[cur].mean += delta * float64(td.nodes[i].weight) / float64(proposed)
			_ = oldWeight
		} else {
			weightSoFar += float64(td.nodes[cur].weight)
			cur++
			td.nodes[cur] = td.nodes[i]
		}
	}
	td.nodes = td.nodes[:cur+1]
	td.mergedNodes = len(td.nodes)
	td.mergedWeight = total
	td.unmergedNodes = 0
	td.unmergedWeight = 0
	td.totalCompressions++
	return nil
}

func weightedAverage(x1, w1, x2, w2 float64) float64 {
	if w1+w2 == 0 {
		return (x1 + x2) / 2
	}
	x := (x1*w1 + x2*w2) / (w1+w2)
	lo, hi := x1, x2
	if lo > hi {
		lo, hi = hi, lo
	}
	if x < lo { return lo }
	if x > hi { return hi }
	return x
}

func (td *tDigest) quantile(q float64) float64 {
	_ = td.compress()
	if q < 0 || q > 1 || td.mergedNodes == 0 {
		return math.NaN()
	}
	if td.mergedNodes == 1 {
		return td.nodes[0].mean
	}
	index := q * float64(td.mergedWeight)
	if index < 1 {
		return td.min
	}
	if index > float64(td.mergedWeight-1) {
		return td.max
	}
	leftWeight := float64(td.nodes[0].weight)
	if leftWeight > 1 && index < leftWeight/2 {
		return td.min + (index-1)/(leftWeight/2-1)*(td.nodes[0].mean-td.min)
	}
	right := td.nodes[td.mergedNodes-1]
	rightWeight := float64(right.weight)
	if rightWeight > 1 && float64(td.mergedWeight)-index <= rightWeight/2 {
		return td.max - (float64(td.mergedWeight)-index-1)/(rightWeight/2-1)*(td.max-right.mean)
	}
	weightSoFar := leftWeight / 2
	for i := 0; i < td.mergedNodes-1; i++ {
		a, b := td.nodes[i], td.nodes[i+1]
		aw, bw := float64(a.weight), float64(b.weight)
		dw := (aw+bw)/2
		if weightSoFar+dw > index {
			leftUnit, rightUnit := float64(0), float64(0)
			if aw == 1 {
				if index-weightSoFar < 0.5 { return a.mean }
				leftUnit = 0.5
			}
			if bw == 1 {
				if weightSoFar+dw-index <= 0.5 { return b.mean }
				rightUnit = 0.5
			}
			z1 := index-weightSoFar-leftUnit
			z2 := weightSoFar+dw-index-rightUnit
			return weightedAverage(a.mean,z2,b.mean,z1)
		}
		weightSoFar += dw
	}
	return td.max
}

func (td *tDigest) cdf(v float64) float64 {
	_ = td.compress()
	if td.mergedNodes == 0 { return math.NaN() }
	if v < td.min { return 0 }
	if v > td.max { return 1 }
	if td.mergedNodes == 1 { return 0.5 }
	n := td.mergedNodes
	left := td.nodes[0]
	if v < left.mean {
		width := left.mean-td.min
		if width > 0 {
			if v == td.min { return 0.5/float64(td.mergedWeight) }
			return (1+(v-td.min)/width*(float64(left.weight)/2-1))/float64(td.mergedWeight)
		}
		return 0
	}
	right := td.nodes[n-1]
	if v > right.mean {
		width := td.max-right.mean
		if width > 0 {
			if v == td.max { return 1-0.5/float64(td.mergedWeight) }
			dq := (1+(td.max-v)/width*(float64(right.weight)/2-1))/float64(td.mergedWeight)
			return 1-dq
		}
		return 1
	}
	weightSoFar := float64(0)
	for i:=0;i<n-1;i++ {
		if td.nodes[i].mean == v {
			dw:=float64(0)
			for i<n && td.nodes[i].mean==v {
				dw += float64(td.nodes[i].weight); i++
			}
			return (weightSoFar+dw/2)/float64(td.mergedWeight)
		}
		if td.nodes[i].mean <= v && v < td.nodes[i+1].mean {
			a,b:=td.nodes[i],td.nodes[i+1]
			aw,bw:=float64(a.weight),float64(b.weight)
			if b.mean-a.mean>0 {
				leftExcluded,rightExcluded:=float64(0),float64(0)
				if aw==1 {
					if bw==1 { return (weightSoFar+1)/float64(td.mergedWeight) }
					leftExcluded=0.5
				} else if bw==1 { rightExcluded=0.5 }
				dw:=(aw+bw)/2
				base:=weightSoFar+aw/2+leftExcluded
				return (base+(dw-leftExcluded-rightExcluded)*(v-a.mean)/(b.mean-a.mean))/float64(td.mergedWeight)
			}
			return (weightSoFar+(aw+bw)/2)/float64(td.mergedWeight)
		}
		weightSoFar += float64(td.nodes[i].weight)
	}
	return 1-0.5/float64(td.mergedWeight)
}

func halfRoundDown(f float64) float64 {
	intp, frac := math.Modf(f)
	if math.Abs(frac) <= 0.5 { return intp }
	if intp >= 0 { return intp+1 }
	return intp-1
}

func (td *tDigest) rank(v float64, reverse bool) int64 {
	size:=td.mergedWeight+td.unmergedWeight
	if size==0 { return -2 }
	if v<td.min { if reverse{return size}; return -1 }
	if v>td.max { if reverse{return -1}; return size }
	cdf:=td.cdf(v)
	x:=cdf*float64(size)
	if reverse {
		return int64(math.Round(float64(size)-math.Round(x)))
	}
	return int64(halfRoundDown(x))
}

func (td *tDigest) byRank(rank int64, reverse bool) float64 {
	size:=td.mergedWeight+td.unmergedWeight
	if size==0 { return math.NaN() }
	if rank==0 {
		if reverse{return td.max}; return td.min
	}
	if rank>=size {
		if reverse{return math.Inf(-1)}; return math.Inf(1)
	}
	qrank:=rank
	if reverse { qrank=size-rank-1 }
	return td.quantile(float64(qrank)/float64(size))
}

func (td *tDigest) trimmedMean(low, high float64) float64 {
	_ = td.compress()
	if td.mergedNodes==0 { return math.NaN() }
	if td.mergedNodes==1 { return td.nodes[0].mean }
	leftmost:=math.Floor(float64(td.mergedWeight)*low)
	rightmost:=math.Ceil(float64(td.mergedWeight)*high)
	countDone,sum,count:=float64(0),float64(0),float64(0)
	for i:=0;i<td.mergedNodes;i++ {
		w:=float64(td.nodes[i].weight)
		add:=w
		skip:=math.Min(math.Max(0,leftmost-countDone),add)
		add-=skip
		add=math.Min(math.Max(0,rightmost-countDone),add)
		countDone+=w
		sum+=td.nodes[i].mean*add
		count+=add
		if countDone>=rightmost {break}
	}
	return sum/count
}

func (td *tDigest) reset() {
	td.min=math.Inf(1); td.max=math.Inf(-1)
	td.mergedNodes=0; td.unmergedNodes=0
	td.mergedWeight=0; td.unmergedWeight=0
	td.totalCompressions=0
	td.nodes=td.nodes[:0]
}

func tDigestInfo(td *tDigest) TDigestInfo {
	return TDigestInfo{
		Compression: td.compression,
		Capacity: td.capacity,
		MergedNodes: td.mergedNodes,
		UnmergedNodes: td.unmergedNodes,
		MergedWeight: td.mergedWeight,
		UnmergedWeight: td.unmergedWeight,
		Observations: td.mergedWeight+td.unmergedWeight,
		TotalCompressions: td.totalCompressions,
		MemoryUsage: int64(80+16*td.capacity),
	}
}

func (s *Store) TDigestCreate(key string, compression int64) error {
	td,err:=newTDigest(compression); if err!=nil{return err}
	sh:=s.shardFor(key); sh.mu.Lock(); defer sh.mu.Unlock()
	if e,ok:=sh.get(key); ok && !sh.expired(key,e,s.now()) {
		if e.valueType==TypeTDigest {return errors.New("ERR T-Digest: key already exists")}
		return tDigestWrongType()
	}
	return s.publish(sh,key,tDigestPreparedEntry(encodeTDigest(td)))
}

func tDigestWrongType() error { return errors.New("WRONGTYPE Operation against a key holding the wrong kind of value") }

func (s *Store) withTDigestWrite(key string, fn func(*tDigest) error) error {
	sh:=s.shardFor(key); sh.mu.Lock(); defer sh.mu.Unlock()
	e,ok:=sh.get(key)
	if !ok || sh.expired(key,e,s.now()) {return errors.New("ERR T-Digest: key does not exist")}
	if e.valueType!=TypeTDigest{return tDigestWrongType()}
	td,err:=decodeTDigest(s.decode(sh,e)); if err!=nil{return err}
	exp:=sh.expirationAt(key,e)
	if err:=fn(td); err!=nil{return err}
	p:=tDigestPreparedEntry(encodeTDigest(td)); p.expiresAt=exp
	return s.publish(sh,key,p)
}

func (s *Store) TDigestAdd(key string, values []float64) error {
	return s.withTDigestWrite(key,func(td *tDigest) error{
		for _,v:=range values { if err:=td.add(v,1); err!=nil{return err} }
		return nil
	})
}

func (s *Store) TDigestReset(key string) error {
	return s.withTDigestWrite(key,func(td *tDigest) error{td.reset();return nil})
}

func (s *Store) TDigestInfo(key string) (TDigestInfo,error) {
	sh:=s.shardFor(key); sh.mu.RLock(); defer sh.mu.RUnlock()
	e,ok:=sh.get(key)
	if !ok || sh.expired(key,e,s.now()){return TDigestInfo{},errors.New("ERR T-Digest: key does not exist")}
	if e.valueType!=TypeTDigest{return TDigestInfo{},tDigestWrongType()}
	td,err:=decodeTDigest(s.decode(sh,e)); if err!=nil{return TDigestInfo{},err}
	return tDigestInfo(td),nil
}

func (s *Store) TDigestMinMax(key string, max bool) (float64,error) {
	sh:=s.shardFor(key); sh.mu.RLock(); defer sh.mu.RUnlock()
	e,ok:=sh.get(key)
	if !ok || sh.expired(key,e,s.now()){return 0,errors.New("ERR T-Digest: key does not exist")}
	if e.valueType!=TypeTDigest{return 0,tDigestWrongType()}
	td,err:=decodeTDigest(s.decode(sh,e)); if err!=nil{return 0,err}
	if len(td.nodes)==0{return math.NaN(),nil}
	if max{return td.max,nil}; return td.min,nil
}

func (s *Store) tDigestReadCompress(key string, fn func(*tDigest) interface{}) (interface{},error) {
	sh:=s.shardFor(key); sh.mu.Lock(); defer sh.mu.Unlock()
	e,ok:=sh.get(key)
	if !ok || sh.expired(key,e,s.now()){return nil,errors.New("ERR T-Digest: key does not exist")}
	if e.valueType!=TypeTDigest{return nil,tDigestWrongType()}
	td,err:=decodeTDigest(s.decode(sh,e)); if err!=nil{return nil,err}
	exp:=sh.expirationAt(key,e)
	result:=fn(td)
	if td.unmergedNodes==0 {
		p:=tDigestPreparedEntry(encodeTDigest(td)); p.expiresAt=exp
		if err:=s.publish(sh,key,p);err!=nil{return nil,err}
	}
	return result,nil
}

func (s *Store) TDigestQuantiles(key string, qs []float64)([]float64,error){
	x,err:=s.tDigestReadCompress(key,func(td *tDigest) interface{}{
		out:=make([]float64,len(qs)); for i,q:=range qs{out[i]=td.quantile(q)}; return out
	})
	if err!=nil{return nil,err}; return x.([]float64),nil
}
func (s *Store) TDigestCDF(key string, values []float64)([]float64,error){
	x,err:=s.tDigestReadCompress(key,func(td *tDigest) interface{}{
		out:=make([]float64,len(values)); for i,v:=range values{out[i]=td.cdf(v)}; return out
	})
	if err!=nil{return nil,err}; return x.([]float64),nil
}
func (s *Store) TDigestRanks(key string, values []float64, reverse bool)([]int64,error){
	x,err:=s.tDigestReadCompress(key,func(td *tDigest) interface{}{
		out:=make([]int64,len(values)); for i,v:=range values{out[i]=td.rank(v,reverse)}; return out
	})
	if err!=nil{return nil,err}; return x.([]int64),nil
}
func (s *Store) TDigestByRanks(key string, ranks []int64, reverse bool)([]float64,error){
	x,err:=s.tDigestReadCompress(key,func(td *tDigest) interface{}{
		out:=make([]float64,len(ranks)); for i,r:=range ranks{out[i]=td.byRank(r,reverse)}; return out
	})
	if err!=nil{return nil,err}; return x.([]float64),nil
}
func (s *Store) TDigestTrimmedMean(key string, low, high float64)(float64,error){
	x,err:=s.tDigestReadCompress(key,func(td *tDigest) interface{}{return td.trimmedMean(low,high)})
	if err!=nil{return 0,err}; return x.(float64),nil
}

func cloneTDigest(td *tDigest)*tDigest{
	cp:=*td; cp.nodes=append([]tDigestNode(nil),td.nodes...); return &cp
}

func mergeInto(dest,src *tDigest) error {
	if err:=dest.compress();err!=nil{return err}
	if err:=src.compress();err!=nil{return err}
	for i:=0;i<src.mergedNodes;i++{
		if err:=dest.add(src.nodes[i].mean,src.nodes[i].weight);err!=nil{return err}
	}
	return nil
}

func (s *Store) TDigestMerge(destination string, sources []string, compression *int64, override bool) error {
	if len(sources)==0{return errors.New("ERR T-Digest: numkeys needs to be a positive integer")}
	unlock:=s.lockAll(); defer unlock(); now:=s.now()

	var existing *tDigest
	var exp stamp
	dstShard:=s.shardFor(destination)
	if e,ok:=dstShard.get(destination); ok && !dstShard.expired(destination,e,now){
		if e.valueType!=TypeTDigest{return tDigestWrongType()}
		td,err:=decodeTDigest(s.decode(dstShard,e)); if err!=nil{return err}
		existing=td; exp=dstShard.expirationAt(destination,e)
	}

	srcs:=make([]*tDigest,len(sources))
	maxCompression:=int64(0)
	for i,key:=range sources{
		sh:=s.shardFor(key); e,ok:=sh.get(key)
		if !ok || sh.expired(key,e,now){return errors.New("ERR T-Digest: key does not exist")}
		if e.valueType!=TypeTDigest{return tDigestWrongType()}
		td,err:=decodeTDigest(s.decode(sh,e)); if err!=nil{return err}
		srcs[i]=td
		if td.compression>maxCompression{maxCompression=td.compression}
	}

	comp:=maxCompression
	if existing!=nil && !override && compression==nil {comp=existing.compression}
	if compression!=nil{comp=*compression}
	if comp<=0{comp=maxCompression}
	out,err:=newTDigest(comp); if err!=nil{return err}
	if existing!=nil && !override {
		if err:=mergeInto(out,cloneTDigest(existing));err!=nil{return errors.New("ERR T-Digest: overflow detected")}
	}
	for _,src:=range srcs{
		if err:=mergeInto(out,cloneTDigest(src));err!=nil{return errors.New("ERR T-Digest: overflow detected")}
	}
	p:=tDigestPreparedEntry(encodeTDigest(out)); p.expiresAt=exp
	return s.publish(dstShard,destination,p)
}
