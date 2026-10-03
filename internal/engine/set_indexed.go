package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"
)

var indexedSetHeader = [...]byte{'S','S',4}
const indexedSetFixed = 15
const indexedSetPromoteMembers = 32

func isIndexedSet(data []byte) bool {
	return len(data) >= indexedSetFixed && bytes.Equal(data[:3], indexedSetHeader[:])
}

func indexedSetMeta(data []byte) (count, slots, used, dataStart int, err error) {
	if !isIndexedSet(data) {
		return 0,0,0,0,errors.New("invalid indexed set")
	}
	count = int(binary.LittleEndian.Uint32(data[3:7]))
	slots = int(binary.LittleEndian.Uint32(data[7:11]))
	used = int(binary.LittleEndian.Uint32(data[11:15]))
	if slots < 8 || slots&(slots-1) != 0 || count < 0 || count > slots {
		return 0,0,0,0,errors.New("invalid indexed set")
	}
	dataStart = indexedSetFixed + slots*4
	if dataStart > len(data) || used < 0 || dataStart+used > len(data) {
		return 0,0,0,0,errors.New("invalid indexed set")
	}
	return
}

func setUvarintLen(value uint64) int {
	var buf [binary.MaxVarintLen64]byte
	return binary.PutUvarint(buf[:], value)
}

func nextSetPow2(n int) int {
	p := 8
	for p < n {
		p <<= 1
	}
	return p
}

func indexedSetRecordKnown(data []byte, pos, used, start int) (member []byte, end int, err error) {
	if pos < 0 || pos >= used {
		return nil,0,errors.New("invalid indexed set offset")
	}
	off := start + pos
	length,e := readSetUvarint(data,&off)
	if e != nil || length > uint64(start+used-off) {
		return nil,0,errors.New("invalid indexed set member")
	}
	valueEnd := off + int(length)
	return data[off:valueEnd], valueEnd-start, nil
}

func indexedSetRecord(data []byte, pos int) (member []byte, end int, err error) {
	_,_,used,start,e := indexedSetMeta(data)
	if e != nil {
		return nil,0,e
	}
	return indexedSetRecordKnown(data,pos,used,start)
}

func indexedSetFindKnown(data, target []byte, slots, used, dataStart int) (slot,pos int, found bool, err error) {
	mask := slots-1
	hashStart := int(hashField64(target)) & mask
	for probe:=0; probe<slots; probe++ {
		slot = (hashStart+probe)&mask
		raw := binary.LittleEndian.Uint32(data[indexedSetFixed+slot*4:indexedSetFixed+slot*4+4])
		if raw == 0 {
			return slot,0,false,nil
		}
		pos = int(raw-1)
		member,_,e := indexedSetRecordKnown(data,pos,used,dataStart)
		if e != nil {
			return 0,0,false,e
		}
		if bytes.Equal(member,target) {
			return slot,pos,true,nil
		}
	}
	return 0,0,false,errors.New("invalid indexed set table")
}

func indexedSetFind(data, target []byte) (slot,pos int, found bool, err error) {
	_,slots,used,dataStart,e := indexedSetMeta(data)
	if e != nil {
		return 0,0,false,e
	}
	return indexedSetFindKnown(data,target,slots,used,dataStart)
}

func indexedSetRecordBytes(member []byte) []byte {
	out := make([]byte,0,len(member)+binary.MaxVarintLen64)
	out = appendSetUvarint(out,uint64(len(member)))
	out = append(out,member...)
	return out
}

