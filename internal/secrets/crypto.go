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
//
// During a key rotation it also holds the previous key: values sealed with it
// still open, and the module re-seals them with the new key at start.
type sealer struct {
	aead cipher.AEAD
	old  cipher.AEAD // previous key during a rotation, or nil
}

func aeadFor(key string) (cipher.AEAD, error) {
	if len(key) < 16 {
		return nil, errors.New("the secrets key must be at least 16 characters")
	}
	sum := sha256.Sum256([]byte("snugkv-secrets-v1\x00" + key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func newSealer(key string) (*sealer, error) {
	aead, err := aeadFor(key)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

// withOld adds the previous key for a rotation.
func (s *sealer) withOld(oldKey string) error {
	if oldKey == "" {
		return nil
	}
	aead, err := aeadFor(oldKey)
	if err != nil {
		return errors.New("the old secrets key: " + err.Error())
	}
	s.old = aead
	return nil
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
	plain, _, err := s.openWith(stored, bind)
	return plain, err
}

// openWith also says whether the value was sealed with the old key.
func (s *sealer) openWith(stored, bind string) (string, bool, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return "", false, errors.New("stored secret has an unknown format")
	}
	raw, err := base64.RawStdEncoding.DecodeString(stored[len(encPrefix):])
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", false, errors.New("stored secret is corrupt")
	}
	nonce, ct := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]
	if pt, err := s.aead.Open(nil, nonce, ct, []byte(bind)); err == nil {
		return string(pt), false, nil
	}
	if s.old != nil {
		if pt, err := s.old.Open(nil, nonce, ct, []byte(bind)); err == nil {
			return string(pt), true, nil
		}
	}
	return "", false, errors.New("stored secret cannot be decrypted (wrong secrets key?)")
}

// hint shows the last characters of a value, enough to tell two apart.
func hint(v string) string {
	if len(v) < 12 {
		return "••••"
	}
	return "••••" + v[len(v)-4:]
}
