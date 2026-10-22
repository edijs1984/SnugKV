package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const encPrefix = "enc:v1:"

// sealer encrypts secret values with AES-256-GCM. The key comes from
// -secrets-key and never leaves the process, so the stored keys, the AOF and
// replicas only ever hold ciphertext. Every value is bound to its place
// (environment and name), so ciphertext copied to another secret fails to open.
type sealer struct{ aead cipher.AEAD }

func newSealer(key string) (*sealer, error) {
	if len(key) < 16 {
		return nil, errors.New("the secrets key must be at least 16 characters")
	}
	sum := sha256.Sum256([]byte("snugkv-secrets-v1\x00" + key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

func (s *sealer) seal(plain, bind string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, []byte(plain), []byte(bind))
	return encPrefix + base64.RawStdEncoding.EncodeToString(ct), nil
}

func (s *sealer) open(stored, bind string) (string, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return "", errors.New("stored secret has an unknown format")
	}
	raw, err := base64.RawStdEncoding.DecodeString(stored[len(encPrefix):])
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", errors.New("stored secret is corrupt")
	}
	pt, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], []byte(bind))
	if err != nil {
		return "", errors.New("stored secret cannot be decrypted (wrong secrets key?)")
	}
	return string(pt), nil
}

// hint shows the last characters of a value, enough to tell two apart.
func hint(v string) string {
	if len(v) < 12 {
		return "••••"
	}
	return "••••" + v[len(v)-4:]
}
