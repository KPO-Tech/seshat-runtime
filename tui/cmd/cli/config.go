package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/db"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	engineconfig "github.com/KPO-Tech/seshat-runtime/pkg/config"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// credentialKey constants — these are the keys used in the credentials table.
const (
	credKeyModel      = "model"
	credKeyAPIKey     = "api_key"
	credKeyBaseURL    = "provider_base_url"
	credKeyTavily     = "TAVILY_API_KEY"
	credKeyExa        = "EXA_API_KEY"
	credKeyJina       = "JINA_API_KEY"
	credKeyLangSearch = "LANGSEARCH_API_KEY"
	credKeySearXNG    = "SEARXNG_BASE_URL"
)

func runConfig(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	flags.SetOutput(stderr)

	providerFlag := flags.String("provider", "", "provider name")
	modelFlag := flags.String("model", "", "model identifier")
	apiKeyFlag := flags.String("api-key", "", "provider API key")
	baseURLFlag := flags.String("base-url", "", "custom base URL")
	cwdFlag := flags.String("cwd", "", "default working directory")
	dbFlag := flags.String("db", "", "SQLite database path")
	printFlag := flags.Bool("print", false, "print current config and exit")
	searchFlag := flags.Bool("search", false, "configure search tool API keys only")
	if err := flags.Parse(args); err != nil {
		return err
	}

	config, err := engineconfig.Load()
	if err != nil {
		return err
	}
	reader := bufio.NewReader(stdin)

	// Open the credentials DB.
	database, dbErr := openCredentialsDB(config)

	// Load saved secrets into the config view.
	if dbErr == nil {
		loadCredsIntoConfig(database, &config)
	}

	if *printFlag {
		printConfigSummary(stdout, config, resolveModel(config), database)
		return nil
	}

	if *searchFlag {
		return configureSearchKeys(reader, stdout, database, &config)
	}

	// ── Step 1: provider selection ────────────────────────────────────────────
	currentModel := resolveModel(config)
	provider := currentModel.Provider
	if value := strings.TrimSpace(*providerFlag); value != "" {
		provider = engineconfig.ResolveProvider(value)
		if provider == "" {
			return fmt.Errorf("unknown provider %q", value)
		}
	}
	if provider == "" {
		provider = sdk.DefaultClientConfig().Model.Provider
	}

	if strings.TrimSpace(*providerFlag) == "" {
		selected, selectErr := promptProvider(reader, stdout, provider)
		if selectErr != nil {
			return selectErr
		}
		provider = selected
	}

	providerInfo, ok := engineconfig.GetProviderInfo(provider)
	if !ok {
		return fmt.Errorf("provider %q is not available", provider)
	}

	// ── Step 2: model selection ───────────────────────────────────────────────
	selectedModel := strings.TrimSpace(*modelFlag)
	if selectedModel == "" {
		defaultModel := currentModel.Model
		if currentModel.Provider != provider || strings.TrimSpace(defaultModel) == "" {
			defaultModel = defaultModelForProvider(providerInfo)
		}
		selectedModel, err = promptModel(reader, stdout, providerInfo, defaultModel)
		if err != nil {
			return err
		}
	}
	selectedModel = strings.TrimSpace(selectedModel)
	if selectedModel == "" {
		return fmt.Errorf("model is required")
	}
	config.Model = fmt.Sprintf("%s:%s", provider, selectedModel)

	if value := strings.TrimSpace(*cwdFlag); value != "" {
		config.Cwd = value
	}
	if value := strings.TrimSpace(*dbFlag); value != "" {
		config.DBPath = value
	}

	// ── Step 3: provider credential fields ───────────────────────────────────
	for _, field := range providerInfo.SetupFields {
		override := fieldOverride(field.Key, *apiKeyFlag, *baseURLFlag)
		if override != "" {
			if err := saveCredential(database, field.Key, override); err != nil {
				return err
			}
			applyField(&config, field.Key, override)
			continue
		}

		current := currentFieldValue(config, field.Key)
		value, promptErr := promptField(reader, stdout, field, current)
		if promptErr != nil {
			return promptErr
		}
		applyField(&config, field.Key, value)
		if err := saveCredential(database, field.Key, value); err != nil {
			return err
		}
	}

	// ── Step 4: search keys ────────────────────────────────────────────────────
	if askYN(reader, stdout, "configure search tool API keys?") {
		if err := configureSearchKeys(reader, stdout, database, &config); err != nil {
			return err
		}
	}

	// ── Validate + save ────────────────────────────────────────────────────────
	if err := engineconfig.ValidateProviderSetup(config, provider); err != nil {
		return err
	}

	// Persist model selection to DB so it survives YAML resets.
	if err := saveCredential(database, credKeyModel, config.Model); err != nil {
		return err
	}

	// Strip runtime-only secrets from YAML before saving.
	yamlConfig := stripRuntimeSecrets(config)
	if err := engineconfig.Save(yamlConfig); err != nil {
		return err
	}
	engineconfig.ApplyRuntimeEnv(config)
	engineconfig.ApplySearchKeys(config)

	fmt.Fprintf(stdout, "\nsaved %s\n", engineconfig.DefaultConfigPath())
	printConfigSummary(stdout, config, resolveModel(config), database)
	return nil
}

