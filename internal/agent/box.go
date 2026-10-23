package agent

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Sealed values for one server (environment values, registry passwords).
//
// Commands are signed but travel through SnugKV in clear, so anything secret
// in them is sealed to the server: the agent keeps an X25519 key in its state
// directory and shows the public half on its card (boxKey); the console seals
// with a fresh key each time (X25519 + HKDF-SHA256 + AES-256-GCM, bound to the
// region and server).

// Sealed is what the console puts in a command.
type Sealed struct {
	EPK   string `json:"epk"`   // the console's one-time public key
	Nonce string `json:"nonce"` // 12 bytes
	CT    string `json:"ct"`    // ciphertext + GCM tag
}

const boxInfo = "snug-agent box v1"

// boxAAD binds a sealed value to one server.
func boxAAD(region, server string) []byte { return []byte("snug-box:" + region + "/" + server) }

// EnsureBoxKey loads the agent's sealing key from dir, creating it when missing.
func EnsureBoxKey(dir string) (*ecdh.PrivateKey, error) {
	file := filepath.Join(dir, "box.key")
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		k, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(file, []byte(base64.StdEncoding.EncodeToString(k.Bytes())+"\n"), 0o600); err != nil {
			return nil, err
		}
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, errors.New("box.key is damaged")
	}
	return ecdh.X25519().NewPrivateKey(b)
}

func boxCipher(shared, epk, recipient []byte) (cipher.AEAD, error) {
	salt := append(append([]byte{}, epk...), recipient...)
	key, err := hkdf.Key(sha256.New, shared, salt, boxInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Open decrypts a value sealed to this server.
func Open(priv *ecdh.PrivateKey, s Sealed, region, server string) ([]byte, error) {
	dec := func(v string) []byte { b, _ := base64.StdEncoding.DecodeString(v); return b }
	epk, nonce, ct := dec(s.EPK), dec(s.Nonce), dec(s.CT)
	pub, err := ecdh.X25519().NewPublicKey(epk)
	if err != nil || len(nonce) != 12 {
		return nil, errors.New("sealed value is damaged")
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, err
	}
	aead, err := boxCipher(shared, epk, priv.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	out, err := aead.Open(nil, nonce, ct, boxAAD(region, server))
	if err != nil {
		return nil, errors.New("cannot open the sealed value (sealed for another server or key?)")
	}
	return out, nil
}

// Seal encrypts for a server's box key (the console does the same in Node).
func Seal(recipient *ecdh.PublicKey, plain []byte, region, server string) (Sealed, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Sealed{}, err
	}
	shared, err := eph.ECDH(recipient)
	if err != nil {
		return Sealed{}, err
	}
	aead, err := boxCipher(shared, eph.PublicKey().Bytes(), recipient.Bytes())
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, err
	}
	enc := base64.StdEncoding.EncodeToString
	return Sealed{EPK: enc(eph.PublicKey().Bytes()), Nonce: enc(nonce), CT: enc(aead.Seal(nil, nonce, plain, boxAAD(region, server)))}, nil
}
