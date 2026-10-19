//go:build ignore

// build_catalog_snapshot rebuilds internal/catalog/snapshot.json.gz, the models.dev catalog embedded in the binary.
//
// Usage: go run scripts/build_catalog_snapshot.go <api.json | https://models.dev/api.json> [out.json.gz]
//
// It is the whole of keeping the model lists up to date: run it with the public URL, look at what changed (the provider tests
// show the models that appeared), commit. It uses the same parser as the runtime's own refresh, so what is embedded and what
// is downloaded are read the same way. The provider ids, base URLs and key variables of the file it writes are the trusted ones: a
// catalog downloaded later never overrides them (see internal/catalog/merge.go), so read the diff of those before committing.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/catalog"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/build_catalog_snapshot.go <api.json | url> [out.json.gz]")
		os.Exit(2)
	}
	out := "internal/catalog/snapshot.json.gz"
	if len(os.Args) > 2 {
		out = os.Args[2]
	}
	raw, err := read(os.Args[1])
	if err != nil {
		fail(err)
	}
	cat, err := catalog.Parse(raw, os.Args[1])
	if err != nil {
		fail(err)
	}
	// What is embedded is verified: it is reviewed when this file is committed.
	for id, p := range cat.Providers {
		p.Unverified = false
		cat.Providers[id] = p
	}
	data, err := catalog.Encode(cat)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fail(err)
	}
	models := 0
	for _, p := range cat.Providers {
		models += len(p.Models)
	}
	fmt.Printf("%d providers, %d models, %d bytes compressed -> %s\n", len(cat.Providers), models, len(data), out)
}

func read(source string) ([]byte, error) {
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return os.ReadFile(source)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", source, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
