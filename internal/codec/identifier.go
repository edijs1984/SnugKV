package codec

import (
	"bytes"
	"errors"
)

// Blockchain identifier codecs.
//
// Applications store hashes, addresses, public keys, signatures and token
// balances as text: 0x-prefixed hex (Ethereum and other EVM chains), base58
// (Solana public keys and signatures) and long decimal integers (uint256
// balances). Each of these is a fixed-width binary value written in a larger
// alphabet, so a lossless binary form is 25-50% smaller.
//
// All three are exact: Encode accepts only text that decodes back to the same
// bytes, and EncodeIdentifier re-checks that before it returns a record.
const (
	Hex        ID = 13
	Base58     ID = 14
	BigDecimal ID = 15
)

const (
	// hexMinDigits is the shortest hex string worth a codec. Shorter text saves
	// too little to matter and collides with colours, short ids and numbers.
	hexMinDigits = 32
	// hexMaxDigits bounds the work done on a foreground write; longer hex blobs
	// are left to general compression.
	hexMaxDigits = 4096

	bigDecimalMinLen = 20
	bigDecimalMaxLen = 78 // 2^256 - 1 has 78 digits
	bigDecimalMaxBit = 256

	hexFlagPrefix  = 0x01
	hexCaseShift   = 1
	hexCaseMask    = 0x03 << hexCaseShift
	hexCaseLower   = 0
	hexCaseUpper   = 1
	hexCaseMixed   = 2
	hexKnownFlags  = hexFlagPrefix | hexCaseMask
	base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
)

// EncodeHexForeground is the only identifier encoder used on the write path:
// hex conversion is a single table lookup per byte. Base58 and big decimal
// conversion cost microseconds, so the background optimizer applies them.
func (r *Registry) EncodeHexForeground(src []byte) (Record, bool) {
	n := len(src)
	if n < hexMinDigits || n > hexMaxDigits+2 {
		return Record{}, false
	}
	c := hexCodec{}
	data, ok := c.Encode(src)
	if !ok || len(data) >= n {
		return Record{}, false
	}
	// A plain run of decimal digits is better served by the big decimal codec.
	if n <= bigDecimalMaxLen && data[0]&hexFlagPrefix == 0 && allDigits(src) {
		return Record{}, false
	}
	decoded, err := c.DecodeInto(data, n, nil)
	if err != nil || !bytes.Equal(decoded, src) {
		return Record{}, false
	}
	return Record{ID: Hex, RawLength: n, Data: data}, true
}

// IsIdentifierCodec reports whether id is one of the identifier codecs.
func IsIdentifierCodec(id ID) bool { return id == Hex || id == Base58 || id == BigDecimal }

// LooksLikeIdentifier is a constant-time filter for values the optimizer
// should examine for base58 or big decimal encoding: 20 to 78 bytes whose first,
// middle and last bytes are letters or digits. It admits some plain text; the
// codecs reject that in a few bytes.
func LooksLikeIdentifier(src []byte) bool {
	n := len(src)
	if n < bigDecimalMinLen || n > bigDecimalMaxLen {
		return false
	}
	return alnum(src[0]) && alnum(src[n/2]) && alnum(src[n-1])
}

