package sdk

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/catalog"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
)

// CatalogCacheDir returns the directory the downloaded models.dev catalog is kept in, under the runtime root.
func CatalogCacheDir() string {
	return runtimepath.CacheDir("")
}

// CatalogProviderSummary describes a generic provider of the models.dev catalog: an OpenAI-compatible provider the runtime does not
// have built in, which it calls with the generic client.
type CatalogProviderSummary struct {
	// ID is the provider id to use in ModelIdentifier.Provider.
	ID         string
	Name       string
	BaseURL    string
	KeyEnvVars []string
	ModelCount int
	// Unverified is true for a provider that only a downloaded catalog knows. Its BaseURL comes from a file that was not reviewed:
	// show it to the user and have it confirmed before a key is sent, then build the ProviderConfig with that URL yourself.
	Unverified bool
}

// CatalogProviders lists the generic providers of the catalog, sorted by name. The embedded snapshot gives about 190; a refresh
// can add more.
func CatalogProviders() []CatalogProviderSummary {
	cat := providers.Catalog()
	if cat == nil {
		return nil
	}
	var out []CatalogProviderSummary
	for _, info := range providers.CatalogProviders() {
		cp, ok := cat.Provider(info.Name)
		if !ok {
			continue
		}
		out = append(out, CatalogProviderSummary{
			ID: cp.ID, Name: cp.Name, BaseURL: cp.BaseURL, KeyEnvVars: append([]string(nil), cp.Env...),
			ModelCount: len(info.Models), Unverified: cp.Unverified,
		})
	}
	return out
}

// LoadCatalogCache applies the catalog downloaded earlier, if there is one, over the embedded catalog. It never uses the network.
// NewClient does this itself when ClientConfig.CatalogCacheDir is set.
func LoadCatalogCache(dir string) error {
	cat, err := catalog.Load(dir)
	if err != nil {
		return err
	}
	providers.SetCatalog(cat)
	return nil
}

// RefreshCatalog downloads the public models.dev catalog into dir and applies it. It is the only call that uses the network, and it
// sends nothing about the user. A provider the download adds is Unverified (see CatalogProviderSummary).
func RefreshCatalog(ctx context.Context, dir string) error {
	return refreshCatalog(ctx, http.DefaultClient, "", dir)
}

func refreshCatalog(ctx context.Context, client *http.Client, url, dir string) error {
	if err := catalog.Refresh(ctx, client, url, dir); err != nil {
		return err
	}
	return LoadCatalogCache(dir)
}

// initCatalog applies the cached catalog and, when asked, refreshes it in the background when the cache is older than a day. A failure
// is logged and the catalog stays as it was: nothing here is needed for the client to work.
func initCatalog(config *ClientConfig) {
	if config.CatalogCacheDir == "" {
		return
	}
	if err := LoadCatalogCache(config.CatalogCacheDir); err != nil {
		slog.Warn("could not load the models catalog cache", "error", err)
	}
	if !config.RefreshCatalog {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		fetched, err := catalog.RefreshIfStale(ctx, http.DefaultClient, "", config.CatalogCacheDir)
		if err != nil {
			slog.Warn("could not refresh the models catalog", "error", err)
			return
		}
		if fetched {
			if err := LoadCatalogCache(config.CatalogCacheDir); err != nil {
				slog.Warn("could not apply the refreshed models catalog", "error", err)
			}
		}
	}()
}
