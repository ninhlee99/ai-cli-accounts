package auth

import (
	"bytes"
	"testing"
)

func TestCrypto_EncryptDecrypt(t *testing.T) {
	msg := []byte("hello secret world 12345")
	enc, err := Encrypt(msg)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if bytes.Equal(msg, enc) {
		t.Fatalf("ciphertext should not match plaintext")
	}
	plain, err := Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if !bytes.Equal(msg, plain) {
		t.Fatalf("expected %s, got %s", string(msg), string(plain))
	}
}
