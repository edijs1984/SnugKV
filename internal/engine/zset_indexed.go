package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

// Header byte 5 stores 4-byte slot offsets; 6 stores 2-byte offsets and is
// used when the payload capacity fits in 16 bits (medium zsets).
var indexedZSetHeader = [...]byte{'S','Z',5}

const indexedZSetNarrowVersion = 6
const indexedZSetNarrowMaxPayload = 65534

func indexedZSetSlotWidth(data []byte) int {
	if data[2] == indexedZSetNarrowVersion {
		return 2
	}
	return 4
}

func indexedZSetSlotGet(data []byte, slot int) uint32 {
	if data[2] == indexedZSetNarrowVersion {
		o := indexedZSetFixed + slot*2
		return uint32(binary.LittleEndian.Uint16(data[o : o+2]))
	}
	o := indexedZSetFixed + slot*4
	return binary.LittleEndian.Uint32(data[o : o+4])
}

func indexedZSetSlotSet(data []byte, slot int, v uint32) {
	if data[2] == indexedZSetNarrowVersion {
		o := indexedZSetFixed + slot*2
		binary.LittleEndian.PutUint16(data[o:o+2], uint16(v))
		return
	}
	o := indexedZSetFixed + slot*4
	binary.LittleEndian.PutUint32(data[o:o+4], v)
}
const indexedZSetFixed = 15
const indexedZSetPromoteMembers = 32

func isIndexedZSet(data []byte) bool {
	return len(data) >= indexedZSetFixed && data[0] == 'S' && data[1] == 'Z' && (data[2] == indexedZSetHeader[2] || data[2] == indexedZSetNarrowVersion)
}

func indexedZSetMeta(data []byte) (count, slots, used, dataStart int, err error) {
	if !isIndexedZSet(data) {
		return 0,0,0,0,errors.New("invalid indexed zset")
	}
	count = int(binary.LittleEndian.Uint32(data[3:7]))
	slots = int(binary.LittleEndian.Uint32(data[7:11]))
	used = int(binary.LittleEndian.Uint32(data[11:15]))
	if slots < 8 || slots&(slots-1) != 0 || count < 0 || count > slots {
		return 0,0,0,0,errors.New("invalid indexed zset")
	}
	dataStart = indexedZSetFixed + slots*indexedZSetSlotWidth(data)
	if dataStart > len(data) || used < 0 || dataStart+used > len(data) {
		return 0,0,0,0,errors.New("invalid indexed zset")
	}
	return
}

func nextZSetPow2(n int) int {
	p := 8
	for p < n {
		p <<= 1
	}
	return p
}

// Record layout: uvarint score tag, uvarint member length, member bytes.
// Integral scores (|n| < 2^52) are stored as zigzag(n)<<1; any other score is
// tag 1 followed by 8 bytes of IEEE-754. Integer scores are the common case
// (ranks, timestamps, counters) and shrink from 8 bytes to 1-4.
const indexedZSetMaxIntScore = 1 << 52

func indexedZSetRecordKnown(data []byte, pos, used, start int) (member []byte, score float64, end int, err error) {
	if pos < 0 || pos >= used {
		return nil,0,0,errors.New("invalid indexed zset offset")
	}
	limit := start + used
	off := start + pos
	tag, n := binary.Uvarint(data[off:limit])
	if n <= 0 {
		return nil,0,0,errors.New("invalid indexed zset score")
	}
	off += n
	if tag&1 == 0 {
		z := tag >> 1
		score = float64(int64(z>>1) ^ -int64(z&1))
	} else {
		if tag != 1 || off+8 > limit {
			return nil,0,0,errors.New("invalid indexed zset score")
		}
		score = math.Float64frombits(binary.LittleEndian.Uint64(data[off:off+8]))
		off += 8
		if math.IsNaN(score) {
			return nil,0,0,errors.New("invalid indexed zset score")
		}
		score = normalizeZSetScore(score)
	}
	length,e := readZSetUvarint(data[:limit],&off)
	if e != nil || length > uint64(limit-off) {
		return nil,0,0,errors.New("invalid indexed zset member")
	}
	valueEnd := off + int(length)
	return data[off:valueEnd], score, valueEnd-start, nil
}

func indexedZSetRecord(data []byte, pos int) (member []byte, score float64, end int, err error) {
	_,_,used,start,e := indexedZSetMeta(data)
	if e != nil {
		return nil,0,0,e
	}
	return indexedZSetRecordKnown(data,pos,used,start)
}

