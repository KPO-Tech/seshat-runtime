package collectionplan

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// ScanDir lists the files under root without reading them, unless hash is set: then each regular file is read once, on this computer,
// to give its content hash, which is what finds duplicates. A symbolic link is listed as a link and not followed. What cannot be read
// (a folder without the right to list it, a file that cannot be opened for its hash) is listed with a Problem instead of stopping the
// scan. Paths are slash-separated and relative to root.
func ScanDir(root string, hash bool) ([]Entry, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &fs.PathError{Op: "scan", Path: root, Err: fs.ErrInvalid}
	}
	var out []Entry
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if walkErr != nil {
			out = append(out, Entry{Path: rel, Problem: walkErr.Error()})
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			out = append(out, Entry{Path: rel, Link: true})
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		fi, infoErr := d.Info()
		if infoErr != nil {
			out = append(out, Entry{Path: rel, Problem: infoErr.Error()})
			return nil
		}
		e := Entry{Path: rel, Size: fi.Size(), ModTime: fi.ModTime()}
		if hash {
			sum, hashErr := hashFile(p)
			if hashErr != nil {
				e.Problem = hashErr.Error()
			}
			e.Hash = sum
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
