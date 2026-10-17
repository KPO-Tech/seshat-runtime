package sdk

import (
	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	"github.com/KPO-Tech/seshat-runtime/internal/memory/longterm"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
)

// newMemoryExtractor builds the extractor that stores what a closed session taught, or returns nil (an untyped nil, so
// the engine can test it) when the configuration does not ask for one or has nowhere to store it.
func newMemoryExtractor(config *ClientConfig, apiClient *providers.Client) engine.MemoryExtractor {
	if !config.ExtractMemories || config.LongTermMemory == nil || apiClient == nil {
		return nil
	}
	cfg := longterm.DefaultExtractorConfig()
	cfg.Model = config.MemoryExtractionModel
	if cfg.Model.Model == "" {
		cfg.Model = config.Model
	}
	return longterm.NewExtractor(config.LongTermMemory, apiClient, cfg)
}