// configureSearchKeys prompts for Tavily / Exa / Jina / LangSearch API keys.
func configureSearchKeys(reader *bufio.Reader, stdout io.Writer, database *db.DB, config *engineconfig.Config) error {
	fmt.Fprintln(stdout, "\n─── Search tool API keys ────────────────────────────────────────")
	fmt.Fprintln(stdout, "Leave blank to keep the current value. Enter \"-\" to clear.")
	fmt.Fprintln(stdout)

	// LangSearch has no Config struct field — use a local var and apply as env var.
	langSearchCurrent := os.Getenv("LANGSEARCH_API_KEY")

	fields := []struct {
		credKey string
		label   string
		envVar  string
		current *string
	}{
		{credKeyTavily, "Tavily API key", "TAVILY_API_KEY", &config.TavilyAPIKey},
		{credKeyExa, "Exa API key", "EXA_API_KEY", &config.ExaAPIKey},
		{credKeyJina, "Jina AI API key", "JINA_API_KEY", &config.JinaAPIKey},
		{credKeyLangSearch, "LangSearch API key", "LANGSEARCH_API_KEY", &langSearchCurrent},
	}

	for _, f := range fields {
		current := strings.TrimSpace(*f.current)
		if current == "" {
			fmt.Fprintf(stdout, "%s (%s): ", f.label, f.envVar)
		} else {
			fmt.Fprintf(stdout, "%s (%s) [configured]: ", f.label, f.envVar)
		}

		val, err := readLine(context.Background(), reader)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		val = strings.TrimSpace(val)
		if val == "-" {
			if database != nil {
				_ = database.DeleteCredential(context.Background(), f.credKey)
			}
			*f.current = ""
			continue
		}
		if val == "" {
			continue // keep current
		}
		*f.current = val
		if err := saveCredential(database, f.credKey, val); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, "search keys saved.")
	return nil
}

// ─── Display ──────────────────────────────────────────────────────────────────

