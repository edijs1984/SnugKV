package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"
)

var indexedHashHeader = [...]byte{'S','H',4}
const indexedHashFixed = 15
const indexedHashPromoteFields = 32

func isIndexedHash(data []byte) bool {
	return len(data) >= indexedHashFixed && bytes.Equal(data[:3], indexedHashHeader[:])
}

func indexedHashMeta(data []byte) (count, slots, used, dataStart int, err error) {
	if !isIndexedHash(data) { return 0,0,0,0,errors.New("invalid indexed hash") }
	count=int(binary.LittleEndian.Uint32(data[3:7]))
	slots=int(binary.LittleEndian.Uint32(data[7:11]))
	used=int(binary.LittleEndian.Uint32(data[11:15]))
	if slots < 8 || slots&(slots-1)!=0 { return 0,0,0,0,errors.New("invalid indexed hash") }
	dataStart=indexedHashFixed+slots*4
	if dataStart > len(data) || used < 0 || dataStart+used > len(data) { return 0,0,0,0,errors.New("invalid indexed hash") }
	return
}

func hashField64(v []byte) uint64 {
	var h uint64 = 1469598103934665603
	for _, b := range v { h ^= uint64(b); h *= 1099511628211 }
	return h
}

func indexedHashRecord(data []byte, pos int) (field,value []byte,end int,err error) {
	_,slots,used,start,e:=indexedHashMeta(data); if e!=nil { err=e; return }
	if pos < 0 || pos >= used { err=errors.New("invalid indexed hash offset"); return }
	off:=start+pos
	fl,e:=readHashUvarint(data,&off); if e!=nil { err=e; return }
	vl,e:=readHashUvarint(data,&off); if e!=nil { err=e; return }
	if fl>uint64(len(data)-off) { err=errors.New("invalid indexed hash field"); return }
	fe:=off+int(fl); field=data[off:fe]; off=fe
	if vl>uint64(start+used-off) { err=errors.New("invalid indexed hash value"); return }
	ve:=off+int(vl); value=data[off:ve]; end=ve-start
	_ = slots
	return
}

func indexedHashFind(data, field []byte) (slot,pos int, found bool, err error) {
	_,slots,_,_,e:=indexedHashMeta(data); if e!=nil { return 0,0,false,e }
	mask:=slots-1
	start:=int(hashField64(field))&mask
	for probe:=0;probe<slots;probe++ {
		slot=(start+probe)&mask
		raw:=binary.LittleEndian.Uint32(data[indexedHashFixed+slot*4:indexedHashFixed+slot*4+4])
		if raw==0 { return slot,0,false,nil }
		pos=int(raw-1)
		f,_,_,e:=indexedHashRecord(data,pos); if e!=nil { return 0,0,false,e }
		if bytes.Equal(f,field) { return slot,pos,true,nil }
	}
	return 0,0,false,errors.New("invalid indexed hash table")
}

func indexedHashRecordBytes(field,value []byte) []byte {
	out:=make([]byte,0,len(field)+len(value)+20)
	out=appendHashUvarint(out,uint64(len(field)))
	out=appendHashUvarint(out,uint64(len(value)))
	out=append(out,field...)
	out=append(out,value...)
	return out
}

func nextHashPow2(n int) int { p:=8; for p<n { p<<=1 }; return p }

func encodeIndexedHash(pairs []HashPair) ([]byte,error) {
	if len(pairs)==0 { return nil,errors.New("invalid empty indexed hash") }
	slots:=nextHashPow2(len(pairs)*2)
	records:=make([][]byte,len(pairs))
	used:=0
	for i,p:=range pairs {
		if p.ExpiresAtMS!=0 { return nil,errors.New("indexed hash does not support field expiry") }
		records[i]=indexedHashRecordBytes(p.Field,p.Value); used+=len(records[i])
	}
	dataCap:=nextHashPow2(used*2)
	total:=indexedHashFixed+slots*4+dataCap
	if total>maxPackedHashBytes { return nil,errors.New("ERR hash exceeds 32 MiB limit") }
	out:=make([]byte,total)
	copy(out[:3],indexedHashHeader[:])
	binary.LittleEndian.PutUint32(out[3:7],uint32(len(pairs)))
	binary.LittleEndian.PutUint32(out[7:11],uint32(slots))
	start:=indexedHashFixed+slots*4
	cursor:=0
	for i,p:=range pairs {
		slot,_,found,err:=indexedHashFind(out,p.Field); if err!=nil { return nil,err }; if found { return nil,errors.New("duplicate hash field") }
		copy(out[start+cursor:],records[i])
		binary.LittleEndian.PutUint32(out[indexedHashFixed+slot*4:indexedHashFixed+slot*4+4],uint32(cursor+1))
		cursor+=len(records[i])
		binary.LittleEndian.PutUint32(out[11:15],uint32(cursor))
	}
	return out,nil
}

