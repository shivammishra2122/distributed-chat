package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

// Encrypt encrypts plainText using the passphrase with AES-GCM
// Returns base64 encoded string: Base64(Salt + Nonce + Ciphertext + Tag)
func Encrypt(plainText, passphrase string) (string, error) {
	// Generate random salt
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}

	key := deriveKey(passphrase, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	// Seal appends the ciphertext and encryption tag to 'nonce' (dst)
	// We want the final structure to be: Salt + Nonce + Ciphertext + Tag
	// So we start with Salt, append Nonce, then Seal appends Cipher+Tag to that.

	// Wait, gcm.Seal(dst, nonce, plaintext, data)
	// It appends result to dst.
	// If dst = salt, and we pass nonce separately...
	// Result = salt + (nonce? No, Seal does not append nonce unless dst has it)
	// Usually: final := input_nonce + ciphertext_tag
	// Here we want: salt + input_nonce + ciphertext_tag

	cipherText := gcm.Seal(nonce, nonce, []byte(plainText), nil) // This returns Nonce + Encrypted + Tag

	// Prepend Salt
	final := append(salt, cipherText...)

	return base64.StdEncoding.EncodeToString(final), nil
}

// Decrypt decrypts base64 encoded cipherText using the passphrase
func Decrypt(cipherTextStr, passphrase string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(cipherTextStr)
	if err != nil {
		return "", err
	}

	saltSize := 16
	// We need at least Salt + Nonce + Tag overhead
	// Nonce is 12, Tag is 16. Total overhead = 16+12+16 = 44 bytes minimum
	if len(data) < saltSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	salt := data[:saltSize]
	cipherTextWithNonce := data[saltSize:]

	key := deriveKey(passphrase, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(cipherTextWithNonce) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, cipherText := cipherTextWithNonce[:nonceSize], cipherTextWithNonce[nonceSize:]
	plainText, err := gcm.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return "", err
	}

	return string(plainText), nil
}

func deriveKey(passphrase string, salt []byte) []byte {
	// Argon2id
	// Time: 1
	// Memory: 64KB (64*1024)
	// Threads: 4
	// KeyLen: 32 (for AES-256)
	return argon2.IDKey([]byte(passphrase), salt, 1, 64*1024, 4, 32)
}
