package index

import (
	"encoding/binary"
	"unsafe"
)

// Packed key records.
//
// A raw record is uvarint(len) followed by the key bytes, so its first byte is
// never 0x00 except for the empty key, which a raw record would write as a
// single zero byte. Byte 0x00 therefore introduces an escaped record:
//
//	0x00 flags [plen prefix] nbytes [mask] payload
//
// flags bits 0-1 give the letter case of the hex digits (0 lower, 1 upper,
// 2 mixed), bit 2 says the digits follow a literal "0x", and bit 3 says a raw
// prefix precedes them. The value 0xFF marks the empty key. nbytes is a
// uvarint count of payload bytes (two hex digits each). For mixed case, mask
// holds one bit per digit, set for upper-case letters.
//
// A key is packed only when its tail is a hex string of 16 to 128 digits that
// re-encodes to exactly the original bytes, and only when the escaped record is
// smaller than the raw one. Anything else stays raw, so lookups of other keys
// pay one byte comparison.
const (
	packEscape = 0x00

	packCaseMask = 0x03
	packCaseLow  = 0
	packCaseUp   = 1
	packCaseMix  = 2
	pack0x       = 0x04
	packPrefix   = 0x08
	packEmpty    = 0xFF

	packMinDigits = 16
	packMaxDigits = 128
	packMaxPrefix = 255

	// packMaxKey bounds the decoded length of a packed key.
	packMaxKey = packMaxPrefix + 2 + packMaxDigits
)

var hexNibble = func() (t [256]int8) {
	for i := range t {
		t[i] = -1
	}
	for c := '0'; c <= '9'; c++ {
		t[c] = int8(c - '0')
	}
	for c := 'a'; c <= 'f'; c++ {
		t[c] = int8(c-'a') + 10
	}
	for c := 'A'; c <= 'F'; c++ {
		t[c] = int8(c-'A') + 10
	}
	return
}()

var hexPairLower, hexPairUpper = func() (lo, up [256][2]byte) {
	const l, u = "0123456789abcdef", "0123456789ABCDEF"
	for i := 0; i < 256; i++ {
		lo[i] = [2]byte{l[i>>4], l[i&15]}
		up[i] = [2]byte{u[i>>4], u[i&15]}
	}
	return
}()

var hexWordLower, hexWordUpper = func() (lo, up [256]uint16) {
	for i := 0; i < 256; i++ {
		lo[i] = uint16(hexPairLower[i][0]) | uint16(hexPairLower[i][1])<<8
		up[i] = uint16(hexPairUpper[i][0]) | uint16(hexPairUpper[i][1])<<8
	}
	return
}()

// hexClass marks each byte as a digit (1), lower-case letter (2), upper-case
// letter (4), or not a hex digit (0).
var hexClass = func() (t [256]uint8) {
	for c := '0'; c <= '9'; c++ {
		t[c] = 1
	}
	for c := 'a'; c <= 'f'; c++ {
		t[c] = 2
	}
	for c := 'A'; c <= 'F'; c++ {
		t[c] = 4
	}
	return
}()

// packedKey is the analysis of a key that can be stored in escaped form.
type packedKey struct {
	flags  byte
	prefix string
	digits string
	size   int
}

// packKey reports whether key has a packable hex tail and its escaped form.
func packKey(key string) (p packedKey, ok bool) {
	n := len(key)
	if n < packMinDigits {
		return p, false
	}
	// Longest hex run at the end of the key, noting which classes it holds.
	s := n
	var seen uint8
	for s > 0 {
		c := hexClass[key[s-1]]
		if c == 0 {
			break
		}
		seen |= c
		s--
	}
	if n-s < packMinDigits {
		return p, false
	}
	if (n-s)&1 == 1 {
		s++
		seen = 0
		for i := s; i < n; i++ {
			seen |= hexClass[key[i]]
		}
	}
	digits := key[s:]
	if len(digits) > packMaxDigits {
		return p, false
	}
	end := s
	var flags byte
	if end >= 2 && key[end-2] == '0' && key[end-1] == 'x' {
		flags |= pack0x
		end -= 2
	}
	if end > packMaxPrefix {
		return p, false
	}
	prefixLen := end
	if prefixLen > 0 {
		flags |= packPrefix
	}
	switch {
	case seen&2 != 0 && seen&4 != 0:
		flags |= packCaseMix
	case seen&4 != 0:
		flags |= packCaseUp
	}
	nbytes := len(digits) / 2
	size := 2 + uvarintLen(nbytes) + nbytes
	if prefixLen > 0 {
		size += 1 + prefixLen
	}
	if flags&packCaseMask == packCaseMix {
		size += (len(digits) + 7) / 8
	}
	if size >= recordBytes(n) {
		return p, false
	}
	return packedKey{flags: flags, prefix: key[:prefixLen], digits: digits, size: size}, true
}

// KeyRecordSize is the key-log space the given key occupies, whichever form it
// is stored in.
func KeyRecordSize(key string) int {
	if len(key) == 0 {
		return 2
	}
	if p, ok := packKey(key); ok {
		return p.size
	}
	return recordBytes(len(key))
}

// PackedKeySize reports the key-log space key occupies when it is stored in
// packed form, and false when it is stored raw.
func PackedKeySize(key string) (int, bool) {
	if p, ok := packKey(key); ok {
		return p.size, true
	}
	return 0, false
}