func encodeIndexedSet(members [][]byte) ([]byte,error) {
	if len(members) == 0 {
		return nil,errors.New("invalid empty indexed set")
	}

	// Canonicalize once during promotion/rebuild. Hot SADD uses the hash table
	// directly and no longer needs sorted insertion.
	sorted := make([][]byte,len(members))
	for i := range members {
		sorted[i] = append([]byte(nil),members[i]...)
	}
	sort.Slice(sorted,func(i,j int)bool{return bytes.Compare(sorted[i],sorted[j])<0})
	for i:=1;i<len(sorted);i++ {
		if bytes.Equal(sorted[i-1],sorted[i]) {
			return nil,errors.New("duplicate set member")
		}
	}

	slots := nextSetPow2(len(sorted)*2)
	records := make([][]byte,len(sorted))
	used := 0
	for i,member := range sorted {
		records[i] = indexedSetRecordBytes(member)
		used += len(records[i])
	}
	dataCap := used + used/4
	if dataCap-used < 512 {
		dataCap = used + 512
	}
	total := indexedSetFixed + slots*4 + dataCap
	if total > maxPackedSetBytes {
		return nil,errors.New("ERR set exceeds 32 MiB limit")
	}

	out := make([]byte,total)
	copy(out[:3],indexedSetHeader[:])
	binary.LittleEndian.PutUint32(out[3:7],uint32(len(sorted)))
	binary.LittleEndian.PutUint32(out[7:11],uint32(slots))
	start := indexedSetFixed + slots*4
	cursor := 0
	for i,member := range sorted {
		slot,_,found,err := indexedSetFind(out,member)
		if err != nil {
			return nil,err
		}
		if found {
			return nil,errors.New("duplicate set member")
		}
		copy(out[start+cursor:],records[i])
		binary.LittleEndian.PutUint32(out[indexedSetFixed+slot*4:indexedSetFixed+slot*4+4],uint32(cursor+1))
		cursor += len(records[i])
		binary.LittleEndian.PutUint32(out[11:15],uint32(cursor))
	}
	return out,nil
}

func decodeIndexedSet(data []byte) ([][]byte,error) {
	count,slots,_,_,err := indexedSetMeta(data)
	if err != nil {
		return nil,err
	}
	members := make([][]byte,0,count)
	for slot:=0;slot<slots;slot++ {
		raw := binary.LittleEndian.Uint32(data[indexedSetFixed+slot*4:indexedSetFixed+slot*4+4])
		if raw == 0 {
			continue
		}
		member,_,e := indexedSetRecord(data,int(raw-1))
		if e != nil {
			return nil,e
		}
		members = append(members,append([]byte(nil),member...))
	}
	if len(members) != count {
		return nil,errors.New("invalid indexed set count")
	}
	sort.Slice(members,func(i,j int)bool{return bytes.Compare(members[i],members[j])<0})
	return members,nil
}

func indexedSetContains(data,target []byte)(bool,error) {
	_,_,found,err := indexedSetFind(data,target)
	return found,err
}

func indexedSetAdd(data []byte, members [][]byte) (added int64, rebuilt []byte, err error) {
	count,slots,used,start,err := indexedSetMeta(data)
	if err != nil {
		return 0,nil,err
	}
	for i,member := range members {
		slot,_,found,e := indexedSetFindKnown(data,member,slots,used,start)
		if e != nil {
			return added,nil,e
		}
		if found {
			continue
		}
		recordLen := setUvarintLen(uint64(len(member))) + len(member)
		needRehash := (count+1)*10 >= slots*7
		if needRehash || start+used+recordLen > len(data) {
			current,e := decodeIndexedSet(data)
			if e != nil {
				return added,nil,e
			}
			seen := make(map[string]struct{},len(current)+len(members)-i)
			for _,v := range current {
				seen[string(v)] = struct{}{}
			}
			for j:=i;j<len(members);j++ {
				if _,ok := seen[string(members[j])]; ok {
					continue
				}
				seen[string(members[j])] = struct{}{}
				current = append(current,append([]byte(nil),members[j]...))
				added++
			}
			rebuilt,e = encodeIndexedSet(current)
			return added,rebuilt,e
		}

		recordStart := start + used
		var varintBuf [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(varintBuf[:], uint64(len(member)))
		copy(data[recordStart:recordStart+n], varintBuf[:n])
		copy(data[recordStart+n:recordStart+n+len(member)], member)
		binary.LittleEndian.PutUint32(data[indexedSetFixed+slot*4:indexedSetFixed+slot*4+4],uint32(used+1))
		used += recordLen
		count++
		added++
		binary.LittleEndian.PutUint32(data[3:7],uint32(count))
		binary.LittleEndian.PutUint32(data[11:15],uint32(used))
	}
	return added,nil,nil
}
