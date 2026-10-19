package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultURL is where the public models.dev catalog is served.
const DefaultURL = "https://models.dev/api.json"

// minProviders is the fewest providers a fetched catalog may hold. Below it the body is an error page or a truncated file.
const minProviders = 100

// fetchTimeout bounds one download.
const fetchTimeout = 30 * time.Second

// The full models.dev format, of which only what Seshat reads is declared.
type fullProvider struct {
	Name   string               `json:"name"`
	NPM    string               `json:"npm"`
	API    string               `json:"api"`
	Env    []string             `json:"env"`
	Models map[string]fullModel `json:"models"`
}

type fullModel struct {
	Name      string `json:"name"`
	Reasoning bool   `json:"reasoning"`
	ToolCall  bool   `json:"tool_call"`
	Limit     struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Cost *struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
	Status      string `json:"status"`
	ReleaseDate string `json:"release_date"`
}

// ErrCatalogTooSmall is returned when a catalog has too few providers to be believed.
var ErrCatalogTooSmall = errors.New("the catalog has too few providers to be genuine")

// Parse reads a catalog in the models.dev format. Every provider of the result is Unverified: it came from outside this binary.
// Models that do not produce text or cannot call tools are left out, as an agent cannot use them.
func Parse(raw []byte, source string) (*Catalog, error) {
	var full map[string]fullProvider
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	if len(full) < minProviders {
		return nil, fmt.Errorf("%w: %d providers", ErrCatalogTooSmall, len(full))
	}
	cat := &Catalog{Providers: make(map[string]Provider, len(full)), Source: source}
	for id, fp := range full {
		p := Provider{ID: id, Name: fp.Name, BaseURL: strings.TrimSpace(fp.API), Env: fp.Env, Protocol: fp.NPM, Models: map[string]Model{}, Unverified: true}
		for mid, fm := range fp.Models {
			if !fm.ToolCall || !producesText(fm.Modalities.Output) {
				continue
			}
			m := Model{ID: mid, Name: fm.Name, ContextWindow: fm.Limit.Context, MaxOutput: fm.Limit.Output, Reasoning: fm.Reasoning, Status: fm.Status, ReleaseDate: fm.ReleaseDate}
			for _, in := range fm.Modalities.Input {
				if in != "text" {
					m.InputModalities = append(m.InputModalities, in)
				}
			}
			if fm.Cost != nil {
				m.Cost = Cost{Input: fm.Cost.Input, Output: fm.Cost.Output, CacheRead: fm.Cost.CacheRead, CacheWrite: fm.Cost.CacheWrite}
			}
			p.Models[mid] = m
		}
		if len(p.Models) > 0 {
			cat.Providers[id] = p
		}
	}
	return cat, nil
}

func producesText(outputs []string) bool {
	if len(outputs) == 0 {
		return true
	}
	for _, o := range outputs {
		if o == "text" {
			return true
		}
	}
	return false
}

// Fetch downloads the catalog from url (DefaultURL when empty). It sends nothing about the user: a plain GET.
func Fetch(ctx context.Context, client *http.Client, url string) (*Catalog, []byte, error) {
	if url == "" {
		url = DefaultURL
	}
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("fetch catalog: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("fetch catalog: %w", err)
	}
	cat, err := Parse(raw, url)
	if err != nil {
		return nil, nil, err
	}
	return cat, raw, nil
}
