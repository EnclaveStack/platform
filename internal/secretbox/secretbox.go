// Package secretbox seals secret values at rest with AES-256-GCM under the machine's master key
// (L3-006 §3). The master key is 32 random bytes in <data-dir>/master.key, mode 0600.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// KeySize is the master key length in bytes.
const KeySize = 32

// ErrOpen is returned when a ciphertext cannot be opened (wrong key or tampered data).
var ErrOpen = errors.New("secretbox: cannot decrypt")

// Box seals and opens values.
type Box struct{ aead cipher.AEAD }

// New builds a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secretbox: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// LoadOrCreateKey reads the key at path, creating it (0600) with fresh random bytes if missing.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != KeySize {
			return nil, fmt.Errorf("master key %s is %d bytes, expected %d", path, len(key), KeySize)
		}
		return key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	key = make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create master key: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(key); err != nil {
		return nil, err
	}
	return key, f.Sync()
}

// Seal encrypts plaintext; the random nonce is prefixed to the result. additional binds the
// ciphertext to its owner (the resource ID), so it cannot be moved to another row.
func (b *Box) Seal(plaintext, additional []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, additional), nil
}

// Open decrypts a value produced by Seal with the same additional data.
func (b *Box) Open(ciphertext, additional []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(ciphertext) < n+b.aead.Overhead() {
		return nil, ErrOpen
	}
	out, err := b.aead.Open(nil, ciphertext[:n], ciphertext[n:], additional)
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}