func indexedZSetFindKnown(data, target []byte, slots, used, dataStart int) (slot,pos int, found bool, score float64, err error) {
	mask := slots-1
	hashStart := int(hashField64(target)) & mask
	for probe:=0;probe<slots;probe++ {
		slot=(hashStart+probe)&mask
		raw := indexedZSetSlotGet(data,slot)
		if raw==0 {
			return slot,0,false,0,nil
		}
		pos=int(raw-1)
		member,sc,_,e := indexedZSetRecordKnown(data,pos,used,dataStart)
		if e != nil {
			return 0,0,false,0,e
		}
		if bytes.Equal(member,target) {
			return slot,pos,true,sc,nil
		}
	}
	return 0,0,false,0,errors.New("invalid indexed zset table")
}

func indexedZSetFind(data, target []byte) (slot,pos int, found bool, score float64, err error) {
	_,slots,used,dataStart,e := indexedZSetMeta(data)
	if e != nil {
		return 0,0,false,0,e
	}
	return indexedZSetFindKnown(data,target,slots,used,dataStart)
}

func indexedZSetRecordBytes(member []byte, score float64) []byte {
	score = normalizeZSetScore(score)
	out := make([]byte,0,2*binary.MaxVarintLen64+8+len(member))
	if score == math.Trunc(score) && math.Abs(score) < indexedZSetMaxIntScore {
		n := int64(score)
		z := uint64(n<<1) ^ uint64(n>>63)
		out = appendZSetUvarint(out,z<<1)
	} else {
		out = appendZSetUvarint(out,1)
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:],math.Float64bits(score))
		out = append(out,raw[:]...)
	}
	out = appendZSetUvarint(out,uint64(len(member)))
	out = append(out,member...)
	return out
}

func encodeIndexedZSet(items []ZSetItem) ([]byte,error) {
	return encodeIndexedZSetWithReserve(items, true)
}

func encodeIndexedZSetCompact(items []ZSetItem) ([]byte,error) {
	return encodeIndexedZSetWithReserve(items, false)
}

func encodeIndexedZSetWithReserve(items []ZSetItem, aggressive bool) ([]byte,error) {
	return encodeIndexedZSetSized(items, aggressive, false)
}

// trimIndexedZSet rebuilds an idle indexed zset with no payload headroom and
// reports whether the result is smaller. Dead records left behind by score
// updates are dropped as well. A later ZADD of a new member regrows it.
func trimIndexedZSet(data []byte) ([]byte, bool) {
	items, err := decodeIndexedZSet(data)
	if err != nil {
		return nil, false
	}
	trimmed, err := encodeIndexedZSetSized(items, false, true)
	if err != nil || len(trimmed) >= len(data) {
		return nil, false
	}
	return trimmed, true
}

func encodeIndexedZSetSized(items []ZSetItem, aggressive, exact bool) ([]byte,error) {
	if len(items)==0 {
		return nil,errors.New("invalid empty indexed zset")
	}
	latest := make(map[string]ZSetItem,len(items))
	for _,item := range items {
		if math.IsNaN(item.Score) {
			return nil,errors.New("ERR resulting score is not a number (NaN)")
		}
		latest[string(item.Member)] = ZSetItem{Member:append([]byte(nil),item.Member...),Score:normalizeZSetScore(item.Score)}
	}
	canonical := make([]ZSetItem,0,len(latest))
	for _,item := range latest {
		canonical = append(canonical,item)
	}
	sort.Slice(canonical,func(i,j int)bool{return zsetLess(canonical[i],canonical[j])})

	// Target at most 80% occupancy instead of reserving a fixed 2x slot
	// table. For medium ZSETs (for example 100 members), 128 slots are enough
	// and keep the compact representation below the next arena size class;
	// large ZSETs still naturally round to the same 2048-slot table at 1000
	// members.
	slotTarget := (len(canonical)*5 + 3) / 4
	slots := nextZSetPow2(slotTarget)
	records := make([][]byte,len(canonical))
	used := 0
	for i,item := range canonical {
		records[i] = indexedZSetRecordBytes(item.Member,item.Score)
		used += len(records[i])
	}
	dataCap := used + used/4
	if aggressive && len(canonical) >= 64 {
		// Medium and large ZSETs are commonly built incrementally. A 25% payload
		// reserve causes repeated decode/map/sort/re-encode rebuilds while the set
		// grows. Give indexed ZSETs one full payload of append headroom once they
		// reach 64 members; maintenance trims the completed value after writes go
		// quiet.
		dataCap = used * 2
	}
	if exact {
		dataCap = used
	} else if dataCap-used < 512 {
		dataCap = used + 512
	}
	slotWidth := 4
	version := indexedZSetHeader[2]
	if dataCap <= indexedZSetNarrowMaxPayload {
		slotWidth = 2
		version = indexedZSetNarrowVersion
	}
	total := indexedZSetFixed + slots*slotWidth + dataCap
	if total > maxPackedZSetBytes {
		return nil,errors.New("ERR sorted set exceeds 32 MiB limit")
	}
	out := make([]byte,total)
	copy(out[:3],indexedZSetHeader[:])
	out[2] = version
	binary.LittleEndian.PutUint32(out[3:7],uint32(len(canonical)))
	binary.LittleEndian.PutUint32(out[7:11],uint32(slots))
	start := indexedZSetFixed + slots*slotWidth
	cursor:=0
	for i,item := range canonical {
		slot,_,found,_,err := indexedZSetFind(out,item.Member)
		if err != nil {
			return nil,err
		}
		if found {
			return nil,errors.New("duplicate sorted set member")
		}
		copy(out[start+cursor:],records[i])
		indexedZSetSlotSet(out,slot,uint32(cursor+1))
		cursor += len(records[i])
		binary.LittleEndian.PutUint32(out[11:15],uint32(cursor))
	}
	return out,nil
}

