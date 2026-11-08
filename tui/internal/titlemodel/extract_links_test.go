package titlemodel

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

type tarEntry struct {
	name     string
	typeflag byte
	link     string
	body     string
}

// orderedTarGz builds an archive whose entries come in the order given, which matters once a later entry goes through an earlier link.
func orderedTarGz(t *testing.T, entries []tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Linkname: e.link, Mode: 0o644}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// canLink reports whether this machine lets the test create a symbolic link (Windows asks for a privilege).
func canLink(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(dir, "probe")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
}

// A link to "." and then a link to "s/.." are both inside the folder as text, and the second one leaves it on disk. A file written
// through it would land outside. Nothing may be written.
func TestExtractTarRefusesAChainOfLinksThatLeavesTheFolder(t *testing.T) {
	canLink(t)
	root := t.TempDir()
	dest := filepath.Join(root, "out")
	archive := orderedTarGz(t, []tarEntry{
		{name: "s", typeflag: tar.TypeSymlink, link: "."},
		{name: "t", typeflag: tar.TypeSymlink, link: "s/.."},
		{name: "t/escape.txt", typeflag: tar.TypeReg, body: "x"},
	})
	if err := extractTarGz(archive, dest); err == nil {
		t.Fatal("expected the chain of links to be refused")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); err == nil {
		t.Fatal("a file was written outside the folder through the links")
	}
}

// A regular file named like a link made earlier would be written through that link.
func TestExtractTarNeverWritesOverALink(t *testing.T) {
	canLink(t)
	root := t.TempDir()
	dest := filepath.Join(root, "out")
	archive := orderedTarGz(t, []tarEntry{
		{name: "lib.so", typeflag: tar.TypeSymlink, link: "lib.so.1"},
		{name: "lib.so", typeflag: tar.TypeReg, body: "x"},
	})
	if err := extractTarGz(archive, dest); err == nil {
		t.Fatal("expected a file over a link to be refused")
	}
}

// The usual case still works: a shared library that is a link to its versioned file.
func TestExtractTarKeepsALinkInsideTheFolder(t *testing.T) {
	canLink(t)
	dest := filepath.Join(t.TempDir(), "out")
	archive := orderedTarGz(t, []tarEntry{
		{name: "lib/libx.so.1", typeflag: tar.TypeReg, body: "elf"},
		{name: "lib/libx.so", typeflag: tar.TypeSymlink, link: "libx.so.1"},
	})
	if err := extractTarGz(archive, dest); err != nil {
		t.Fatalf("a link inside the folder must be accepted: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "lib", "libx.so"))
	if err != nil || string(got) != "elf" {
		t.Fatalf("the link does not lead to the file: %v %q", err, got)
	}
}

// The size of an entry is a number in the archive. A value above int64 wrapped around into a negative one, and the entry went through
// the limit; it is refused now.
func TestExtractZipRefusesASizeThatWouldWrapAround(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "big.bin",
		Method:             zip.Store,
		CompressedSize64:   3,
		UncompressedSize64: 1 << 63,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "out")
	if err := extractZip(path, dest); err == nil {
		t.Fatal("expected an entry of 2^63 bytes to be refused")
	}
	if _, err := os.Stat(filepath.Join(dest, "big.bin")); err == nil {
		t.Fatal("the entry was written")
	}
}

// What is really written is counted, whatever the header says.
func TestWriteZipEntryStopsAtTheBudget(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeZipEntry(zr.File[0], filepath.Join(t.TempDir(), "a.txt"), 5); err == nil {
		t.Fatal("expected an entry larger than the budget to be refused")
	}
	if written, err := writeZipEntry(zr.File[0], filepath.Join(t.TempDir(), "b.txt"), 10); err != nil || written != 10 {
		t.Fatalf("an entry that fits must be written: %d %v", written, err)
	}
}
