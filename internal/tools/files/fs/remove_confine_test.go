package fs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfineToBase(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "src", "a.txt")
	if err := os.MkdirAll(filepath.Dir(inside), 0o750); err != nil {
		t.Fatal(err)
	}

	t.Run("a file inside the folder is accepted", func(t *testing.T) {
		if _, err := confineToBase(base, inside); err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
	})

	t.Run("a folder named like a parent but starting with two dots is inside", func(t *testing.T) {
		dotted := filepath.Join(base, "..notes", "b.txt")
		if err := os.MkdirAll(filepath.Dir(dotted), 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := confineToBase(base, dotted); err != nil {
			t.Fatalf("a folder called ..notes is inside the working directory: %v", err)
		}
	})

	t.Run("a file next to the folder is refused", func(t *testing.T) {
		other := t.TempDir()
		if _, err := confineToBase(base, filepath.Join(other, "x.txt")); err == nil {
			t.Fatal("expected a refusal for a file outside the working directory")
		}
	})

	t.Run("a working directory that cannot be resolved is a refusal, not a skipped check", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "gone")
		if _, err := confineToBase(missing, inside); err == nil {
			t.Fatal("expected a refusal when the working directory cannot be resolved")
		}
	})

	t.Run("a missing folder means there is nothing to remove", func(t *testing.T) {
		parentMissing, err := confineToBase(base, filepath.Join(base, "nope", "c.txt"))
		if err != nil || !parentMissing {
			t.Fatalf("expected parentMissing without an error, got %v %v", parentMissing, err)
		}
	})
}
