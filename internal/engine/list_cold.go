package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// Cold list layout. An idle indexed list keeps a per-element offset table and
// append headroom so pushes and pops stay cheap, but a list nobody writes does
// not need either. The cold layout stores the records back to back and keeps a
// skip table with one offset for every coldListStride elements, so an element
// is found by one table read and at most coldListStride-1 record skips.
//
//	[0:3]   'S','L',4
//	[3:7]   element count (uint32)
//	[7:11]  payload bytes (uint32)
//	[11:]   skip table: ceil(count/stride) offsets into the payload, 16-bit when
//	        the payload is under 64 KiB and 32-bit otherwise
//	        payload: records, each a uvarint length followed by the bytes
//
// The layout is only written by compaction. Any write to the list decodes it
// and stores the result in the regular packed or indexed layout, so none of the
// mutation paths know about it.
var coldListHeader = [...]byte{'S', 'L', 4}

const (
	coldListFixed       = 11
	coldListStride      = 8
	coldListMinElements = indexedListPromoteElements
)

func isColdList(data []byte) bool {
	return len(data) >= coldListFixed && bytes.Equal(data[:3], coldListHeader[:])
}

func coldListTableWidth(payload int) int {
	if payload < 1<<16 {
		return 2
	}
	return 4
}

func coldListTableEntries(count int) int {
	return (count + coldListStride - 1) / coldListStride
}

func encodeColdList(elements [][]byte) ([]byte, error) {
	payload := 0
	for _, element := range elements {
		payload += listUvarintLen(uint64(len(element))) + len(element)
	}
	width := coldListTableWidth(payload)
	table := coldListTableEntries(len(elements)) * width
	total := coldListFixed + table + payload
	if total > maxPackedListBytes {
		return nil, errors.New("ERR list exceeds 32 MiB limit")
	}
	out := make([]byte, coldListFixed+table, total)
	copy(out[:3], coldListHeader[:])
	binary.LittleEndian.PutUint32(out[3:7], uint32(len(elements)))
	binary.LittleEndian.PutUint32(out[7:11], uint32(payload))
	cursor := 0
	for i, element := range elements {
		if i%coldListStride == 0 {
			putListOffset(out[coldListFixed:], width, i/coldListStride, uint32(cursor))
		}
		out = appendListUvarint(out, uint64(len(element)))
		out = append(out, element...)
		cursor += listUvarintLen(uint64(len(element))) + len(element)
	}
	return out, nil
}

func coldListMeta(data []byte) (count, payload, width, dataStart int, err error) {
	if !isColdList(data) {
		return 0, 0, 0, 0, errors.New("invalid cold list")
	}
	count = int(binary.LittleEndian.Uint32(data[3:7]))
	payload = int(binary.LittleEndian.Uint32(data[7:11]))
	width = coldListTableWidth(payload)
	dataStart = coldListFixed + coldListTableEntries(count)*width
	if count < 0 || payload < 0 || dataStart+payload != len(data) {
		return 0, 0, 0, 0, errors.New("invalid cold list")
	}
	return count, payload, width, dataStart, nil
}

// coldListSeek returns the payload offset of the record at index.
func coldListSeek(data []byte, index, width, dataStart, payload int) (int, error) {
	pos := int(listOffset(data[coldListFixed:], width, index/coldListStride))
	for skip := index % coldListStride; skip > 0; skip-- {
		length, n := binary.Uvarint(data[dataStart+pos : dataStart+payload])
		if n <= 0 || length > uint64(payload-pos-n) {
			return 0, errors.New("invalid cold list record")
		}
		pos += n + int(length)
	}
	if pos < 0 || pos >= payload {
		return 0, errors.New("invalid cold list offset")
	}
	return pos, nil
}

func coldListRecord(data []byte, pos, dataStart, payload int) (value []byte, next int, err error) {
	length, n := binary.Uvarint(data[dataStart+pos : dataStart+payload])
	if n <= 0 || length > uint64(payload-pos-n) {
		return nil, 0, errors.New("invalid cold list record")
	}
	begin := dataStart + pos + n
	return data[begin : begin+int(length)], pos + n + int(length), nil
}

func coldListElement(data []byte, index int) ([]byte, bool, error) {
	count, payload, width, dataStart, err := coldListMeta(data)
	if err != nil {
		return nil, false, err
	}
	if index < 0 {
		index += count
	}
	if index < 0 || index >= count {
		return nil, false, nil
	}
	pos, err := coldListSeek(data, index, width, dataStart, payload)
	if err != nil {
		return nil, false, err
	}
	value, _, err := coldListRecord(data, pos, dataStart, payload)
	if err != nil {
		return nil, false, err
	}
	return append([]byte(nil), value...), true, nil
}

func coldListRange(data []byte, startIdx, stopIdx int64) ([][]byte, error) {
	count, payload, width, dataStart, err := coldListMeta(data)
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
	pos, err := coldListSeek(data, int(startIdx), width, dataStart, payload)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, stopIdx-startIdx+1)
	for i := startIdx; i <= stopIdx; i++ {
		var value []byte
		value, pos, err = coldListRecord(data, pos, dataStart, payload)
		if err != nil {
			return nil, err
		}
		out = append(out, append([]byte(nil), value...))
	}
	return out, nil
}

func decodeColdList(data []byte) ([][]byte, error) {
	count, payload, _, dataStart, err := coldListMeta(data)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, count)
	pos := 0
	for i := 0; i < count; i++ {
		var value []byte
		value, pos, err = coldListRecord(data, pos, dataStart, payload)
		if err != nil {
			return nil, err
		}
		out = append(out, append([]byte(nil), value...))
	}
	if pos != payload {
		return nil, errors.New("invalid cold list trailing data")
	}
	return out, nil
}

// coldFromIndexedList converts an indexed list to the cold layout.
func coldFromIndexedList(data []byte) ([]byte, bool) {
	count, _, _, _, err := indexedListMeta(data)
	if err != nil || count < coldListMinElements {
		return nil, false
	}
	elements, err := decodeIndexedList(data)
	if err != nil {
		return nil, false
	}
	cold, err := encodeColdList(elements)
	if err != nil {
		return nil, false
	}
	return cold, true
}
