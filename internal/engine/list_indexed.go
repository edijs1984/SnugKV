package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var indexedListHeader = [...]byte{'S','L',2}

// indexedListHeaderNarrow marks a list whose offset table uses 16-bit entries.
// Payloads below 64 KiB can address every record with two bytes, which saves two
// bytes per element; larger lists keep 32-bit entries.
var indexedListHeaderNarrow = [...]byte{'S','L',3}

// indexedListNarrowMaxPayload is the largest payload (including headroom) a
// narrow list may reserve: stored offsets are record start + 1 and must fit in
// 16 bits.
const indexedListNarrowMaxPayload = 1<<16 - 2

func indexedListOffsetWidth(data []byte) int {
	if len(data) >= 3 && data[2] == indexedListHeaderNarrow[2] {
		return 2
	}
	return 4
}

// indexedListWidthFor picks the offset width for a payload reservation.
func indexedListWidthFor(dataCap int) int {
	if dataCap <= indexedListNarrowMaxPayload {
		return 2
	}
	return 4
}

func putIndexedListHeader(out []byte, width int) {
	if width == 2 {
		copy(out[:3], indexedListHeaderNarrow[:])
		return
	}
	copy(out[:3], indexedListHeader[:])
}

// listOffset returns the stored offset (record start + 1, 0 when unset) of
// element i.
func listOffset(table []byte, width, i int) uint32 {
	if width == 2 {
		return uint32(binary.LittleEndian.Uint16(table[i*2 : i*2+2]))
	}
	return binary.LittleEndian.Uint32(table[i*4 : i*4+4])
}

func putListOffset(table []byte, width, i int, v uint32) {
	if width == 2 {
		binary.LittleEndian.PutUint16(table[i*2:i*2+2], uint16(v))
		return
	}
	binary.LittleEndian.PutUint32(table[i*4:i*4+4], v)
}
const indexedListFixed = 15
const indexedListPromoteElements = 32

// indexedListMaxPayloadHeadroom caps the append reserve added when a list is
// regrown, so very large lists grow by a fixed step instead of doubling.
const indexedListMaxPayloadHeadroom = 1 << 20

// indexedListDoublingPayloadBytes is the payload size from which regrow headroom
// doubles. Below it the reserve stays at 25%: lists that end up small keep a tight
// footprint, while lists that keep growing stop leaving a long chain of dead copies.
const indexedListDoublingPayloadBytes = 8 << 10

