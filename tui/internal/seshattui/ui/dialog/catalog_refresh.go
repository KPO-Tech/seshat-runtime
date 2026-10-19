package dialog

import (
	"context"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/util"
)

// catalogRefreshTimeout bounds one download of the models.dev catalog.
const catalogRefreshTimeout = time.Minute

// refreshCatalogCmd downloads the public models.dev catalog into the cache and tells the user how it went. It is the one thing the
// user starts by hand that uses the network for the catalog; the providers and models it adds are listed at the next start.
func refreshCatalogCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), catalogRefreshTimeout)
		defer cancel()
		before := len(sdk.CatalogProviders())
		if err := sdk.RefreshCatalog(ctx, sdk.CatalogCacheDir()); err != nil {
			return util.ReportError(err)()
		}
		after := len(sdk.CatalogProviders())
		return util.NewInfoMsg("Models catalog refreshed: " + strconv.Itoa(after) + " generic providers (" + strconv.Itoa(after-before) + " new). Restart to list them.")
	}
}