func printConfigSummary(out io.Writer, config engineconfig.Config, model sdk.ModelIdentifier, database *db.DB) {
	fmt.Fprintf(out, "\n─── Current configuration ───────────────────────────────────────\n")
	fmt.Fprintf(out, "  provider        : %s\n", model.Provider)
	fmt.Fprintf(out, "  model           : %s\n", model.Model)
	fmt.Fprintf(out, "  config file     : %s\n", engineconfig.DefaultConfigPath())
	if strings.TrimSpace(config.Cwd) != "" && config.Cwd != "." {
		fmt.Fprintf(out, "  working dir     : %s\n", config.Cwd)
	}
	if strings.TrimSpace(config.DBPath) != "" {
		fmt.Fprintf(out, "  database        : %s\n", config.DBPath)
	}

	// Provider API key status
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey != "" {
		masked := maskSecret(apiKey)
		fmt.Fprintf(out, "  api key         : %s\n", masked)
	} else {
		fmt.Fprintf(out, "  api key         : (not configured)\n")
	}

	// Search keys (check DB)
	fmt.Fprintf(out, "\n─── Search tool keys ────────────────────────────────────────────\n")
	searchFields := []struct{ label, val string }{
		{"tavily", config.TavilyAPIKey},
		{"exa", config.ExaAPIKey},
		{"jina", config.JinaAPIKey},
		{"langsearch", os.Getenv("LANGSEARCH_API_KEY")},
	}
	anySearch := false
	for _, f := range searchFields {
		if strings.TrimSpace(f.val) != "" {
			fmt.Fprintf(out, "  %-16s: %s\n", f.label, maskSecret(f.val))
			anySearch = true
		}
	}
	if !anySearch {
		fmt.Fprintf(out, "  (none configured — run `seshat config --search` to add)\n")
	}

	if database != nil {
		keys, _ := database.ListCredentialKeys(context.Background())
		if len(keys) > 0 {
			fmt.Fprintf(out, "\n  credentials stored in SQLite: %d key(s)\n", len(keys))
		}
	}
	fmt.Fprintln(out)
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

// ─── Prompts ──────────────────────────────────────────────────────────────────

func promptProvider(reader *bufio.Reader, stdout io.Writer, current sdk.APIProvider) (sdk.APIProvider, error) {
	providers := engineconfig.AvailableProviders()
	fmt.Fprintln(stdout, "Available providers:")
	defaultIndex := 1
	for index, provider := range providers {
		marker := " "
		if provider.Name == current {
			marker = "*"
			defaultIndex = index + 1
		}
		fmt.Fprintf(stdout, "  %d. [%s] %-14s — %s\n", index+1, marker, provider.DisplayName, provider.Description)
	}

	for {
		fmt.Fprintf(stdout, "provider [%d]: ", defaultIndex)
		value, err := readLine(context.Background(), reader)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if strings.TrimSpace(value) == "" {
			return providers[defaultIndex-1].Name, nil
		}
		if index, convErr := strconv.Atoi(value); convErr == nil && index >= 1 && index <= len(providers) {
			return providers[index-1].Name, nil
		}
		if resolved := engineconfig.ResolveProvider(value); resolved != "" {
			return resolved, nil
		}
		fmt.Fprintln(stdout, "invalid selection — enter a number or provider name")
	}
}

func promptModel(reader *bufio.Reader, stdout io.Writer, provider engineconfig.ProviderInfo, current string) (string, error) {
	fmt.Fprintf(stdout, "Models for %s:\n", provider.DisplayName)
	for index, model := range provider.Models {
		suffix := ""
		if model.Description != "" {
			suffix = " — " + model.Description
		}
		fmt.Fprintf(stdout, "  %d. %s%s\n", index+1, model.Identifier, suffix)
	}

	defaultModel := strings.TrimSpace(current)
	if defaultModel == "" {
		defaultModel = defaultModelForProvider(provider)
	}

	fmt.Fprintf(stdout, "model [%s]: ", defaultModel)
	value, err := readLine(context.Background(), reader)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return defaultModel, nil
	}
	if index, convErr := strconv.Atoi(value); convErr == nil && index >= 1 && index <= len(provider.Models) {
		return provider.Models[index-1].Identifier, nil
	}
	return strings.TrimSpace(value), nil
}

func promptField(reader *bufio.Reader, stdout io.Writer, field engineconfig.ProviderSetupField, current string) (string, error) {
	label := field.Label
	if field.EnvVar != "" {
		label = fmt.Sprintf("%s (%s)", label, field.EnvVar)
	}
	if field.Description != "" {
		fmt.Fprintf(stdout, "%s\n", field.Description)
	}

	for {
		if strings.TrimSpace(current) != "" {
			if field.Secret {
				fmt.Fprintf(stdout, "%s [configured]: ", label)
			} else {
				fmt.Fprintf(stdout, "%s [%s]: ", label, current)
			}
		} else {
			fmt.Fprintf(stdout, "%s: ", label)
		}

		value, err := readLine(context.Background(), reader)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = current
		}
		if field.Required && strings.TrimSpace(value) == "" {
			fmt.Fprintln(stdout, "value required")
			continue
		}
		return value, nil
	}
}

func askYN(reader *bufio.Reader, stdout io.Writer, question string) bool {
	fmt.Fprintf(stdout, "%s [y/N]: ", question)
	val, _ := readLine(context.Background(), reader)
	return strings.EqualFold(strings.TrimSpace(val), "y")
}

// ─── Credential helpers ───────────────────────────────────────────────────────

