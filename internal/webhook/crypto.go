package webhook

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

// sealer encrypts stored secrets (API keys, signing secrets) with AES-256-GCM.
// The key never leaves the process: it comes from -webhooks-key or the
// SNUGKV_WEBHOOKS_KEY environment variable.
type sealer struct{ aead cipher.AEAD }

func newSealer(key string) (*sealer, error) {
	if key == "" {
		return nil, nil
	}
	if len(key) < 16 {
		return nil, errors.New("webhooks key must be at least 16 characters")
	}
	sum := sha256.Sum256([]byte("snugkv-webhooks-v1\x00" + key))
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

func (s *sealer) seal(plain string) (string, error) {
	if s == nil {
		return "", errors.New("storing secrets needs -webhooks-key (or SNUGKV_WEBHOOKS_KEY)")
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix + base64.RawStdEncoding.EncodeToString(ct), nil
}

func (s *sealer) open(stored string) (string, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return "", errors.New("stored secret has an unknown format")
	}
	if s == nil {
		return "", errors.New("webhooks key is not configured, cannot read stored secrets")
	}
	raw, err := base64.RawStdEncoding.DecodeString(stored[len(encPrefix):])
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", errors.New("stored secret is corrupt")
	}
	pt, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("stored secret cannot be decrypted (wrong webhooks key?)")
	}
	return string(pt), nil
}

func hint(secret string) string {
	if len(secret) <= 4 {
		return "••••"
	}
	return "••••" + secret[len(secret)-4:]
}