func allDigits(src []byte) bool {
	for _, c := range src {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func alnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// EncodeIdentifier returns the smallest blockchain-identifier representation
// of src, or false when src is not one. It is cheap to call on arbitrary
// values: length and the first bytes reject almost everything immediately.
func (r *Registry) EncodeIdentifier(src []byte) (Record, bool) {
	n := len(src)
	if n < bigDecimalMinLen || n > hexMaxDigits+2 {
		return Record{}, false
	}
	// Every identifier form starts with a digit, a letter or the 0x prefix.
	first := src[0]
	if !(first >= '0' && first <= '9') && !(first >= 'a' && first <= 'z') && !(first >= 'A' && first <= 'Z') {
		return Record{}, false
	}

	var best Record
	found := false
	consider := func(id ID, c Codec) {
		data, ok := c.Encode(src)
		if !ok || len(data) >= n {
			return
		}
		if found && len(data) >= len(best.Data) {
			return
		}
		decoded, err := c.Decode(data, n)
		if err != nil || !bytes.Equal(decoded, src) {
			return
		}
		best = Record{ID: id, RawLength: n, Data: data}
		found = true
	}

	if n <= bigDecimalMaxLen {
		consider(BigDecimal, bigDecimalCodec{})
	}
	if n >= hexMinDigits {
		consider(Hex, hexCodec{})
	}
	if n <= 44 {
		consider(Base58, base58Codec{})
	}
	return best, found
}

// ---- hex ----

type hexCodec struct{}

func (hexCodec) ID() ID       { return Hex }
func (hexCodec) Name() string { return "hex" }

func hexValue(b byte) (v byte, upper bool, ok bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', false, true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, false, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true, true
	}
	return 0, false, false
}

// Encode layout: one flag byte (bit 0: "0x" prefix; bits 1-2: case), the bytes
// the digits spell, and for mixed case one bit per digit (set = upper case).
func (hexCodec) Encode(src []byte) ([]byte, bool) {
	flags := byte(0)
	digits := src
	if len(digits) >= 2 && digits[0] == '0' && digits[1] == 'x' {
		flags |= hexFlagPrefix
		digits = digits[2:]
	}
	if len(digits) < hexMinDigits || len(digits) > hexMaxDigits || len(digits)%2 != 0 {
		return nil, false
	}

	sawUpper, sawLower := false, false
	out := make([]byte, 1, 1+len(digits)/2+(len(digits)+7)/8)
	for i := 0; i < len(digits); i += 2 {
		hi, hiUpper, ok := hexValue(digits[i])
		if !ok {
			return nil, false
		}
		lo, loUpper, ok := hexValue(digits[i+1])
		if !ok {
			return nil, false
		}
		out = append(out, hi<<4|lo)
		for k, up := range [2]bool{hiUpper, loUpper} {
			if up {
				sawUpper = true
			} else if c := digits[i+k]; c >= 'a' && c <= 'f' {
				sawLower = true
			}
		}
	}

	switch {
	case sawUpper && sawLower:
		flags |= hexCaseMixed << hexCaseShift
		mask := make([]byte, (len(digits)+7)/8)
		for i, c := range digits {
			if c >= 'A' && c <= 'F' {
				mask[i/8] |= 1 << (i % 8)
			}
		}
		out = append(out, mask...)
	case sawUpper:
		flags |= hexCaseUpper << hexCaseShift
	}
	out[0] = flags
	return out, true
}

func appendHexDigits(dst, body []byte, flags byte, mask []byte) []byte {
	lower := "0123456789abcdef"
	upper := "0123456789ABCDEF"
	caseMode := (flags & hexCaseMask) >> hexCaseShift
	pos := 0
	for _, b := range body {
		for _, nib := range [2]byte{b >> 4, b & 0x0f} {
			digits := lower
			switch caseMode {
			case hexCaseUpper:
				digits = upper
			case hexCaseMixed:
				if mask[pos/8]&(1<<(pos%8)) != 0 {
					digits = upper
				}
			}
			dst = append(dst, digits[nib])
			pos++
		}
	}
	return dst
}

func (hexCodec) Decode(src []byte, rawLength int) ([]byte, error) {
	return hexCodec{}.DecodeInto(src, rawLength, nil)
}

func (hexCodec) DecodeInto(src []byte, rawLength int, dst []byte) ([]byte, error) {
	if len(src) < 1 {
		return nil, errors.New("invalid hex payload")
	}
	flags := src[0]
	if flags&^byte(hexKnownFlags) != 0 {
		return nil, errors.New("unknown hex flags")
	}
	caseMode := (flags & hexCaseMask) >> hexCaseShift
	if caseMode > hexCaseMixed {
		return nil, errors.New("unknown hex case mode")
	}
	digits := rawLength
	prefix := 0
	if flags&hexFlagPrefix != 0 {
		prefix = 2
		digits -= 2
	}
	if digits < hexMinDigits || digits > hexMaxDigits || digits%2 != 0 {
		return nil, errors.New("invalid hex length")
	}
	body := src[1:]
	var mask []byte
	want := digits / 2
	if caseMode == hexCaseMixed {
		maskLen := (digits + 7) / 8
		if len(body) != want+maskLen {
			return nil, errors.New("invalid hex payload length")
		}
		mask = body[want:]
		body = body[:want]
	} else if len(body) != want {
		return nil, errors.New("invalid hex payload length")
	}

	if cap(dst) < rawLength {
		dst = make([]byte, 0, rawLength)
	} else {
		dst = dst[:0]
	}
	if prefix != 0 {
		dst = append(dst, '0', 'x')
	}
	return appendHexDigits(dst, body, flags, mask), nil
}

// ---- base58 (Bitcoin alphabet; Solana public keys and signatures) ----

type base58Codec struct{}

func (base58Codec) ID() ID       { return Base58 }
func (base58Codec) Name() string { return "base58" }

var base58Reverse = func() [256]int8 {
	var t [256]int8
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(base58Alphabet); i++ {
		t[base58Alphabet[i]] = int8(i)
	}
	return t
}()

// base58Size is the only decoded width worth a codec: a 32-byte public key or
// hash. A 64-byte signature saves about 13% of its row but costs an 88-digit
// conversion on every read, which measured as a poor trade.
func base58Size(n int) bool { return n == 32 }

func (base58Codec) Encode(src []byte) ([]byte, bool) {
	if len(src) < 32 || len(src) > 44 {
		return nil, false
	}
	out, ok := base58Decode(src)
	if !ok || !base58Size(len(out)) {
		return nil, false
	}
	return out, true
}

func (base58Codec) Decode(src []byte, rawLength int) ([]byte, error) {
	return base58Codec{}.DecodeInto(src, rawLength, nil)
}

func (base58Codec) DecodeInto(src []byte, rawLength int, dst []byte) ([]byte, error) {
	if !base58Size(len(src)) {
		return nil, errors.New("invalid base58 payload")
	}
	// A leading zero byte is a leading '1'; the rest is the magnitude.
	zeros := 0
	for zeros < len(src) && src[zeros] == 0 {
		zeros++
	}
	if cap(dst) < rawLength {
		dst = make([]byte, 0, rawLength)
	}
	dst = dst[:0]
	for i := 0; i < zeros; i++ {
		dst = append(dst, '1')
	}
	dst = radixAppend(dst, src[zeros:], 58, 5, base58Chunk, base58Alphabet)
	if len(dst) != rawLength {
		return nil, errors.New("base58 length mismatch")
	}
	return dst, nil
}

// radixDecode converts text in the given radix (digit values from digit) into
// big-endian bytes without leading zeros, working in 32-bit limbs and consuming
// `chunk` digits per multiply. It returns false for an invalid digit.
func radixDecode(text []byte, radix uint64, chunk int, digit func(byte) int) ([]byte, bool) {
	limbs := make([]uint32, 0, 24)
	for len(text) > 0 {
		n := chunk
		if n > len(text) {
			n = len(text)
		}
		var val, mult uint64 = 0, 1
		for _, c := range text[:n] {
			d := digit(c)
			if d < 0 {
				return nil, false
			}
			val = val*radix + uint64(d)
			mult *= radix
		}
		text = text[n:]
		// limbs = limbs*mult + val, little-endian limbs.
		carry := val
		for i := range limbs {
			cur := uint64(limbs[i])*mult + carry
			limbs[i] = uint32(cur)
			carry = cur >> 32
		}
		for carry != 0 {
			limbs = append(limbs, uint32(carry))
			carry >>= 32
		}
	}
	out := make([]byte, 0, len(limbs)*4)
	for i := len(limbs) - 1; i >= 0; i-- {
		out = append(out, byte(limbs[i]>>24), byte(limbs[i]>>16), byte(limbs[i]>>8), byte(limbs[i]))
	}
	skip := 0
	for skip < len(out) && out[skip] == 0 {
		skip++
	}
	return out[skip:], true
}

// radixEncode writes the big-endian magnitude of src (no leading-zero handling)
// in the given radix, dividing by radix^chunk per pass. The result has no
// leading zero digits, and is empty for a zero value.
func radixEncode(src []byte, radix uint64, chunk int, chunkDiv uint64, symbols string) []byte {
	return radixAppend(nil, src, radix, chunk, chunkDiv, symbols)
}

// radixAppend appends the digits of src to dst; see radixEncode.
func radixAppend(dst, src []byte, radix uint64, chunk int, chunkDiv uint64, symbols string) []byte {
	// big-endian 32-bit limbs
	n := (len(src) + 3) / 4
	var stack [24]uint32
	limbs := stack[:0]
	if n > len(stack) {
		limbs = make([]uint32, 0, n)
	}
	first := len(src) % 4
	i := 0
	if first != 0 {
		var v uint32
		for ; i < first; i++ {
			v = v<<8 | uint32(src[i])
		}
		limbs = append(limbs, v)
	}
	for ; i < len(src); i += 4 {
		limbs = append(limbs, uint32(src[i])<<24|uint32(src[i+1])<<16|uint32(src[i+2])<<8|uint32(src[i+3]))
	}
	for len(limbs) > 0 && limbs[0] == 0 {
		limbs = limbs[1:]
	}

	var outStack [128]byte
	out := outStack[:0] // least significant digit first
	for len(limbs) > 0 {
		var rem uint64
		for j := range limbs {
			cur := rem<<32 | uint64(limbs[j])
			limbs[j] = uint32(cur / chunkDiv)
			rem = cur % chunkDiv
		}
		for len(limbs) > 0 && limbs[0] == 0 {
			limbs = limbs[1:]
		}
		for k := 0; k < chunk; k++ {
			out = append(out, symbols[rem%radix])
			rem /= radix
		}
	}
	for len(out) > 0 && out[len(out)-1] == symbols[0] {
		out = out[:len(out)-1]
	}
	for i := len(out) - 1; i >= 0; i-- {
		dst = append(dst, out[i])
	}
	return dst
}

const base58Chunk = 58 * 58 * 58 * 58 * 58 // 656356768, fits 32 bits

func base58Digit(c byte) int { return int(base58Reverse[c]) }

// base58Decode returns false for any character outside the alphabet. A run of
// leading '1' characters maps to the same number of zero bytes.
func base58Decode(src []byte) ([]byte, bool) {
	zeros := 0
	for zeros < len(src) && src[zeros] == '1' {
		zeros++
	}
	body, ok := radixDecode(src[zeros:], 58, 5, base58Digit)
	if !ok {
		return nil, false
	}
	out := make([]byte, zeros+len(body))
	copy(out[zeros:], body)
	return out, true
}

func base58Encode(src []byte) []byte {
	zeros := 0
	for zeros < len(src) && src[zeros] == 0 {
		zeros++
	}
	body := radixEncode(src[zeros:], 58, 5, base58Chunk, base58Alphabet)
	out := make([]byte, zeros+len(body))
	for i := 0; i < zeros; i++ {
		out[i] = '1'
	}
	copy(out[zeros:], body)
	return out
}

// ---- large decimal integers (uint256 token balances) ----

type bigDecimalCodec struct{}

func (bigDecimalCodec) ID() ID       { return BigDecimal }
func (bigDecimalCodec) Name() string { return "bigdecimal" }

// Encode accepts canonical decimal text (digits only, no leading zero, no sign)
// of a value that needs more than 64 bits. Smaller values are handled by the
// integer codecs. The payload is the big-endian magnitude.
func decimalDigit(c byte) int {
	if c >= '0' && c <= '9' {
		return int(c - '0')
	}
	return -1
}

func (bigDecimalCodec) Encode(src []byte) ([]byte, bool) {
	if len(src) < bigDecimalMinLen || len(src) > bigDecimalMaxLen || src[0] == '0' {
		return nil, false
	}
	out, ok := radixDecode(src, 10, 9, decimalDigit)
	if !ok {
		return nil, false
	}
	// More than 64 bits and at most 256 bits is 9..32 bytes.
	if len(out) < 9 || len(out) > bigDecimalMaxBit/8 {
		return nil, false
	}
	return out, true
}

func (bigDecimalCodec) Decode(src []byte, rawLength int) ([]byte, error) {
	return bigDecimalCodec{}.DecodeInto(src, rawLength, nil)
}

func (bigDecimalCodec) DecodeInto(src []byte, rawLength int, dst []byte) ([]byte, error) {
	if len(src) < 9 || len(src) > bigDecimalMaxBit/8 || src[0] == 0 {
		return nil, errors.New("invalid big decimal payload")
	}
	if cap(dst) < rawLength {
		dst = make([]byte, 0, rawLength)
	}
	dst = radixAppend(dst[:0], src, 10, 9, 1_000_000_000, "0123456789")
	if len(dst) != rawLength {
		return nil, errors.New("big decimal length mismatch")
	}
	return dst, nil
}
