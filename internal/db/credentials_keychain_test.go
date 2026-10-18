package db

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/keychain"
)

// freshRoot points the runtime root at an empty folder and forgets the keys cached for other folders.
func freshRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SESHAT_RUNTIME_ROOT", root)
	keyMu.Lock()
	cachedKeys = map[string][]byte{}
	keyMu.Unlock()
	return root
}

func TestANewKeyGoesToTheCredentialStoreNotToAFile(t *testing.T) {
	defer keychain.UseForTesting()()
	root := freshRoot(t)

	key, err := loadOrCreateEncryptionKey()
	if err != nil || len(key) != 32 {
		t.Fatalf("loadOrCreateEncryptionKey = %d bytes, %v", len(key), err)
	}
	if _, err := os.Stat(filepath.Join(root, "secret.key")); err == nil {
		t.Fatal("with a credential store the key must not be written next to the data")
	}
	stored, err := keychain.Get(keychainAccount(filepath.Join(root, "secret.key")))
	if err != nil {
		t.Fatalf("the key must be in the store: %v", err)
	}
	if decoded, _ := base64.StdEncoding.DecodeString(stored); !bytes.Equal(decoded, key) {
		t.Fatal("the store holds another key than the one returned")
	}
}

func TestALegacyKeyFileMovesToTheStoreAndIsRemoved(t *testing.T) {
	defer keychain.UseForTesting()()
	root := freshRoot(t)
	legacy := bytes.Repeat([]byte{0x42}, 32)
	file := filepath.Join(root, "secret.key")
	if err := os.WriteFile(file, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := loadOrCreateEncryptionKey()
	if err != nil || !bytes.Equal(key, legacy) {
		t.Fatalf("the key already in use must not change: %v", err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("the legacy file must be removed once the store holds the key")
	}
	stored, err := keychain.Get(keychainAccount(file))
	if err != nil {
		t.Fatal(err)
	}
	if decoded, _ := base64.StdEncoding.DecodeString(stored); !bytes.Equal(decoded, legacy) {
		t.Error("the store must hold the legacy key")
	}
}

func TestWithoutACredentialStoreTheKeyStaysInAnOwnerOnlyFile(t *testing.T) {
	// The package is off inside test binaries: this is the container and headless-server case.
	root := freshRoot(t)

	key, err := loadOrCreateEncryptionKey()
	if err != nil || len(key) != 32 {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "secret.key"))
	if err != nil {
		t.Fatalf("the fallback file must exist: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("the key file must be owner-only, got %v", info.Mode().Perm())
	}
}

func TestTheStoreWinsOverADifferingFileWhichIsKept(t *testing.T) {
	defer keychain.UseForTesting()()
	root := freshRoot(t)
	file := filepath.Join(root, "secret.key")
	inStore := bytes.Repeat([]byte{1}, 32)
	if err := keychain.Set(keychainAccount(file), base64.StdEncoding.EncodeToString(inStore)); err != nil {
		t.Fatal(err)
	}
	other := bytes.Repeat([]byte{2}, 32)
	if err := os.WriteFile(file, other, 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := loadOrCreateEncryptionKey()
	if err != nil || !bytes.Equal(key, inStore) {
		t.Fatalf("the store must win: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Error("a file holding another key may still decrypt old data and must not be deleted")
	}
}

func TestEncryptedCredentialsSurviveTheMoveToTheStore(t *testing.T) {
	root := freshRoot(t)
	// Written the old way (no store), read after the store appears.
	oldKey, err := loadOrCreateEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := encryptAESGCM(oldKey, []byte("sk-secret"))
	if err != nil {
		t.Fatal(err)
	}

	defer keychain.UseForTesting()()
	keyMu.Lock()
	cachedKeys = map[string][]byte{} // a new process
	keyMu.Unlock()
	newKey, err := loadOrCreateEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptAESGCM(newKey, sealed)
	if err != nil || string(plain) != "sk-secret" {
		t.Fatalf("what was encrypted before the move must still open: %q, %v", plain, err)
	}
	if _, err := os.Stat(filepath.Join(root, "secret.key")); err == nil {
		t.Error("the file must be gone after the move")
	}
}
