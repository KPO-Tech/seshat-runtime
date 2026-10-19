package catalog

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// cacheFileName is the file the last fetched catalog is kept in, inside the cache directory.
const cacheFileName = "models-catalog.json.gz"

// RefreshInterval is how old the cache may be before a background refresh fetches the catalog again.
const RefreshInterval = 24 * time.Hour

// Load returns the catalog to use: the embedded one, with the cached fetch merged over it when there is one. It never touches the
// network. cacheDir may be empty to ignore the cache. A damaged cache is ignored, not an error: the embedded catalog stands.
func Load(cacheDir string) (*Catalog, error) {
	trusted, err := Embedded()
	if err != nil {
		return nil, err
	}
	if cacheDir == "" {
		return trusted, nil
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, cacheFileName))
	if err != nil {
		return trusted, nil
	}
	cached, err := decodeSnapshot(data, "cache", true)
	if err != nil {
		return trusted, nil
	}
	return Merge(trusted, cached), nil
}

// CacheAge returns how long ago the cache was written, and false when there is none.
func CacheAge(cacheDir string) (time.Duration, bool) {
	info, err := os.Stat(filepath.Join(cacheDir, cacheFileName))
	if err != nil {
		return 0, false
	}
	return time.Since(info.ModTime()), true
}

// Refresh fetches the catalog from url (DefaultURL when empty) and keeps it in cacheDir for Load. It is the only function of the
// package that uses the network, and the only one a host has to call for the catalog to follow the public one.
func Refresh(ctx context.Context, client *http.Client, url, cacheDir string) error {
	if cacheDir == "" {
		return errors.New("no cache directory to keep the catalog in")
	}
	cat, _, err := Fetch(ctx, client, url)
	if err != nil {
		return err
	}
	data, err := encodeSnapshot(cat)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("keep catalog: %w", err)
	}
	// Write to a temporary file and rename it, so a crash never leaves a half-written cache.
	tmp, err := os.CreateTemp(cacheDir, cacheFileName+".*")
	if err != nil {
		return fmt.Errorf("keep catalog: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("keep catalog: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("keep catalog: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(cacheDir, cacheFileName)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("keep catalog: %w", err)
	}
	return nil
}

// RefreshIfStale refreshes the catalog when the cache is missing or older than RefreshInterval. It reports whether it fetched.
func RefreshIfStale(ctx context.Context, client *http.Client, url, cacheDir string) (bool, error) {
	if age, ok := CacheAge(cacheDir); ok && age < RefreshInterval {
		return false, nil
	}
	return true, Refresh(ctx, client, url, cacheDir)
}

// encodeSnapshot writes a catalog in the compact gzip form Load reads.
func encodeSnapshot(c *Catalog) ([]byte, error) {
	doc := snapshotDoc{Version: 1, Providers: make(map[string]snapshotProvider, len(c.Providers))}
	for id, p := range c.Providers {
		sp := snapshotProvider{Name: p.Name, API: p.BaseURL, Env: p.Env, Protocol: p.Protocol, Models: make(map[string]snapshotModel, len(p.Models))}
		for mid, m := range p.Models {
			sm := snapshotModel{Name: m.Name, Context: m.ContextWindow, Output: m.MaxOutput, Status: m.Status, Date: m.ReleaseDate}
			if m.Reasoning {
				sm.Reason = 1
			}
			for i, in := range m.InputModalities {
				if i > 0 {
					sm.Input += ","
				}
				sm.Input += in
			}
			if m.Cost.Input > 0 || m.Cost.Output > 0 {
				sm.Price = []float64{m.Cost.Input, m.Cost.Output, m.Cost.CacheRead, m.Cost.CacheWrite}
			}
			sp.Models[mid] = sm
		}
		doc.Providers[id] = sp
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
