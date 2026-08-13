package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const envelopeVersion = "v1"

type Cipher struct {
	aead cipher.AEAD
}

func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Encrypt(plaintext []byte, context string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nil, nonce, plaintext, []byte(context))
	payload := append(nonce, sealed...)
	return envelopeVersion + "." + base64.RawURLEncoding.EncodeToString(payload), nil
}

func (c *Cipher) Decrypt(value, context string) ([]byte, error) {
	if len(value) < 4 || value[:3] != envelopeVersion+"." {
		return nil, errors.New("unsupported encrypted value")
	}
	payload, err := base64.RawURLEncoding.DecodeString(value[3:])
	if err != nil {
		return nil, fmt.Errorf("decode encrypted value: %w", err)
	}
	if len(payload) < c.aead.NonceSize() {
		return nil, errors.New("encrypted value is truncated")
	}
	nonce, ciphertext := payload[:c.aead.NonceSize()], payload[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, []byte(context))
	if err != nil {
		return nil, errors.New("encrypted value authentication failed")
	}
	return plaintext, nil
}

func RandomKey() ([]byte, error) {
	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	return key, err
}

func TokenHash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func RandomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
