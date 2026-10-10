package codec

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
)

// SolanaTokenAccount stores an SPL Token account (165 bytes) in a compact
// exact form. The account is what getAccountInfo returns for every token
// holder: mint, owner, amount and a set of optional fields that are almost
// always absent. The fixed layout spends 165 bytes where about 70 carry
// information.
//
// The value can arrive as the 165 raw bytes or as the 220-character standard
// base64 text that the JSON-RPC "base64" encoding returns; both are accepted
// and both are rebuilt exactly.
const SolanaTokenAccount ID = 16

const (
	tokenAccountSize    = 165
	tokenAccountB64Size = 220 // 165 bytes, standard base64, no padding

	tokenFlagDelegate = 1 << 0
	tokenFlagNative   = 1 << 1
	tokenFlagClose    = 1 << 2
	tokenStateShift   = 3
	tokenStateMask    = 3 << tokenStateShift
	tokenKnownFlags   = tokenFlagDelegate | tokenFlagNative | tokenFlagClose | tokenStateMask

	offMint        = 0
	offOwner       = 32
	offAmount      = 64
	offDelegateTag = 72
	offDelegate    = 76
	offState       = 108
	offNativeTag   = 109
	offNative      = 113
	offDelegated   = 121
	offCloseTag    = 129
	offClose       = 133
)

type solanaTokenCodec struct{}

func (solanaTokenCodec) ID() ID       { return SolanaTokenAccount }
func (solanaTokenCodec) Name() string { return "solana-token-account" }

// EncodeSolanaTokenAccount is the write-path entry point. It is a handful of
// byte moves, so it runs on the foreground path; values of any other length are
// rejected on the first comparison.
func (r *Registry) EncodeSolanaTokenAccount(src []byte) (Record, bool) {
	n := len(src)
	if n != tokenAccountSize && n != tokenAccountB64Size {
		return Record{}, false
	}
	c := solanaTokenCodec{}
	data, ok := c.Encode(src)
	if !ok || len(data) >= n {
		return Record{}, false
	}
	decoded, err := c.Decode(data, n)
	if err != nil || !bytes.Equal(decoded, src) {
		return Record{}, false
	}
	return Record{ID: SolanaTokenAccount, RawLength: n, Data: data}, true
}

func (solanaTokenCodec) Encode(src []byte) ([]byte, bool) {
	raw := src
	if len(src) == tokenAccountB64Size {
		var buf [tokenAccountSize]byte
		n, err := base64.StdEncoding.Strict().Decode(buf[:], src)
		if err != nil || n != tokenAccountSize {
			return nil, false
		}
		raw = buf[:]
	} else if len(src) != tokenAccountSize {
		return nil, false
	}

	state := raw[offState]
	if state > 2 {
		return nil, false
	}
	flags := state << tokenStateShift

	delegate, ok := option(raw, offDelegateTag, offDelegate, 32)
	if !ok {
		return nil, false
	}
	if delegate {
		flags |= tokenFlagDelegate
	}
	native, ok := option(raw, offNativeTag, offNative, 8)
	if !ok {
		return nil, false
	}
	if native {
		flags |= tokenFlagNative
	}
	closeAuth, ok := option(raw, offCloseTag, offClose, 32)
	if !ok {
		return nil, false
	}
	if closeAuth {
		flags |= tokenFlagClose
	}

	out := make([]byte, 0, 1+64+9+32+9+9+32)
	out = append(out, flags)
	out = append(out, raw[offMint:offMint+64]...) // mint then owner
	out = binary.AppendUvarint(out, binary.LittleEndian.Uint64(raw[offAmount:]))
	if delegate {
		out = append(out, raw[offDelegate:offDelegate+32]...)
	}
	if native {
		out = binary.AppendUvarint(out, binary.LittleEndian.Uint64(raw[offNative:]))
	}
	out = binary.AppendUvarint(out, binary.LittleEndian.Uint64(raw[offDelegated:]))
	if closeAuth {
		out = append(out, raw[offClose:offClose+32]...)
	}
	return out, true
}

// option reads a COption: a little-endian u32 tag (0 or 1) followed by a fixed
// payload. An absent option must have an all-zero payload to be encoded, since
// the payload is not stored.
func option(raw []byte, tagOff, valOff, size int) (present, ok bool) {
	switch binary.LittleEndian.Uint32(raw[tagOff:]) {
	case 1:
		return true, true
	case 0:
		for _, b := range raw[valOff : valOff+size] {
			if b != 0 {
				return false, false
			}
		}
		return false, true
	}
	return false, false
}

func (c solanaTokenCodec) Decode(src []byte, rawLength int) ([]byte, error) {
	return c.DecodeInto(src, rawLength, nil)
}

func (solanaTokenCodec) DecodeInto(src []byte, rawLength int, dst []byte) ([]byte, error) {
	if rawLength != tokenAccountSize && rawLength != tokenAccountB64Size {
		return nil, errors.New("invalid token account length")
	}
	if len(src) < 1+64+1+1 {
		return nil, errors.New("short token account payload")
	}
	flags := src[0]
	if flags&^byte(tokenKnownFlags) != 0 {
		return nil, errors.New("unknown token account flags")
	}
	state := (flags & tokenStateMask) >> tokenStateShift
	if state > 2 {
		return nil, errors.New("invalid token account state")
	}

	var raw [tokenAccountSize]byte
	p := src[1:]
	copy(raw[offMint:], p[:64])
	p = p[64:]

	next := func() (uint64, error) {
		v, n := binary.Uvarint(p)
		if n <= 0 {
			return 0, errors.New("invalid token account varint")
		}
		p = p[n:]
		return v, nil
	}
	take := func(size int) ([]byte, error) {
		if len(p) < size {
			return nil, errors.New("short token account payload")
		}
		b := p[:size]
		p = p[size:]
		return b, nil
	}

	amount, err := next()
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(raw[offAmount:], amount)
	if flags&tokenFlagDelegate != 0 {
		b, err := take(32)
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint32(raw[offDelegateTag:], 1)
		copy(raw[offDelegate:], b)
	}
	raw[offState] = state
	if flags&tokenFlagNative != 0 {
		v, err := next()
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint32(raw[offNativeTag:], 1)
		binary.LittleEndian.PutUint64(raw[offNative:], v)
	}
	delegated, err := next()
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(raw[offDelegated:], delegated)
	if flags&tokenFlagClose != 0 {
		b, err := take(32)
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint32(raw[offCloseTag:], 1)
		copy(raw[offClose:], b)
	}
	if len(p) != 0 {
		return nil, errors.New("trailing token account bytes")
	}

	if rawLength == tokenAccountSize {
		return append(dst[:0], raw[:]...), nil
	}
	if cap(dst) < tokenAccountB64Size {
		dst = make([]byte, tokenAccountB64Size)
	}
	dst = dst[:tokenAccountB64Size]
	base64.StdEncoding.Encode(dst, raw[:])
	return dst, nil
}
