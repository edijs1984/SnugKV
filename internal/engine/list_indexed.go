package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var indexedListHeader = [...]byte{'S','L',2}
const indexedListFixed = 15
const indexedListPromoteElements = 32

func isIndexedList(data []byte) bool {
	return len(data) >= indexedListFixed && bytes.Equal(data[:3], indexedListHeader[:])
}

func indexedListMeta(data []byte) (count, capacity, used, dataStart int, err error) {
	if !isIndexedList(data) {
		return 0,0,0,0,errors.New("invalid indexed list")
	}
	count = int(binary.LittleEndian.Uint32(data[3:7]))
	capacity = int(binary.LittleEndian.Uint32(data[7:11]))
	used = int(binary.LittleEndian.Uint32(data[11:15]))
	if capacity < indexedListPromoteElements || count < 0 || count > capacity {
		return 0,0,0,0,errors.New("invalid indexed list")
	}
	dataStart = indexedListFixed + capacity*4
	if dataStart > len(data) || used < 0 || dataStart+used > len(data) {
		return 0,0,0,0,errors.New("invalid indexed list")
	}
	return
}

func nextListPow2(n int) int {
	p := indexedListPromoteElements
	for p < n {
		p <<= 1
	}
	return p
}

func indexedListRecord(data []byte, pos int) (value []byte, end int, err error) {
	_,_,used,start,e := indexedListMeta(data)
	if e != nil {
		return nil,0,e
	}
	if pos < 0 || pos >= used {
		return nil,0,errors.New("invalid indexed list offset")
	}
	off := start + pos
	length,e := readListUvarint(data,&off)
	if e != nil || length > uint64(start+used-off) {
		return nil,0,errors.New("invalid indexed list element")
	}
	valueEnd := off + int(length)
	return data[off:valueEnd], valueEnd-start, nil
}

func indexedListElement(data []byte, index int) ([]byte,bool,error) {
	count,capacity,_,_,err := indexedListMeta(data)
	if err != nil {
		return nil,false,err
	}
	if index < 0 {
		index += count
	}
	if index < 0 || index >= count {
		return nil,false,nil
	}
	raw := binary.LittleEndian.Uint32(data[indexedListFixed+index*4:indexedListFixed+index*4+4])
	if raw == 0 {
		return nil,false,errors.New("invalid indexed list index")
	}
	value,_,err := indexedListRecord(data,int(raw-1))
	if err != nil {
		return nil,false,err
	}
	_ = capacity
	return append([]byte(nil), value...),true,nil
}

func indexedListRecordBytes(value []byte) []byte {
	out := make([]byte,0,len(value)+binary.MaxVarintLen64)
	out = appendListUvarint(out,uint64(len(value)))
	out = append(out,value...)
	return out
}

func encodeIndexedList(elements [][]byte) ([]byte,error) {
	if len(elements) == 0 {
		return nil,errors.New("invalid empty indexed list")
	}
	capacity := nextListPow2(len(elements)*2)
	records := make([][]byte,len(elements))
	used := 0
	for i,element := range elements {
		records[i] = indexedListRecordBytes(element)
		used += len(records[i])
	}
	dataCap := used + used/4
	if dataCap-used < 512 {
		dataCap = used + 512
	}
	total := indexedListFixed + capacity*4 + dataCap
	if total > maxPackedListBytes {
		return nil,errors.New("ERR list exceeds 32 MiB limit")
	}
	out := make([]byte,total)
	copy(out[:3],indexedListHeader[:])
	binary.LittleEndian.PutUint32(out[3:7],uint32(len(elements)))
	binary.LittleEndian.PutUint32(out[7:11],uint32(capacity))
	start := indexedListFixed + capacity*4
	cursor := 0
	for i,rec := range records {
		copy(out[start+cursor:],rec)
		binary.LittleEndian.PutUint32(out[indexedListFixed+i*4:indexedListFixed+i*4+4],uint32(cursor+1))
		cursor += len(rec)
	}
	binary.LittleEndian.PutUint32(out[11:15],uint32(cursor))
	return out,nil
}

func decodeIndexedList(data []byte) ([][]byte,error) {
	count,_,_,_,err := indexedListMeta(data)
	if err != nil {
		return nil,err
	}
	out := make([][]byte,count)
	for i:=0;i<count;i++ {
		value,found,err := indexedListElement(data,i)
		if err != nil {
			return nil,err
		}
		if !found {
			return nil,errors.New("invalid indexed list")
		}
		out[i] = value
	}
	return out,nil
}

// indexedListAppend mutates the current allocation in place when both offset
// capacity and payload headroom are sufficient. Otherwise it returns a rebuilt
// indexed representation for normal publication.
func indexedListAppend(data []byte, values [][]byte) (newCount int, rebuilt []byte, err error) {
	count,capacity,used,start,err := indexedListMeta(data)
	if err != nil {
		return 0,nil,err
	}
	records := make([][]byte,len(values))
	extra := 0
	for i,value := range values {
		records[i] = indexedListRecordBytes(value)
		extra += len(records[i])
	}
	if count+len(values) > capacity || start+used+extra > len(data) {
		elements,err := decodeIndexedList(data)
		if err != nil {
			return 0,nil,err
		}
		for _,value := range values {
			elements = append(elements,append([]byte(nil),value...))
		}
		rebuilt,err = encodeIndexedList(elements)
		if err != nil {
			return 0,nil,err
		}
		return len(elements),rebuilt,nil
	}

	cursor := used
	for i,rec := range records {
		copy(data[start+cursor:],rec)
		idx := count+i
		binary.LittleEndian.PutUint32(data[indexedListFixed+idx*4:indexedListFixed+idx*4+4],uint32(cursor+1))
		cursor += len(rec)
	}
	count += len(values)
	binary.LittleEndian.PutUint32(data[3:7],uint32(count))
	binary.LittleEndian.PutUint32(data[11:15],uint32(cursor))
	return count,nil,nil
}