func (p *packedKey) appendTo(dst []byte) []byte {
	dst = append(dst, packEscape, p.flags)
	if p.flags&packPrefix != 0 {
		dst = append(dst, byte(len(p.prefix)))
		dst = append(dst, p.prefix...)
	}
	nbytes := len(p.digits) / 2
	dst = binary.AppendUvarint(dst, uint64(nbytes))
	if p.flags&packCaseMask == packCaseMix {
		mask := (len(p.digits) + 7) / 8
		at := len(dst)
		dst = append(dst, make([]byte, mask)...)
		for i := 0; i < len(p.digits); i++ {
			if hexClass[p.digits[i]] == 4 {
				dst[at+i>>3] |= 1 << (i & 7)
			}
		}
	}
	at := len(dst)
	dst = append(dst, make([]byte, nbytes)...)
	out := dst[at:]
	d := p.digits
	for i := range out {
		out[i] = byte(hexNibble[d[2*i]])<<4 | byte(hexNibble[d[2*i+1]])
	}
	return dst
}

// packedParts splits an escaped record that starts at rec[0].
type packedParts struct {
	flags   byte
	prefix  []byte
	mask    []byte
	payload []byte
	total   int
}

func (p *packedParts) parse(rec []byte) {
	p.flags = rec[1]
	p.prefix, p.mask, p.payload = nil, nil, nil
	if p.flags == packEmpty {
		p.total = 2
		return
	}
	i := 2
	if p.flags&packPrefix != 0 {
		n := int(rec[i])
		p.prefix = rec[i+1 : i+1+n]
		i += 1 + n
	}
	nbytes, w := binary.Uvarint(rec[i:])
	i += w
	if p.flags&packCaseMask == packCaseMix {
		m := (int(nbytes)*2 + 7) / 8
		p.mask = rec[i : i+m]
		i += m
	}
	p.payload = rec[i : i+int(nbytes)]
	p.total = i + int(nbytes)
}

// recordLen is the total byte length of the record at log[offset:].
func recordLen(log []byte, offset uint32) int {
	b := log[offset:]
	if b[0] == packEscape {
		var parts packedParts
		parts.parse(b)
		return parts.total
	}
	if b[0] < 0x80 {
		return 1 + int(b[0])
	}
	v, w := binary.Uvarint(b)
	return w + int(v)
}

// appendKey appends the decoded key to dst.
func (p *packedParts) appendKey(dst []byte) []byte {
	if p.flags == packEmpty {
		return dst
	}
	dst = append(dst, p.prefix...)
	if p.flags&pack0x != 0 {
		dst = append(dst, '0', 'x')
	}
	at := len(dst)
	n := 2 * len(p.payload)
	if cap(dst)-at < n {
		dst = append(dst, make([]byte, n)...)
	} else {
		dst = dst[:at+n]
	}
	out := dst[at:]
	table := &hexPairLower
	if p.flags&packCaseMask == packCaseUp {
		table = &hexPairUpper
	}
	for i, b := range p.payload {
		pair := table[b]
		out[2*i], out[2*i+1] = pair[0], pair[1]
	}
	if p.flags&packCaseMask == packCaseMix {
		// Digits were written in lower case; the flagged letters are raised by
		// clearing bit 5. Two digits share each pair of mask bits.
		for j := range p.payload {
			bits := p.mask[j>>2] >> (uint(j&3) * 2) & 3
			out[2*j] -= (bits & 1) << 5
			out[2*j+1] -= (bits >> 1) << 5
		}
	}
	return dst
}

// equals compares the decoded key with key without allocating.
func (p *packedParts) equals(key string) bool {
	if p.flags == packEmpty {
		return len(key) == 0
	}
	n := len(p.prefix) + 2*len(p.payload)
	if p.flags&pack0x != 0 {
		n += 2
	}
	if n != len(key) {
		return false
	}
	// One table load and one compare per payload byte. Digits are held in lower
	// case; a flagged letter is lowered by clearing bit 5 of its character.
	table := &hexWordLower
	if p.flags&packCaseMask == packCaseUp {
		table = &hexWordUpper
	}
	i := len(p.prefix)
	if string(p.prefix) != key[:i] {
		return false
	}
	if p.flags&pack0x != 0 {
		if key[i] != '0' || key[i+1] != 'x' {
			return false
		}
		i += 2
	}
	rest := key[i:]
	mixed := p.flags&packCaseMask == packCaseMix
	for j, b := range p.payload {
		want := table[b]
		if mixed {
			bits := p.mask[j>>2] >> (uint(j&3) * 2) & 3
			want -= uint16(bits&1)*0x20 + uint16(bits>>1)<<13
		}
		if uint16(rest[2*j])|uint16(rest[2*j+1])<<8 != want {
			return false
		}
	}
	return true
}

// keyChunk hands out decoded keys from shared backing arrays, so iterating many
// packed keys costs one allocation per chunk instead of one per key. A chunk
// is never written after a key is cut from it, so earlier keys stay valid when
// a later chunk replaces it.
type keyChunk struct{ buf []byte }

const keyChunkSize = 8 << 10

func (c *keyChunk) decode(rec []byte) string {
	var parts packedParts
	parts.parse(rec)
	if parts.flags == packEmpty {
		return ""
	}
	n := len(parts.prefix) + 2*len(parts.payload)
	if parts.flags&pack0x != 0 {
		n += 2
	}
	if cap(c.buf)-len(c.buf) < n {
		size := keyChunkSize
		if n > size {
			size = n
		}
		c.buf = make([]byte, 0, size)
	}
	start := len(c.buf)
	c.buf = parts.appendKey(c.buf)
	return unsafe.String(&c.buf[start], n)
}

// keyLen is the decoded key length of the record described by p.
func (p *packedParts) keyLen() int {
	if p.flags == packEmpty {
		return 0
	}
	n := len(p.prefix) + 2*len(p.payload)
	if p.flags&pack0x != 0 {
		n += 2
	}
	return n
}