func openCredentialsDB(config engineconfig.Config) (*db.DB, error) {
	dbPath := engineconfig.EffectiveSessionDBPath(config)
	database, err := db.Open(context.Background(), db.DefaultSQLiteConfig(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open credentials database: %w", err)
	}
	return database, nil
}

func saveCredential(database *db.DB, key, value string) error {
	if database == nil || strings.TrimSpace(value) == "" {
		return nil
	}
	return database.UpsertCredential(context.Background(), key, value)
}

func loadCredsIntoConfig(database *db.DB, config *engineconfig.Config) {
	if database == nil {
		return
	}
	ctx := context.Background()
	loadCred := func(key string) string {
		val, ok, _ := database.GetCredential(ctx, key)
		if !ok {
			return ""
		}
		return val
	}
	// loadCredScoped checks the per-provider scoped key first (written by the
	// TUI config panel), then falls back to the global key (written by `seshat config`).
	loadCredScoped := func(fieldKey, providerID string) string {
		if providerID != "" {
			// Normalize providerID to ensure consistent lookups (e.g. "zai" -> "z-ai").
			normalized := string(engineconfig.ResolveProvider(providerID))
			if normalized == "" {
				normalized = strings.ToLower(providerID)
			}
			if v := loadCred(fieldKey + ":" + normalized); v != "" {
				return v
			}
		}
		return loadCred(fieldKey)
	}

	// Load persisted model selection from DB when YAML has none.
	// This is the primary source of truth for which provider is "active".
	if strings.TrimSpace(config.Model) == "" {
		if v := loadCred(credKeyModel); v != "" {
			config.Model = v
		}
	}

	// Still no model: take the first provider (catalog order) whose key is in the
	// credentials DB, on its default model, unless an environment variable already
	// names a provider. A user who added a key from the interface but never picked
	// a model otherwise starts on nothing and is told to configure a provider.
	if strings.TrimSpace(config.Model) == "" {
		if _, fromEnv := engineconfig.EffectiveAPIKeyAndProvider(*config); fromEnv == "" {
			for _, provider := range engineconfig.AvailableProviders() {
				if loadCred(credKeyAPIKey+":"+string(provider.Name)) == "" {
					continue
				}
				if def, ok := providers.DefaultModelIdentifier(provider.Name); ok {
					config.Model = string(provider.Name) + ":" + def.Model
					break
				}
			}
		}
	}

	// Determine the active provider from the config model string.
	// We use the raw model string to avoid circularity in resolveModel.
	activeProvider := ""
	if m := engineconfig.ParseModelIdentifier(config.Model); m.Provider != "" {
		activeProvider = string(m.Provider)
	}
	if activeProvider == "" {
		activeProvider = string(engineconfig.DetectProviderFromModel(config.Model))
	}

	if v := loadCredScoped(credKeyAPIKey, activeProvider); v != "" {
		config.APIKey = v
	}
	if v := loadCredScoped(credKeyBaseURL, activeProvider); v != "" {
		config.ProviderBaseURL = v
	}
	config.TavilyAPIKey = loadCred(credKeyTavily)
	config.ExaAPIKey = loadCred(credKeyExa)
	config.JinaAPIKey = loadCred(credKeyJina)

	if config.WebSearchProvider == "" {
		config.WebSearchProvider = loadCred("WEB_SEARCH_PROVIDER")
	}

	// LangSearch and SearXNG have no Config struct field — apply directly as env vars.
	if v := loadCred(credKeyLangSearch); v != "" && os.Getenv("LANGSEARCH_API_KEY") == "" {
		os.Setenv("LANGSEARCH_API_KEY", v)
	}
	if v := loadCred(credKeySearXNG); v != "" && os.Getenv("SEARXNG_BASE_URL") == "" {
		os.Setenv("SEARXNG_BASE_URL", v)
	}
}

func stripRuntimeSecrets(config engineconfig.Config) engineconfig.Config {
	// Strip secrets — stored in the DB, never in YAML.
	config.APIKey = ""
	config.ProviderBaseURL = ""
	config.TavilyAPIKey = ""
	config.ExaAPIKey = ""
	config.JinaAPIKey = ""
	// RuntimeRoot is always re-computed at startup from SESHAT_RUNTIME_ROOT or
	// the XDG default (~/.config/seshat-cli). Never persist it so the YAML stays
	// portable and doesn't hard-code absolute paths.
	config.RuntimeRoot = ""
	return config
}

// ─── Field mapping helpers ────────────────────────────────────────────────────

func fieldOverride(key, apiKey, baseURL string) string {
	switch key {
	case "api_key":
		return strings.TrimSpace(apiKey)
	case "provider_base_url":
		return strings.TrimSpace(baseURL)
	default:
		return ""
	}
}

func currentFieldValue(config engineconfig.Config, key string) string {
	switch key {
	case "api_key":
		return strings.TrimSpace(config.APIKey)
	case "provider_base_url":
		return strings.TrimSpace(config.ProviderBaseURL)
	default:
		return ""
	}
}

func applyField(config *engineconfig.Config, key, value string) {
	switch key {
	case "api_key":
		config.APIKey = value
	case "provider_base_url":
		config.ProviderBaseURL = value
	}
}

// defaultModelForProvider returns the default model of a provider: the first of
// its registry list. The catalog view in pkg/config sorts the models by name, so
// its first entry is not the default.
func defaultModelForProvider(provider engineconfig.ProviderInfo) string {
	if def, ok := providers.DefaultModelIdentifier(provider.Name); ok {
		return def.Model
	}
	if len(provider.Models) > 0 {
		return provider.Models[0].Identifier
	}
	return ""
}
