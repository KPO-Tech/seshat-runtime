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
	extractor := longterm.NewExtractor(config.LongTermMemory, apiClient, cfg)
	if store, ok := config.LongTermMemory.(longterm.Consolidatable); ok && config.ConsolidateMemories {
		extractor.SetConsolidator(longterm.NewConsolidator(store, apiClient, longterm.ConsolidatorConfig{Model: cfg.Model}))
	}
	return extractor
}

// newMemoryRecaller builds the recaller that puts long-term memory in the prompt of a new session, or returns nil (an
// untyped nil) when the configuration does not ask for one or the store cannot list its entities.
func newMemoryRecaller(config *ClientConfig) engine.MemoryRecaller {
	if !config.RecallMemories || config.LongTermMemory == nil {
		return nil
	}
	lister, ok := config.LongTermMemory.(longterm.Lister)
	if !ok {
		return nil
	}
	return longterm.NewRecaller(lister, 0)
}
