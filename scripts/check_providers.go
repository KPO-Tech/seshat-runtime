//go:build ignore

// check_providers calls real providers with your own API keys, to see that the models of the catalog answer.
//
// Usage: go run scripts/check_providers.go [-all-models] [-limit N] [provider ...]
//
// A provider is a models.dev id (groq, zai, fireworks-ai...). With none, every provider whose key variable is set in the
// environment is checked. For each one a small model (the cheapest the catalog lists, or every model with -all-models up to -limit)
// gets a short question and the answer, the reasoning if any, and the error if any are printed. It costs a few tokens per call.
// Nothing is sent anywhere but the provider's own URL.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/catalog"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func main() {
	all := flag.Bool("all-models", false, "check every model of the provider instead of the cheapest one")
	limit := flag.Int("limit", 5, "with -all-models, how many models at most per provider")
	flag.Parse()

	cat := providers.Catalog()
	ids := flag.Args()
	if len(ids) == 0 {
		for id, p := range cat.Providers {
			if keyFor(p) != "" {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
	}
	if len(ids) == 0 {
		fmt.Println("no provider to check: pass ids, or set a key variable such as GROQ_API_KEY")
		return
	}
	failed := 0
	for _, id := range ids {
		p, ok := cat.Provider(id)
		if !ok {
			fmt.Printf("%-22s unknown to the catalog\n", id)
			failed++
			continue
		}
		key := keyFor(p)
		if key == "" {
			fmt.Printf("%-22s skipped: none of %v is set\n", id, p.Env)
			continue
		}
		for _, m := range pick(p, *all, *limit) {
			if err := check(id, p, key, m); err != nil {
				failed++
			}
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func keyFor(p catalog.Provider) string {
	for _, name := range p.Env {
		if !strings.HasSuffix(name, "KEY") && !strings.HasSuffix(name, "TOKEN") {
			continue
		}
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// pick returns the models to try: the cheapest one, or the first limit models, newest first.
func pick(p catalog.Provider, all bool, limit int) []catalog.Model {
	var models []catalog.Model
	for _, m := range p.SortedModels() {
		if !m.Deprecated() && m.ContextWindow > 0 {
			models = append(models, m)
		}
	}
	if len(models) == 0 {
		return nil
	}
	if all {
		if len(models) > limit {
			models = models[:limit]
		}
		return models
	}
	cheapest := models[0]
	for _, m := range models[1:] {
		if m.Cost.Output > 0 && (cheapest.Cost.Output == 0 || m.Cost.Output < cheapest.Cost.Output) {
			cheapest = m
		}
	}
	return []catalog.Model{cheapest}
}

func check(id string, p catalog.Provider, key string, m catalog.Model) error {
	provider := providers.ProviderForCatalogID(id)
	config := providers.GetProviderConfig(provider)
	if config == nil {
		if cfg, ok := providers.ConfigForCatalogProvider(provider); ok {
			config = cfg
		}
	}
	if config == nil {
		fmt.Printf("%-22s %-40s NOT CALLABLE: no configuration (URL %q, protocol %q)\n", id, m.ID, p.BaseURL, p.Protocol)
		return fmt.Errorf("no config")
	}
	config.APIKey = key
	client := providers.NewClientWithConfig(key, config)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := client.CreateMessage(ctx, types.APIRequest{
		Model:     types.ModelIdentifier{Provider: provider, Model: m.ID},
		Messages:  []types.Message{types.UserMessage("check", "Answer with the single word: pong")},
		MaxTokens: 512,
	})
	if err != nil {
		fmt.Printf("%-22s %-40s FAIL %v\n", id, m.ID, oneLine(err.Error()))
		return err
	}
	var text, thinking string
	for _, b := range resp.Content {
		switch c := b.(type) {
		case types.TextContent:
			text += c.Text
		case types.ThinkingContent:
			thinking += c.Thinking
		}
	}
	fmt.Printf("%-22s %-40s ok %4dms answer=%q reasoning=%d chars\n", id, m.ID, time.Since(start).Milliseconds(), oneLine(text), len(thinking))
	return nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
