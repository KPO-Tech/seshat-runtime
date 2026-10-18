package db

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/KPO-Tech/seshat-runtime/internal/keychain"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ─── GORM private model ───────────────────────────────────────────────────────

type gCredential struct {
	Key           string `gorm:"primaryKey;size:191"`
	CipherText    string `gorm:"column:cipher_text;not null"`
	CreatedAtUnix int64  `gorm:"column:created_at_unix;autoCreateTime:unix"`
	UpdatedAtUnix int64  `gorm:"column:updated_at_unix;autoUpdateTime:unix"`
}

func (gCredential) TableName() string { return "credentials" }

// ─── Public API (methods on *DB) ─────────────────────────────────────────────

// UpsertCredential encrypts plaintext and stores it under key.
func (db *DB) UpsertCredential(ctx context.Context, key, plaintext string) error {
	aesKey, err := loadOrCreateEncryptionKey()
	if err != nil {
		return err
	}
	cipherText, err := encryptAESGCM(aesKey, []byte(plaintext))
	if err != nil {
		return err
	}
	row := gCredential{Key: key, CipherText: cipherText}
	return db.gormDB.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"cipher_text", "updated_at_unix"}),
		}).Create(&row).Error
}

// GetCredential retrieves and decrypts the value stored under key.
// Returns ("", false, nil) when the key does not exist.
func (db *DB) GetCredential(ctx context.Context, key string) (string, bool, error) {
	var row gCredential
	err := db.gormDB.WithContext(ctx).Where("key = ?", key).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	aesKey, err := loadOrCreateEncryptionKey()
	if err != nil {
		return "", false, err
	}
	plain, err := decryptAESGCM(aesKey, row.CipherText)
	if err != nil {
		return "", false, err
	}
	return string(plain), true, nil
}

// DeleteCredential removes the credential for key. No-op if it does not exist.
func (db *DB) DeleteCredential(ctx context.Context, key string) error {
	return db.gormDB.WithContext(ctx).Delete(&gCredential{}, "key = ?", key).Error
}

// ListCredentialKeys returns the stored keys without their values.
func (db *DB) ListCredentialKeys(ctx context.Context) ([]string, error) {
	var rows []gCredential
	if err := db.gormDB.WithContext(ctx).Order("key").Find(&rows).Error; err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	return keys, nil
}

// ─── Encryption helpers ────────────────────────────────────────────────────────

// loadOrCreateEncryptionKey returns the 32-byte AES key that encrypts the stored credentials.
//
// The key lives in the operating system's credential store when there is one (see internal/keychain), because a
// key kept in a file next to the ciphertext protects nothing from another program running as the same user. A key
// found in the legacy secret.key file of the runtime root is moved there, and the file is removed once the store
// has given the key back. When there is no usable store (a container, a server with no desktop session) the key
// stays in secret.key with mode 0600, as before.
func loadOrCreateEncryptionKey() ([]byte, error) {
	path, err := encryptionKeyPath()
	if err != nil {
		return nil, err
	}

	keyMu.Lock()
	defer keyMu.Unlock()
	if key, ok := cachedKeys[path]; ok {
		return key, nil
	}
	key, err := resolveEncryptionKey(path)
	if err != nil {
		return nil, err
	}
	cachedKeys[path] = key
	return key, nil
}

var (
	keyMu      sync.Mutex
	cachedKeys = map[string][]byte{}
)

// keychainAccount names the store item that holds the key of one runtime root: the CLI and the product have
// separate roots and must not share a key.
func keychainAccount(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return "credentials-key:" + hex.EncodeToString(sum[:8])
}

func resolveEncryptionKey(path string) ([]byte, error) {
	account := keychainAccount(path)

	if stored, err := keychain.Get(account); err == nil {
		if key, derr := base64.StdEncoding.DecodeString(stored); derr == nil && len(key) == 32 {
			return key, nil
		}
	}

	data, err := os.ReadFile(path)
	if err == nil && len(data) == 32 {
		moveKeyToKeychain(account, path, data)
		return data, nil
	}

	// Generate a new key.
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	if storeKeyInKeychain(account, key) {
		return key, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write encryption key: %w", err)
	}
	return key, nil
}

// storeKeyInKeychain saves key and reads it back: a store that accepts a write and does not return it would lose
// the key, and with it every stored credential.
func storeKeyInKeychain(account string, key []byte) bool {
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := keychain.Set(account, encoded); err != nil {
		return false
	}
	back, err := keychain.Get(account)
	return err == nil && back == encoded
}

// moveKeyToKeychain moves the key of the legacy file into the store and deletes the file, only once the store
// holds the same key.
func moveKeyToKeychain(account, path string, key []byte) {
	if !storeKeyInKeychain(account, key) {
		return
	}
	if err := os.Remove(path); err != nil {
		slog.Warn("the encryption key is in the credential store but the old key file could not be removed", "path", path, "error", err)
	}
}

func encryptionKeyPath() (string, error) {
	return runtimepath.Join("", "secret.key"), nil
}

// encryptAESGCM encrypts plaintext with AES-256-GCM and returns a base64 string
// of the form: base64(nonce || ciphertext).
func encryptAESGCM(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// decryptAESGCM reverses encryptAESGCM.
func decryptAESGCM(key []byte, encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode credential: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, fmt.Errorf("credential data too short")
	}
	plain, err := gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	return plain, nil
}
