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
	Name             string `json:"name"`
	Reasoning        bool   `json:"reasoning"`
	ToolCall         bool   `json:"tool_call"`
	Attachment       bool   `json:"attachment"`
	StructuredOutput bool   `json:"structured_output"`
	// Temperature is a pointer: absent means the catalog does not say.
	Temperature *bool `json:"temperature"`
	// Interleaved is true, false, or an object {"field": "reasoning_content"}.
	Interleaved json.RawMessage `json:"interleaved"`
	OpenWeights bool            `json:"open_weights"`
	Knowledge   string          `json:"knowledge"`
	LastUpdated string          `json:"last_updated"`
	Limit       struct {
		Context int `json:"context"`
		Input   int `json:"input"`
		Output  int `json:"output"`
	} `json:"limit"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Cost *struct {
		Input       float64 `json:"input"`
		Output      float64 `json:"output"`
		CacheRead   float64 `json:"cache_read"`
		CacheWrite  float64 `json:"cache_write"`
		Reasoning   float64 `json:"reasoning"`
		InputAudio  float64 `json:"input_audio"`
		OutputAudio float64 `json:"output_audio"`
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
			m := Model{
				ID: mid, Name: fm.Name, ContextWindow: fm.Limit.Context, MaxInput: fm.Limit.Input, MaxOutput: fm.Limit.Output,
				Reasoning: fm.Reasoning, Attachment: fm.Attachment, StructuredOutput: fm.StructuredOutput,
				NoTemperature: fm.Temperature != nil && !*fm.Temperature, Interleaved: interleavedField(fm.Interleaved),
				OpenWeights: fm.OpenWeights, Knowledge: fm.Knowledge, Status: fm.Status, ReleaseDate: fm.ReleaseDate, LastUpdated: fm.LastUpdated,
			}
			for _, in := range fm.Modalities.Input {
				if in != "text" {
					m.InputModalities = append(m.InputModalities, in)
				}
			}
			if fm.Cost != nil {
				m.Cost = Cost{
					Input: fm.Cost.Input, Output: fm.Cost.Output, CacheRead: fm.Cost.CacheRead, CacheWrite: fm.Cost.CacheWrite,
					Reasoning: fm.Cost.Reasoning, InputAudio: fm.Cost.InputAudio, OutputAudio: fm.Cost.OutputAudio,
				}
			}
			p.Models[mid] = m
		}
		if len(p.Models) > 0 {
			cat.Providers[id] = p
		}
	}
	return cat, nil
}

// interleavedField reads the "interleaved" value of a model: true, false, or an object with the name of the field that carries the
// reasoning. It returns "true" for true without a field, the field name for an object, and "" otherwise.
func interleavedField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var flag bool
	if err := json.Unmarshal(raw, &flag); err == nil {
		if flag {
			return "true"
		}
		return ""
	}
	var obj struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Field != "" {
		return obj.Field
	}
	return ""
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
