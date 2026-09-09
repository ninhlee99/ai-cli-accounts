package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "am-master-key"
	keyringUser    = "default"
)

// MasterKey returns the 32-byte AES key, creating and storing one in the
// system keychain on first use.
func MasterKey() []byte {
	s, err := keyring.Get(keyringService, keyringUser)
	if err == nil {
		k, decErr := base64.StdEncoding.DecodeString(s)
		if decErr == nil && len(k) == 32 {
			return k
		}
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(fmt.Sprintf("rand: %v", err))
	}
	if err := keyring.Set(keyringService, keyringUser, base64.StdEncoding.EncodeToString(k)); err != nil {
		panic(fmt.Sprintf("store master key in keychain: %v", err))
	}
	return k
}

// Encrypt encrypts plain bytes using AES-GCM with the master key.
func Encrypt(plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(MasterKey())
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

// Decrypt decrypts encrypted bytes using AES-GCM with the master key.
func Decrypt(enc []byte) ([]byte, error) {
	block, err := aes.NewCipher(MasterKey())
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	if len(enc) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, body := enc[:gcm.NonceSize()], enc[gcm.NonceSize():]
	out, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt (wrong master key?): %w", err)
	}
	return out, nil
}
