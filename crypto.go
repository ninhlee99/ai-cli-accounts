package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "am-master-key"
	keyringUser    = "default"
)

// masterKey returns the 32-byte AES key, creating and storing one in the
// system keychain on first use.
func masterKey() []byte {
	s, err := keyring.Get(keyringService, keyringUser)
	if err == nil {
		k, decErr := base64.StdEncoding.DecodeString(s)
		if decErr == nil && len(k) == 32 {
			return k
		}
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		die("rand: %v", err)
	}
	if err := keyring.Set(keyringService, keyringUser, base64.StdEncoding.EncodeToString(k)); err != nil {
		die("store master key in keychain: %v", err)
	}
	return k
}

func encrypt(plain []byte) []byte {
	block, err := aes.NewCipher(masterKey())
	if err != nil {
		die("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		die("gcm: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		die("nonce: %v", err)
	}
	return gcm.Seal(nonce, nonce, plain, nil)
}

func decrypt(enc []byte) []byte {
	block, err := aes.NewCipher(masterKey())
	if err != nil {
		die("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		die("gcm: %v", err)
	}
	if len(enc) < gcm.NonceSize() {
		die("ciphertext too short")
	}
	nonce, body := enc[:gcm.NonceSize()], enc[gcm.NonceSize():]
	out, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		die("decrypt (wrong master key?): %v", err)
	}
	return out
}
