package catalog

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// maxCatalogBytes bounds what is read from the embedded file, the cache or the network once decompressed.
const maxCatalogBytes = 64 << 20

// snapshotFile is the trimmed catalog embedded in the binary. Rebuild it with scripts/build_catalog_snapshot.go.
//
//go:embed snapshot.json.gz
var snapshotFile []byte

// The compact form of the snapshot, as scripts/build_catalog_snapshot.go writes it.
type snapshotDoc struct {
	Version   int                         `json:"v"`
	Providers map[string]snapshotProvider `json:"providers"`
}

type snapshotProvider struct {
	Name     string                   `json:"n"`
	API      string                   `json:"a,omitempty"`
	Env      []string                 `json:"e,omitempty"`
	Protocol string                   `json:"k,omitempty"`
	Models   map[string]snapshotModel `json:"m"`
}

type snapshotModel struct {
	Name    string `json:"n"`
	Context int    `json:"c,omitempty"`
	Output  int    `json:"o,omitempty"`
	Input   string `json:"i,omitempty"`
	Reason  int    `json:"r,omitempty"`
	// Price is [input, output, cache read, cache write, reasoning, input audio, output audio]; the last five may be missing.
	Price  []float64 `json:"p,omitempty"`
	Status string    `json:"s,omitempty"`
	Date   string    `json:"d,omitempty"`
	// Added in version 2; absent in version 1.
	MaxInput    int    `json:"li,omitempty"`
	Attachment  int    `json:"at,omitempty"`
	Structured  int    `json:"so,omitempty"`
	NoTemp      int    `json:"nt,omitempty"`
	Interleaved string `json:"il,omitempty"`
	OpenWeights int    `json:"ow,omitempty"`
	Knowledge   string `json:"kn,omitempty"`
	Updated     string `json:"lu,omitempty"`
}

// snapshotVersion is the version of the compact form this package writes. It reads versions 1 and 2.
const snapshotVersion = 2

var (
	embeddedOnce sync.Once
	embedded     *Catalog
	embeddedErr  error
)

// Embedded returns the catalog built into the binary. It is read once. Every provider in it is verified.
func Embedded() (*Catalog, error) {
	embeddedOnce.Do(func() {
		embedded, embeddedErr = decodeSnapshot(snapshotFile, "embedded", false)
	})
	return embedded, embeddedErr
}

// decodeSnapshot reads the compact gzip form. unverified marks every provider as coming from an untrusted source.
func decodeSnapshot(data []byte, source string, unverified bool) (*Catalog, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, maxCatalogBytes))
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	if doc.Version != 1 && doc.Version != snapshotVersion {
		return nil, fmt.Errorf("unsupported catalog version %d", doc.Version)
	}
	return fromSnapshot(doc, source, unverified), nil
}

func fromSnapshot(doc snapshotDoc, source string, unverified bool) *Catalog {
	cat := &Catalog{Providers: make(map[string]Provider, len(doc.Providers)), Source: source}
	for id, sp := range doc.Providers {
		p := Provider{
			ID: id, Name: sp.Name, BaseURL: sp.API, Env: sp.Env, Protocol: sp.Protocol,
			Models: make(map[string]Model, len(sp.Models)), Unverified: unverified,
		}
		for mid, sm := range sp.Models {
			m := Model{
				ID: mid, Name: sm.Name, ContextWindow: sm.Context, MaxInput: sm.MaxInput, MaxOutput: sm.Output, Reasoning: sm.Reason == 1,
				Attachment: sm.Attachment == 1, StructuredOutput: sm.Structured == 1, NoTemperature: sm.NoTemp == 1, Interleaved: sm.Interleaved,
				OpenWeights: sm.OpenWeights == 1, Knowledge: sm.Knowledge, Status: sm.Status, ReleaseDate: sm.Date, LastUpdated: sm.Updated,
			}
			if sm.Input != "" {
				m.InputModalities = strings.Split(sm.Input, ",")
			}
			if len(sm.Price) >= 2 {
				m.Cost = Cost{Input: sm.Price[0], Output: sm.Price[1]}
				if len(sm.Price) > 2 {
					m.Cost.CacheRead = sm.Price[2]
				}
				if len(sm.Price) > 3 {
					m.Cost.CacheWrite = sm.Price[3]
				}
				if len(sm.Price) > 4 {
					m.Cost.Reasoning = sm.Price[4]
				}
				if len(sm.Price) > 6 {
					m.Cost.InputAudio, m.Cost.OutputAudio = sm.Price[5], sm.Price[6]
				}
			}
			p.Models[mid] = m
		}
		cat.Providers[id] = p
	}
	return cat
}
