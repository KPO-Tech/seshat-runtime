// Package keychain keeps small secrets in the operating system's credential store: the Windows Credential Manager,
// the macOS Keychain, or the Secret Service (GNOME Keyring, KWallet) on Linux. When there is no usable store (a
// container, a server with no desktop session) every function returns ErrUnavailable and the caller falls back to
// a file with owner-only permissions. SESHAT_KEYCHAIN=off turns the store off. See internal/keychain.
package keychain

import "github.com/KPO-Tech/seshat-runtime/internal/keychain"

// Service is the name every item is stored under.
const Service = keychain.Service

// EnvDisable turns the operating system store off (value "off", "0" or "false").
const EnvDisable = keychain.EnvDisable

var (
	// ErrNotFound means the store works and holds no item of that name.
	ErrNotFound = keychain.ErrNotFound
	// ErrUnavailable means there is no usable store: disabled, absent, locked or too slow to answer.
	ErrUnavailable = keychain.ErrUnavailable
)

// Enabled reports whether this process may use the operating system store.
func Enabled() bool { return keychain.Enabled() }

// Get returns the secret stored under account, ErrNotFound when there is none, ErrUnavailable when there is no
// usable store.
func Get(account string) (string, error) { return keychain.Get(account) }

// Set stores value under account.
func Set(account, value string) error { return keychain.Set(account, value) }

// Delete removes the item stored under account; a missing item is not an error.
func Delete(account string) error { return keychain.Delete(account) }

// UseForTesting installs an in-memory store and turns the package on, and returns the function that restores the
// previous state, so that tests of the packages that call this one never touch the real store.
func UseForTesting() (restore func()) { return keychain.UseForTesting() }
