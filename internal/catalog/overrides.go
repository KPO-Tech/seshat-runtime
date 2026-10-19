package catalog

// endpointOverride is what we add by hand for a provider whose models.dev entry names a vendor SDK instead of an OpenAI-compatible
// endpoint, so the catalog gives no base URL. The provider is called with the generic OpenAI-compatible client at this URL.
type endpointOverride struct {
	BaseURL string
	// Source is the page of the provider's documentation that gives the URL, for whoever reviews the table.
	Source string
}

// endpointOverrides is the reviewed table. It is applied to the embedded catalog only, so a fetched catalog can never move a
// provider (see Merge), and it survives every refresh. TestOverridesAreStillNeeded fails when a rebuilt snapshot starts giving a
// URL for one of these providers: then compare the two and drop the line or fix it.
var endpointOverrides = map[string]endpointOverride{
	"groq":       {"https://api.groq.com/openai/v1", "https://console.groq.com/docs/openai"},
	"xai":        {"https://api.x.ai/v1", "https://docs.x.ai/docs/api-reference"},
	"cerebras":   {"https://api.cerebras.ai/v1", "https://inference-docs.cerebras.ai/resources/openai"},
	"togetherai": {"https://api.together.xyz/v1", "https://docs.together.ai/docs/openai-api-compatibility"},
	"deepinfra":  {"https://api.deepinfra.com/v1/openai", "https://deepinfra.com/docs/openai_api"},
	"cohere":     {"https://api.cohere.ai/compatibility/v1", "https://docs.cohere.com/docs/compatibility-api"},
}

// applyEndpointOverrides gives the providers of the table their URL and the OpenAI-compatible protocol.
func applyEndpointOverrides(c *Catalog) {
	for id, o := range endpointOverrides {
		p, ok := c.Providers[id]
		if !ok {
			continue
		}
		p.BaseURL = o.BaseURL
		p.Protocol = protocolOpenAICompatible
		c.Providers[id] = p
	}
}