func decodeIndexedZSet(data []byte) ([]ZSetItem,error) {
	count,slots,_,_,err := indexedZSetMeta(data)
	if err != nil {
		return nil,err
	}
	items := make([]ZSetItem,0,count)
	for slot:=0;slot<slots;slot++ {
		raw:=indexedZSetSlotGet(data,slot)
		if raw==0 {
			continue
		}
		member,score,_,e := indexedZSetRecord(data,int(raw-1))
		if e != nil {
			return nil,e
		}
		items = append(items,ZSetItem{Member:append([]byte(nil),member...),Score:score})
	}
	if len(items)!=count {
		return nil,errors.New("invalid indexed zset count")
	}
	sort.Slice(items,func(i,j int)bool{return zsetLess(items[i],items[j])})
	return items,nil
}


func indexedZSetIncrBy(data, member []byte, increment float64) (added bool, score float64, rebuilt []byte, err error) {
	count, slots, used, start, err := indexedZSetMeta(data)
	if err != nil {
		return false, 0, nil, err
	}
	slot, _, found, current, err := indexedZSetFindKnown(data, member, slots, used, start)
	if err != nil {
		return false, 0, nil, err
	}
	score = normalizeZSetScore(increment)
	if found {
		score = normalizeZSetScore(current + increment)
		if math.IsNaN(score) {
			return false, 0, nil, errors.New("ERR resulting score is not a number (NaN)")
		}
	}
	rec := indexedZSetRecordBytes(member, score)
	needRehash := !found && (count+1)*5 >= slots*4
	if needRehash || start+used+len(rec) > len(data) {
		items, e := decodeIndexedZSet(data)
		if e != nil {
			return false, 0, nil, e
		}
		idx := zsetFindMember(items, member)
		if idx >= 0 {
			items[idx].Score = score
		} else {
			items = append(items, ZSetItem{Member: append([]byte(nil), member...), Score: score})
			added = true
		}
		rebuilt, e = encodeIndexedZSet(items)
		return added, score, rebuilt, e
	}

	copy(data[start+used:], rec)
	indexedZSetSlotSet(data, slot, uint32(used+1))
	used += len(rec)
	if !found {
		count++
		added = true
		binary.LittleEndian.PutUint32(data[3:7], uint32(count))
	}
	binary.LittleEndian.PutUint32(data[11:15], uint32(used))
	return added, score, nil, nil
}

func indexedZSetRank(data, target []byte, reverse bool) (int64, bool, error) {
	count, slots, used, start, err := indexedZSetMeta(data)
	if err != nil {
		return 0, false, err
	}
	_, _, found, targetScore, err := indexedZSetFindKnown(data, target, slots, used, start)
	if err != nil || !found {
		return 0, found, err
	}
	targetItem := ZSetItem{Member: target, Score: targetScore}
	rank := int64(0)
	seen := 0
	for slot := 0; slot < slots; slot++ {
		raw := indexedZSetSlotGet(data,slot)
		if raw == 0 {
			continue
		}
		member, score, _, e := indexedZSetRecordKnown(data, int(raw-1), used, start)
		if e != nil {
			return 0, false, e
		}
		seen++
		item := ZSetItem{Member: member, Score: score}
		if reverse {
			if zsetLess(targetItem, item) {
				rank++
			}
		} else if zsetLess(item, targetItem) {
			rank++
		}
	}
	if seen != count {
		return 0, false, errors.New("invalid indexed zset count")
	}
	return rank, true, nil
}