func decodeIndexedHash(data []byte) ([]HashPair,error) {
	count,slots,_,_,err:=indexedHashMeta(data); if err!=nil { return nil,err }
	pairs:=make([]HashPair,0,count)
	for slot:=0;slot<slots;slot++ {
		raw:=binary.LittleEndian.Uint32(data[indexedHashFixed+slot*4:indexedHashFixed+slot*4+4])
		if raw==0 { continue }
		f,v,_,e:=indexedHashRecord(data,int(raw-1)); if e!=nil { return nil,e }
		pairs=append(pairs,HashPair{Field:append([]byte(nil),f...),Value:append([]byte(nil),v...)})
	}
	if len(pairs)!=count { return nil,errors.New("invalid indexed hash count") }
	sort.Slice(pairs,func(i,j int)bool{return bytes.Compare(pairs[i].Field,pairs[j].Field)<0})
	return pairs,nil
}

func indexedHashLookup(data,target []byte)([]byte,bool,error){
	_,pos,found,err:=indexedHashFind(data,target); if err!=nil||!found { return nil,found,err }
	_,v,_,err:=indexedHashRecord(data,pos); if err!=nil{return nil,false,err}
	return append([]byte(nil),v...),true,nil
}

// indexedHashSet mutates data in place when its reserved table/data capacity can
// absorb the update. If it cannot, rebuilt is returned for publication.
func indexedHashSet(data []byte, fields, values [][]byte)(added int64, rebuilt []byte, err error){
	count,slots,used,start,err:=indexedHashMeta(data); if err!=nil{return 0,nil,err}
	for i,field:=range fields {
		slot,_,found,e:=indexedHashFind(data,field); if e!=nil{return added,nil,e}
		rec:=indexedHashRecordBytes(field,values[i])
		needRehash:=!found && (count+1)*10 >= slots*7
		if needRehash || start+used+len(rec)>len(data) {
			pairs,e:=decodeIndexedHash(data); if e!=nil{return added,nil,e}
			idx:=sort.Search(len(pairs),func(j int)bool{return bytes.Compare(pairs[j].Field,field)>=0})
			if idx<len(pairs)&&bytes.Equal(pairs[idx].Field,field){pairs[idx].Value=append([]byte(nil),values[i]...)}else{
				pairs=append(pairs,HashPair{});copy(pairs[idx+1:],pairs[idx:]);pairs[idx]=HashPair{Field:append([]byte(nil),field...),Value:append([]byte(nil),values[i]...)};added++
			}
			for j:=i+1;j<len(fields);j++ {
				k:=sort.Search(len(pairs),func(x int)bool{return bytes.Compare(pairs[x].Field,fields[j])>=0})
				if k<len(pairs)&&bytes.Equal(pairs[k].Field,fields[j]){pairs[k].Value=append([]byte(nil),values[j]...)}else{pairs=append(pairs,HashPair{});copy(pairs[k+1:],pairs[k:]);pairs[k]=HashPair{Field:append([]byte(nil),fields[j]...),Value:append([]byte(nil),values[j]...)};added++}
			}
			rebuilt,e=encodeIndexedHash(pairs);return added,rebuilt,e
		}
		copy(data[start+used:],rec)
		binary.LittleEndian.PutUint32(data[indexedHashFixed+slot*4:indexedHashFixed+slot*4+4],uint32(used+1))
		used+=len(rec)
		if !found { count++;added++ }
		binary.LittleEndian.PutUint32(data[3:7],uint32(count))
		binary.LittleEndian.PutUint32(data[11:15],uint32(used))
	}
	return added,nil,nil
}
