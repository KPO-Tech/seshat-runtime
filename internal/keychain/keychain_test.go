package keychain

import (
	"errors"
	"testing"
	"time"
)

func TestDisabledInsideTestBinariesSoTheRealStoreIsNeverTouched(t *testing.T) {
	if Enabled() {
		t.Fatal("the package must be off in a test binary unless a test installs a fake")
	}
	if _, err := Get("anything"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get = %v, want ErrUnavailable", err)
	}
	if err := Set("anything", "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Set = %v, want ErrUnavailable", err)
	}
}

func TestRoundTripWithTheFakeStore(t *testing.T) {
	defer UseForTesting()()

	if _, err := Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get before Set = %v, want ErrNotFound", err)
	}
	if err := Set("k", "secret"); err != nil {
		t.Fatal(err)
	}
	if got, err := Get("k"); err != nil || got != "secret" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := Delete("k"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("k"); err != nil {
		t.Fatalf("deleting a missing item is not an error, got %v", err)
	}
	if _, err := Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestEnvironmentSwitchTurnsTheStoreOff(t *testing.T) {
	restore := UseForTesting()
	forced = false // let the environment decide again, keeping the fake as the backend
	defer func() { forced = true; restore() }()

	t.Setenv(EnvDisable, "off")
	if Enabled() {
		t.Fatal("SESHAT_KEYCHAIN=off must turn the store off")
	}
}

type slowBackend struct{}

func (slowBackend) Get(string, string) (string, error) { time.Sleep(timeout * 3); return "", nil }
func (slowBackend) Set(string, string, string) error   { return nil }
func (slowBackend) Delete(string, string) error        { return nil }

func TestAStoreThatDoesNotAnswerIsAbandoned(t *testing.T) {
	restore := UseForTesting()
	defer restore()
	mu.Lock()
	current = slowBackend{}
	mu.Unlock()

	start := time.Now()
	_, err := Get("k")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get = %v, want ErrUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > timeout+2*time.Second {
		t.Fatalf("Get waited %v, the deadline is %v", elapsed, timeout)
	}
}
