package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	secretEnvVar    = "XMUX_SECRET_KEY"
	secretKeyLen    = 32
	secretFileMode  = 0o600
	secretEncPrefix = "enc:v1:"
)

type secretKeeper struct {
	once sync.Once
	key  []byte
	err  error
	path string
}

func newSecretKeeper(dataDir string) *secretKeeper {
	path := filepath.Join(dataDir, "secret.key")
	return &secretKeeper{path: path}
}

// Key loads the AES key, generating one on first call if neither the env var
// nor an on-disk key file is present.
func (k *secretKeeper) Key() ([]byte, error) {
	k.once.Do(func() {
		k.key, k.err = loadOrCreateSecretKey(k.path)
	})
	if k.err != nil {
		return nil, k.err
	}
	return k.key, nil
}

func loadOrCreateSecretKey(path string) ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv(secretEnvVar)); raw != "" {
		key, err := decodeKey(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", secretEnvVar, err)
		}
		return key, nil
	}
	if data, err := os.ReadFile(path); err == nil {
		key, err := decodeKey(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, secretKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key) + "\n"
	if err := os.WriteFile(path, []byte(encoded), secretFileMode); err != nil {
		return nil, err
	}
	return key, nil
}

func decodeKey(raw string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == secretKeyLen {
		return b, nil
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) == secretKeyLen {
		return b, nil
	}
	if len(raw) == secretKeyLen {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("secret key must decode to %d bytes", secretKeyLen)
}

// encryptSecret encrypts plaintext with AES-GCM and returns a self-describing
// string ("enc:v1:" + base64(nonce||ciphertext)). Empty input returns empty.
func (k *secretKeeper) encryptSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key, err := k.Key()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(plaintext), nil)
	buf := make([]byte, 0, len(nonce)+len(ciphertext))
	buf = append(buf, nonce...)
	buf = append(buf, ciphertext...)
	return secretEncPrefix + base64.StdEncoding.EncodeToString(buf), nil
}

func (k *secretKeeper) decryptSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, secretEncPrefix) {
		// Backward-compat: legacy plaintext value.
		return value, nil
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, secretEncPrefix))
	if err != nil {
		return "", err
	}
	key, err := k.Key()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < aead.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce := payload[:aead.NonceSize()]
	ciphertext := payload[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// isEncryptedSecret reports whether a stored value is already encrypted.
func isEncryptedSecret(value string) bool {
	return strings.HasPrefix(value, secretEncPrefix)
}
