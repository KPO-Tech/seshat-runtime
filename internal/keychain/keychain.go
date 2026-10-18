// Package keychain keeps small secrets in the operating system's credential store: the Windows Credential Manager,
// the macOS Keychain, or the Secret Service (GNOME Keyring, KWallet) on Linux.
//
// A secret kept in a file next to the data it protects, such as the key that encrypts the stored API keys, shields
// nothing from a program running as the same user: it reads both. The operating system store is the place that
// separates them. On macOS another program reading an item prompts the user; on Windows the item is protected by the
// user's login (DPAPI).
//
// The store is not always there (a container, a server with no desktop session, an SSH login with a locked
// keychain). Every function then returns ErrUnavailable, and the caller decides what to do: the callers of this
// package fall back to a file with owner-only permissions, as before, rather than refuse to run.
package keychain

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	gokeyring "github.com/zalando/go-keyring"
)

// Service is the name every item is stored under.
const Service = "seshat"

// EnvDisable turns the operating system store off (value "off", "0" or "false"): the callers keep their file
// fallback. For containers and CI, where a store that blocks or prompts is worse than none.
const EnvDisable = "SESHAT_KEYCHAIN"

var (
	// ErrNotFound means the store works and holds no item of that name.
	ErrNotFound = errors.New("keychain: item not found")
	// ErrUnavailable means there is no usable store: disabled, absent, locked or too slow to answer.
	ErrUnavailable = errors.New("keychain: no usable credential store")
)

// timeout bounds every call: a Secret Service that waits for an unlock prompt would otherwise hang the program.
const timeout = 3 * time.Second

// backend is the part of the store the package uses; tests replace it.
type backend interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
	Delete(service, account string) error
}

type osBackend struct{}

func (osBackend) Get(service, account string) (string, error) {
	return gokeyring.Get(service, account)
}
func (osBackend) Set(service, account, value string) error {
	return gokeyring.Set(service, account, value)
}
func (osBackend) Delete(service, account string) error { return gokeyring.Delete(service, account) }

var (
	mu      sync.RWMutex
	current backend = osBackend{}
	// forced makes Enabled report true whatever the environment: set by tests that install a fake store.
	forced bool
)

// Enabled reports whether this process may use the operating system store. It is off when SESHAT_KEYCHAIN says so
// and inside Go test binaries, which must never write to the developer's real credential store (a test installs a
// fake with UseForTesting instead).
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	if forced {
		return true
	}
	if testing.Testing() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvDisable))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

// call runs fn with a deadline: the store has no way to be cancelled, so a call that does not answer is abandoned.
func call[T any](fn func(backend) (T, error)) (T, error) {
	var zero T
	if !Enabled() {
		return zero, ErrUnavailable
	}
	mu.RLock()
	b := current
	mu.RUnlock()

	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := fn(b)
		done <- result{value, err}
	}()
	select {
	case r := <-done:
		switch {
		case r.err == nil:
			return r.value, nil
		case errors.Is(r.err, gokeyring.ErrNotFound):
			return zero, ErrNotFound
		default:
			return zero, errors.Join(ErrUnavailable, r.err)
		}
	case <-time.After(timeout):
		return zero, errors.Join(ErrUnavailable, errors.New("the credential store did not answer in time"))
	}
}

// Get returns the secret stored under account, ErrNotFound when there is none, ErrUnavailable when there is no
// usable store.
func Get(account string) (string, error) {
	return call(func(b backend) (string, error) { return b.Get(Service, account) })
}

// Set stores value under account.
func Set(account, value string) error {
	_, err := call(func(b backend) (struct{}, error) { return struct{}{}, b.Set(Service, account, value) })
	return err
}

// Delete removes the item stored under account; a missing item is not an error.
func Delete(account string) error {
	_, err := call(func(b backend) (struct{}, error) { return struct{}{}, b.Delete(Service, account) })
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// UseForTesting installs an in-memory store and turns the package on, and returns the function that restores the
// previous state. It exists so that tests of the packages that call this one can exercise the store without touching
// the real one.
func UseForTesting() (restore func()) {
	fake := &memoryBackend{items: map[string]string{}}
	mu.Lock()
	prevBackend, prevForced := current, forced
	current, forced = fake, true
	mu.Unlock()
	return func() {
		mu.Lock()
		current, forced = prevBackend, prevForced
		mu.Unlock()
	}
}

type memoryBackend struct {
	mu    sync.Mutex
	items map[string]string
}

func (m *memoryBackend) Get(service, account string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.items[service+"\x00"+account]; ok {
		return v, nil
	}
	return "", gokeyring.ErrNotFound
}

func (m *memoryBackend) Set(service, account, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[service+"\x00"+account] = value
	return nil
}

func (m *memoryBackend) Delete(service, account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[service+"\x00"+account]; !ok {
		return gokeyring.ErrNotFound
	}
	delete(m.items, service+"\x00"+account)
	return nil
}