func isIndexedList(data []byte) bool {
	return len(data) >= indexedListFixed &&
		(bytes.Equal(data[:3], indexedListHeader[:]) || bytes.Equal(data[:3], indexedListHeaderNarrow[:]))
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
	dataStart = indexedListFixed + capacity*indexedListOffsetWidth(data)
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
	raw := listOffset(data[indexedListFixed:], indexedListOffsetWidth(data), index)
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
	width := indexedListWidthFor(dataCap)
	total := indexedListFixed + capacity*width + dataCap
	if total > maxPackedListBytes {
		return nil,errors.New("ERR list exceeds 32 MiB limit")
	}
	out := make([]byte,total)
	putIndexedListHeader(out,width)
	binary.LittleEndian.PutUint32(out[3:7],uint32(len(elements)))
	binary.LittleEndian.PutUint32(out[7:11],uint32(capacity))
	start := indexedListFixed + capacity*width
	cursor := 0
	for i,rec := range records {
		copy(out[start+cursor:],rec)
		putListOffset(out[indexedListFixed:],width,i,uint32(cursor+1))
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

	// Single-value RPUSH is the dominant hot path. Write the varint length and
	// payload directly into reserved indexed-list space instead of allocating a
	// temporary record slice that is immediately copied.
	if len(values) == 1 {
		value := values[0]
		var lenBuf [binary.MaxVarintLen64]byte
		lenBytes := binary.PutUvarint(lenBuf[:], uint64(len(value)))
		extra := lenBytes + len(value)
		if count+1 <= capacity && start+used+extra <= len(data) {
			cursor := start + used
			copy(data[cursor:cursor+lenBytes], lenBuf[:lenBytes])
			cursor += lenBytes
			copy(data[cursor:cursor+len(value)], value)
			putListOffset(data[indexedListFixed:], indexedListOffsetWidth(data), count, uint32(used+1))
			count++
			used += extra
			binary.LittleEndian.PutUint32(data[3:7], uint32(count))
			binary.LittleEndian.PutUint32(data[11:15], uint32(used))
			return count,nil,nil
		}
	}

	extra := 0
	for _,value := range values {
		extra += listUvarintLen(uint64(len(value))) + len(value)
	}
	if count+len(values) > capacity || start+used+extra > len(data) {
		// Out of room: grow by copying the stored bytes verbatim. This avoids
		// decoding and re-encoding every element (one allocation each), which
		// made RPUSH cost grow with list length.
		grownCount,grown,ok,growErr := growIndexedListRaw(data,values)
		if growErr != nil {
			return 0,nil,growErr
		}
		if ok {
			return grownCount,grown,nil
		}
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
	for i,value := range values {
		recordStart := cursor
		cursor += binary.PutUvarint(data[start+cursor:],uint64(len(value)))
		cursor += copy(data[start+cursor:],value)
		idx := count+i
		putListOffset(data[indexedListFixed:],indexedListOffsetWidth(data),idx,uint32(recordStart+1))
	}
	count += len(values)
	binary.LittleEndian.PutUint32(data[3:7],uint32(count))
	binary.LittleEndian.PutUint32(data[11:15],uint32(cursor))
	return count,nil,nil
}

func listUvarintLen(n uint64) int {
	length := 1
	for n >= 0x80 {
		n >>= 7
		length++
	}
	return length
}

// indexedListFullyReferenced reports whether every payload byte is owned by a
// live element, i.e. the summed record sizes equal the used payload length. It
// walks the offset table without allocating. Any inconsistency returns false so
// callers fall back to the validating decode/encode path.
func indexedListFullyReferenced(data []byte, count, used, start int) bool {
	live := 0
	for i := 0; i < count; i++ {
		raw := listOffset(data[indexedListFixed:], indexedListOffsetWidth(data), i)
		if raw == 0 || int(raw-1) >= used {
			return false
		}
		off := start + int(raw-1)
		length, err := readListUvarint(data, &off)
		if err != nil || length > uint64(start+used-off) {
			return false
		}
		live += (off - (start + int(raw-1))) + int(length)
	}
	return live == used
}

// indexedListLiveBytes sums the record sizes of the live elements. Left pops
// leave dead records at the front of the payload, so live can be below used.
// ok is false on any inconsistency so callers fall back to the validating path.
func indexedListLiveBytes(data []byte, count, used, start int) (live int, ok bool) {
	for i := 0; i < count; i++ {
		raw := listOffset(data[indexedListFixed:], indexedListOffsetWidth(data), i)
		if raw == 0 || int(raw-1) >= used {
			return 0, false
		}
		off := start + int(raw-1)
		length, err := readListUvarint(data, &off)
		if err != nil || length > uint64(start+used-off) {
			return 0, false
		}
		live += (off - (start + int(raw-1))) + int(length)
	}
	return live, true
}

// copyIndexedListLive writes the live records of data, in list order, into
// payload (which must hold at least live bytes) and fills the matching offset
// table entries. It returns the bytes written.
func copyIndexedListLive(data []byte, count, start int, table []byte, tableWidth int, payload []byte) int {
	srcWidth := indexedListOffsetWidth(data)
	cursor := 0
	for i := 0; i < count; i++ {
		raw := int(listOffset(data[indexedListFixed:], srcWidth, i)) - 1
		off := start + raw
		rec := off
		length, _ := readListUvarint(data, &off)
		end := off + int(length)
		n := copy(payload[cursor:], data[rec:end])
		putListOffset(table, tableWidth, i, uint32(cursor+1))
		cursor += n
	}
	return cursor
}

// growIndexedListRaw returns a larger copy of an indexed list with values
// appended. The existing offset table and payload are copied verbatim, so the
// cost is a memmove rather than one decode and one re-encode allocation per
// element. ok is false when the list has unreferenced payload or fails
// validation; the caller then uses the compacting rebuild, which also reports
// any corruption error.
func growIndexedListRaw(data []byte, values [][]byte) (newCount int, grown []byte, ok bool, err error) {
	count, capacity, used, start, metaErr := indexedListMeta(data)
	if metaErr != nil {
		return 0, nil, false, nil
	}
	live, liveOK := indexedListLiveBytes(data, count, used, start)
	if !liveOK {
		return 0, nil, false, nil
	}

	extra := 0
	for _, value := range values {
		extra += listUvarintLen(uint64(len(value))) + len(value)
	}
	newCount = count + len(values)
	newCapacity := capacity
	if newCount > capacity {
		newCapacity = nextListPow2(newCount * 2)
	}
	newUsed := live + extra
	// Large lists double their payload reserve (capped, like Redis SDS
	// preallocation) so repeated RPUSH regrows O(log n) times. Each regrow leaves
	// a dead copy in the arena and a temporary heap buffer, so a small growth
	// factor multiplies both the post-load memory spike and GC churn. Small
	// lists keep a 25% reserve to stay tight. Idle lists give the reserve back
	// through trimIndexedList during compaction.
	headroom := newUsed
	if newUsed < indexedListDoublingPayloadBytes {
		headroom = newUsed / 4
	}
	if headroom > indexedListMaxPayloadHeadroom {
		headroom = indexedListMaxPayloadHeadroom
	}
	if headroom < 512 {
		headroom = 512
	}
	dataCap := newUsed + headroom
	newWidth := indexedListWidthFor(dataCap)
	oldWidth := indexedListOffsetWidth(data)
	newStart := indexedListFixed + newCapacity*newWidth
	total := newStart + dataCap
	if total > maxPackedListBytes {
		return 0, nil, false, errors.New("ERR list exceeds 32 MiB limit")
	}

	out := make([]byte, total)
	copy(out[:indexedListFixed], data[:indexedListFixed])
	putIndexedListHeader(out, newWidth)
	cursor := live
	if live == used {
		if newWidth == oldWidth {
			copy(out[indexedListFixed:indexedListFixed+count*newWidth], data[indexedListFixed:indexedListFixed+count*newWidth])
		} else {
			for i := 0; i < count; i++ {
				putListOffset(out[indexedListFixed:], newWidth, i, listOffset(data[indexedListFixed:], oldWidth, i))
			}
		}
		copy(out[newStart:newStart+used], data[start:start+used])
	} else {
		// Dead records (left pops) are dropped while copying.
		copyIndexedListLive(data, count, start, out[indexedListFixed:newStart], newWidth, out[newStart:newStart+live])
	}
	for i, value := range values {
		recordStart := cursor
		cursor += binary.PutUvarint(out[newStart+cursor:], uint64(len(value)))
		cursor += copy(out[newStart+cursor:], value)
		putListOffset(out[indexedListFixed:], newWidth, count+i, uint32(recordStart+1))
	}
	binary.LittleEndian.PutUint32(out[3:7], uint32(newCount))
	binary.LittleEndian.PutUint32(out[7:11], uint32(newCapacity))
	binary.LittleEndian.PutUint32(out[11:15], uint32(cursor))
	return newCount, out, true, nil
}

// trimIndexedList returns a copy of an indexed list sized exactly to its
// contents: the offset table keeps only max(count, promote threshold) slots and
// the payload keeps only the used bytes. Append headroom exists to make RPUSH
// cheap on hot lists; once a list is idle it is pure overhead. A later RPUSH
// simply regrows the list through growIndexedListRaw. ok is false when the list
// is invalid or trimming would not shrink it.
func trimIndexedList(data []byte) (trimmed []byte, ok bool) {
	count, _, used, start, err := indexedListMeta(data)
	if err != nil {
		return nil, false
	}
	live, liveOK := indexedListLiveBytes(data, count, used, start)
	if !liveOK {
		return nil, false
	}
	newCapacity := count
	if newCapacity < indexedListPromoteElements {
		newCapacity = indexedListPromoteElements
	}
	newWidth := indexedListWidthFor(live)
	oldWidth := indexedListOffsetWidth(data)
	newStart := indexedListFixed + newCapacity*newWidth
	total := newStart + live
	if total >= len(data) {
		return nil, false
	}
	out := make([]byte, total)
	copy(out[:indexedListFixed], data[:indexedListFixed])
	putIndexedListHeader(out, newWidth)
	if live == used {
		if newWidth == oldWidth {
			copy(out[indexedListFixed:indexedListFixed+count*newWidth], data[indexedListFixed:indexedListFixed+count*newWidth])
		} else {
			for i := 0; i < count; i++ {
				putListOffset(out[indexedListFixed:], newWidth, i, listOffset(data[indexedListFixed:], oldWidth, i))
			}
		}
		copy(out[newStart:], data[start:start+used])
	} else {
		copyIndexedListLive(data, count, start, out[indexedListFixed:newStart], newWidth, out[newStart:])
	}
	binary.LittleEndian.PutUint32(out[7:11], uint32(newCapacity))
	binary.LittleEndian.PutUint32(out[11:15], uint32(live))
	return out, true
}

// indexedListPopMinCount is the element count below which a pop falls back to
// the packed representation, so small lists stay tight and a list hovering
// around the promote threshold does not flip between formats.
const indexedListPopMinCount = indexedListPromoteElements / 2

// indexedListPop removes up to n elements from the left or right end in place
// and returns them in pop order. It never allocates a new list. ok is false
// when the pop would leave fewer than indexedListPopMinCount elements or the
// list fails validation; the caller then uses the generic path, which also
// reports corruption.
func indexedListPop(data []byte, n int, left bool) (out [][]byte, ok bool) {
	count, _, used, start, err := indexedListMeta(data)
	if err != nil || n <= 0 || n > count || count-n < indexedListPopMinCount {
		return nil, false
	}
	width := indexedListOffsetWidth(data)
	out = make([][]byte, n)
	var lo, hi int
	if left {
		lo, hi = 0, n
	} else {
		lo, hi = count-n, count
	}
	type span struct{ start, end int }
	spans := make([]span, 0, n)
	for i := lo; i < hi; i++ {
		raw := listOffset(data[indexedListFixed:], width, i)
		if raw == 0 || int(raw-1) >= used {
			return nil, false
		}
		value, end, e := indexedListRecord(data, int(raw-1))
		if e != nil {
			return nil, false
		}
		spans = append(spans, span{int(raw - 1), end})
		j := i - lo
		if !left {
			j = hi - 1 - i
		}
		out[j] = append([]byte(nil), value...)
	}
	newCount := count - n
	if left {
		table := data[indexedListFixed : indexedListFixed+count*width]
		copy(table, table[n*width:])
		clear(table[newCount*width:])
	} else {
		clear(data[indexedListFixed+newCount*width : indexedListFixed+count*width])
		// Right pops reclaim payload when the removed records are the tail.
		for i := len(spans) - 1; i >= 0 && spans[i].end == used; i-- {
			used = spans[i].start
		}
		binary.LittleEndian.PutUint32(data[11:15], uint32(used))
	}
	binary.LittleEndian.PutUint32(data[3:7], uint32(newCount))
	_ = start
	return out, true
}

// indexedListRange returns the elements in [start, stop] after Redis-style
// negative index normalisation, reading only those records.
func indexedListRange(data []byte, startIdx, stopIdx int64) ([][]byte, error) {
	count, _, used, start, err := indexedListMeta(data)
	if err != nil {
		return nil, err
	}
	n := int64(count)
	if startIdx < 0 {
		startIdx += n
	}
	if stopIdx < 0 {
		stopIdx += n
	}
	if startIdx < 0 {
		startIdx = 0
	}
	if stopIdx < 0 || startIdx >= n || startIdx > stopIdx {
		return nil, nil
	}
	if stopIdx >= n {
		stopIdx = n - 1
	}
	out := make([][]byte, 0, stopIdx-startIdx+1)
	for i := startIdx; i <= stopIdx; i++ {
		raw := listOffset(data[indexedListFixed:], indexedListOffsetWidth(data), int(i))
		if raw == 0 || int(raw-1) >= used {
			return nil, errors.New("invalid indexed list index")
		}
		value, _, e := indexedListRecord(data, int(raw-1))
		if e != nil {
			return nil, e
		}
		out = append(out, append([]byte(nil), value...))
	}
	_ = start
	return out, nil
}
