package sdk

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/memory/longterm"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func TestMemoryExtractionIsOffUnlessAskedForAndStorable(t *testing.T) {
	client := providers.NewClient("k", types.APIProviderOpenAI)
	// The store is only held, never used, by the builder: a nil database is enough.
	var store longterm.Store = longterm.NewSQLiteStore(nil)
	cases := []struct {
		name string
		cfg  ClientConfig
		want bool
	}{
		{"off by default", ClientConfig{LongTermMemory: store}, false},
		{"asked for, nowhere to store", ClientConfig{ExtractMemories: true}, false},
		{"asked for and storable", ClientConfig{ExtractMemories: true, LongTermMemory: store}, true},
	}
	for _, c := range cases {
		got := newMemoryExtractor(&c.cfg, client)
		if (got != nil) != c.want {
			t.Errorf("%s: extractor present = %v, want %v", c.name, got != nil, c.want)
		}
	}
}