func indexedZSetSmallRange(data []byte, startRank, stopRank int64, reverse bool) ([]ZSetItem, bool, error) {
	count, slots, used, start, err := indexedZSetMeta(data)
	if err != nil {
		return nil, false, err
	}
	startIndex, stopIndex, ok := normalizeZSetRange(count, startRank, stopRank)
	if !ok {
		return nil, true, nil
	}
	// This path is deliberately bounded for leaderboard/top-N traffic. Larger
	// ranges continue through the generic decode+sort implementation.
	if stopIndex >= 64 {
		return nil, false, nil
	}
	limit := stopIndex + 1
	best := make([]ZSetItem, 0, limit)
	seen := 0
	for slot := 0; slot < slots; slot++ {
		raw := indexedZSetSlotGet(data,slot)
		if raw == 0 {
			continue
		}
		member, score, _, e := indexedZSetRecordKnown(data, int(raw-1), used, start)
		if e != nil {
			return nil, false, e
		}
		seen++
		item := ZSetItem{Member: member, Score: score}
		pos := len(best)
		for i := 0; i < len(best); i++ {
			less := zsetLess(item, best[i])
			if reverse {
				less = zsetLess(best[i], item)
			}
			if less {
				pos = i
				break
			}
		}
		if pos >= limit {
			continue
		}
		if len(best) < limit {
			best = append(best, ZSetItem{})
		}
		copy(best[pos+1:], best[pos:len(best)-1])
		best[pos] = ZSetItem{Member: append([]byte(nil), member...), Score: score}
	}
	if seen != count {
		return nil, false, errors.New("invalid indexed zset count")
	}
	out := append([]ZSetItem(nil), best[startIndex:stopIndex+1]...)
	return out, true, nil
}

func indexedZSetScore(data,target []byte)(float64,bool,error) {
	_,_,found,score,err := indexedZSetFind(data,target)
	return score,found,err
}

// indexedZSetAddSimple handles ordinary ZADD without NX/XX/GT/LT/CH/INCR.
// It appends new/updated records and retargets the member slot. Old score
// records become harmless slack until the next rebuild.
func indexedZSetAddSimple(data []byte, pairs []ZSetItem)(added int64, rebuilt []byte, err error) {
	count,slots,used,start,err := indexedZSetMeta(data)
	if err != nil {
		return 0,nil,err
	}
	for i,pair := range pairs {
		if math.IsNaN(pair.Score) {
			return added,nil,errors.New("ERR resulting score is not a number (NaN)")
		}
		slot,_,found,current,e := indexedZSetFindKnown(data,pair.Member,slots,used,start)
		if e != nil {
			return added,nil,e
		}
		score := normalizeZSetScore(pair.Score)
		if found && current == score {
			continue
		}
		rec := indexedZSetRecordBytes(pair.Member,score)
		// Match the 80% occupancy target used by the encoder. Linear probing
		// remains bounded while avoiding an early 2x table expansion for medium
		// cardinalities such as 100 members.
		needRehash := !found && (count+1)*5 >= slots*4
		if needRehash || start+used+len(rec) > len(data) {
			items,e := decodeIndexedZSet(data)
			if e != nil {
				return added,nil,e
			}
			byMember := make(map[string]ZSetItem,len(items)+len(pairs)-i)
			for _,item := range items {
				byMember[string(item.Member)] = item
			}
			for j:=i;j<len(pairs);j++ {
				next := pairs[j]
				if math.IsNaN(next.Score) {
					return added,nil,errors.New("ERR resulting score is not a number (NaN)")
				}
				key := string(next.Member)
				if _,ok := byMember[key]; !ok {
					added++
				}
				byMember[key] = ZSetItem{Member:append([]byte(nil),next.Member...),Score:normalizeZSetScore(next.Score)}
			}
			items = items[:0]
			for _,item := range byMember {
				items = append(items,item)
			}
			rebuilt,e = encodeIndexedZSet(items)
			return added,rebuilt,e
		}

		copy(data[start+used:],rec)
		indexedZSetSlotSet(data,slot,uint32(used+1))
		used += len(rec)
		if !found {
			count++
			added++
		}
		binary.LittleEndian.PutUint32(data[3:7],uint32(count))
		binary.LittleEndian.PutUint32(data[11:15],uint32(used))
	}
	return added,nil,nil
}
