package codec

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"testing"
)

type tokenOpts struct {
	delegate, native, closeAuth bool
	state                       byte
	amount, delegated, nativeV  uint64
}

func buildTokenAccount(t testing.TB, o tokenOpts) []byte {
	t.Helper()
	b := make([]byte, tokenAccountSize)
	rand.Read(b[offMint : offMint+64])
	binary.LittleEndian.PutUint64(b[offAmount:], o.amount)
	if o.delegate {
		binary.LittleEndian.PutUint32(b[offDelegateTag:], 1)
		rand.Read(b[offDelegate : offDelegate+32])
	}
	b[offState] = o.state
	if o.native {
		binary.LittleEndian.PutUint32(b[offNativeTag:], 1)
		binary.LittleEndian.PutUint64(b[offNative:], o.nativeV)
	}
	binary.LittleEndian.PutUint64(b[offDelegated:], o.delegated)
	if o.closeAuth {
		binary.LittleEndian.PutUint32(b[offCloseTag:], 1)
		rand.Read(b[offClose : offClose+32])
	}
	return b
}

func TestSolanaTokenAccountRoundTrip(t *testing.T) {
	r := NewRegistry()
	for i, o := range []tokenOpts{
		{state: 1, amount: 0},
		{state: 1, amount: 1_000_000},
		{state: 1, amount: ^uint64(0)},
		{state: 2, amount: 5, delegate: true, delegated: 3},
		{state: 1, amount: 9, native: true, nativeV: 2_039_280},
		{state: 1, amount: 9, closeAuth: true},
		{state: 0},
		{state: 1, amount: 77, delegate: true, native: true, closeAuth: true, delegated: ^uint64(0), nativeV: 1},
	} {
		raw := buildTokenAccount(t, o)
		b64 := []byte(base64.StdEncoding.EncodeToString(raw))
		for name, in := range map[string][]byte{"raw": raw, "base64": b64} {
			rec, ok := r.EncodeSolanaTokenAccount(in)
			if !ok || rec.ID != SolanaTokenAccount || rec.RawLength != len(in) {
				t.Fatalf("case %d %s: ok=%v rec=%+v", i, name, ok, rec)
			}
			if len(rec.Data) > 1+64+10+32+10+10+32 {
				t.Fatalf("case %d %s: payload %d", i, name, len(rec.Data))
			}
			out, err := r.Decode(rec, len(in))
			if err != nil || !bytes.Equal(out, in) {
				t.Fatalf("case %d %s: round trip failed: %v", i, name, err)
			}
			// the registry's own encoder reaches the same record
			if got := r.Encode(in); got.ID != SolanaTokenAccount {
				t.Fatalf("case %d %s: Encode chose %v", i, name, got.ID)
			}
		}
	}
	rec, _ := r.EncodeSolanaTokenAccount(buildTokenAccount(t, tokenOpts{state: 1, amount: 123456}))
	if len(rec.Data) > 72 {
		t.Errorf("common account payload = %d bytes, want <= 72", len(rec.Data))
	}
}

func TestSolanaTokenAccountRejects(t *testing.T) {
	r := NewRegistry()
	good := buildTokenAccount(t, tokenOpts{state: 1, amount: 5})
	mut := func(f func(b []byte)) []byte { b := bytes.Clone(good); f(b); return b }
	for name, in := range map[string][]byte{
		"state 3":               mut(func(b []byte) { b[offState] = 3 }),
		"delegate tag 2":        mut(func(b []byte) { b[offDelegateTag] = 2 }),
		"native tag 5":          mut(func(b []byte) { b[offNativeTag] = 5 }),
		"close tag high byte":   mut(func(b []byte) { b[offCloseTag+3] = 1 }),
		"absent delegate bytes": mut(func(b []byte) { b[offDelegate+10] = 1 }),
		"absent native bytes":   mut(func(b []byte) { b[offNative+2] = 1 }),
		"absent close bytes":    mut(func(b []byte) { b[offClose+31] = 1 }),
		"length 164":            good[:164],
		"length 166":            append(bytes.Clone(good), 0),
		"base64 bad char":       []byte(string(bytes.Repeat([]byte("A"), 219)) + "!"),
		"base64 wrong size":     []byte(base64.StdEncoding.EncodeToString(good[:162])),
	} {
		if _, ok := r.EncodeSolanaTokenAccount(in); ok {
			t.Errorf("%s: encoded", name)
		}
	}
	// non-canonical base64 (url alphabet / padding variants) must not match
	url := []byte(base64.URLEncoding.EncodeToString(good))
	if bytes.ContainsAny(url, "-_") {
		if _, ok := r.EncodeSolanaTokenAccount(url); ok {
			t.Error("url-safe base64 encoded")
		}
	}
}

func TestSolanaTokenAccountCorruptPayload(t *testing.T) {
	r := NewRegistry()
	raw := buildTokenAccount(t, tokenOpts{state: 1, amount: 5, delegate: true})
	rec, _ := r.EncodeSolanaTokenAccount(raw)
	for name, p := range map[string][]byte{
		"empty":         nil,
		"short":         rec.Data[:30],
		"truncated":     rec.Data[:len(rec.Data)-1],
		"trailing":      append(bytes.Clone(rec.Data), 0),
		"reserved flag": append([]byte{rec.Data[0] | 0x80}, rec.Data[1:]...),
		"state 3":       append([]byte{rec.Data[0] | tokenStateMask}, rec.Data[1:]...),
	} {
		if _, err := r.Decode(Record{ID: SolanaTokenAccount, RawLength: tokenAccountSize, Data: p}, tokenAccountSize); err == nil {
			t.Errorf("%s decoded without error", name)
		}
	}
	if _, err := r.Decode(Record{ID: SolanaTokenAccount, RawLength: 100, Data: rec.Data}, 4096); err == nil {
		t.Error("wrong raw length decoded")
	}
}

func FuzzSolanaTokenAccount(f *testing.F) {
	seed := make([]byte, tokenAccountSize)
	seed[offState] = 1
	f.Add(seed)
	f.Add([]byte(base64.StdEncoding.EncodeToString(seed)))
	r := NewRegistry()
	f.Fuzz(func(t *testing.T, in []byte) {
		rec, ok := r.EncodeSolanaTokenAccount(in)
		if !ok {
			return
		}
		out, err := r.Decode(rec, len(in))
		if err != nil || !bytes.Equal(out, in) {
			t.Fatalf("round trip failed for %x: %v", in, err)
		}
	})
}

func FuzzSolanaTokenAccountDecode(f *testing.F) {
	f.Add([]byte{0x08}, true)
	r := NewRegistry()
	f.Fuzz(func(t *testing.T, p []byte, b64 bool) {
		n := tokenAccountSize
		if b64 {
			n = tokenAccountB64Size
		}
		out, err := r.Decode(Record{ID: SolanaTokenAccount, RawLength: n, Data: p}, 4096)
		if err == nil && len(out) != n {
			t.Fatalf("decoded %d bytes, want %d", len(out), n)
		}
	})
}
