package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

var indexedZSetHeader = [...]byte{'S','Z',5}
const indexedZSetFixed = 15
const indexedZSetPromoteMembers = 32

func isIndexedZSet(data []byte) bool {
	return len(data) >= indexedZSetFixed && bytes.Equal(data[:3], indexedZSetHeader[:])
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
	dataStart = indexedZSetFixed + slots*4
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

func indexedZSetRecordKnown(data []byte, pos, used, start int) (member []byte, score float64, end int, err error) {
	if pos < 0 || pos >= used || start+pos+8 > start+used {
		return nil,0,0,errors.New("invalid indexed zset offset")
	}
	off := start + pos
	score = math.Float64frombits(binary.LittleEndian.Uint64(data[off:off+8]))
	off += 8
	if math.IsNaN(score) {
		return nil,0,0,errors.New("invalid indexed zset score")
	}
	score = normalizeZSetScore(score)
	length,e := readZSetUvarint(data,&off)
	if e != nil || length > uint64(start+used-off) {
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
		raw := binary.LittleEndian.Uint32(data[indexedZSetFixed+slot*4:indexedZSetFixed+slot*4+4])
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
	out := make([]byte,8,8+binary.MaxVarintLen64+len(member))
	binary.LittleEndian.PutUint64(out[:8],math.Float64bits(normalizeZSetScore(score)))
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
	if dataCap-used < 512 {
		dataCap = used + 512
	}
	total := indexedZSetFixed + slots*4 + dataCap
	if total > maxPackedZSetBytes {
		return nil,errors.New("ERR sorted set exceeds 32 MiB limit")
	}
	out := make([]byte,total)
	copy(out[:3],indexedZSetHeader[:])
	binary.LittleEndian.PutUint32(out[3:7],uint32(len(canonical)))
	binary.LittleEndian.PutUint32(out[7:11],uint32(slots))
	start := indexedZSetFixed + slots*4
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
		binary.LittleEndian.PutUint32(out[indexedZSetFixed+slot*4:indexedZSetFixed+slot*4+4],uint32(cursor+1))
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
		raw:=binary.LittleEndian.Uint32(data[indexedZSetFixed+slot*4:indexedZSetFixed+slot*4+4])
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
		binary.LittleEndian.PutUint32(data[indexedZSetFixed+slot*4:indexedZSetFixed+slot*4+4],uint32(used+1))
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
