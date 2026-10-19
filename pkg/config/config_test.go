package config

import (
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseModelIdentifierKeepsColonModelIDs(t *testing.T) {
	model := ParseModelIdentifier("qwen2.5-coder:7b")
	if model.Provider != sdk.APIProviderAnthropic {
		t.Fatalf("unexpected provider: got %q", model.Provider)
	}
	if model.Model != "qwen2.5-coder:7b" {
		t.Fatalf("unexpected model: got %q", model.Model)
	}
}

func TestHasExplicitProviderPrefix(t *testing.T) {
	if !HasExplicitProviderPrefix("openai:gpt-5.5") {
		t.Fatalf("expected explicit provider prefix")
	}
	if !HasExplicitProviderPrefix("deepseek:deepseek-chat") {
		t.Fatalf("expected explicit deepseek provider prefix")
	}
	if HasExplicitProviderPrefix("qwen2.5-coder:7b") {
		t.Fatalf("unexpected provider prefix for raw model id")
	}
}

func TestParseModelIdentifierRecognizesOpenAICompatibleAdditions(t *testing.T) {
	model := ParseModelIdentifier("deepseek:deepseek-chat")
	if model.Provider != sdk.APIProviderDeepSeek {
		t.Fatalf("unexpected provider: got %q", model.Provider)
	}
	if model.Model != "deepseek-chat" {
		t.Fatalf("unexpected model: got %q", model.Model)
	}
}

func TestResolveAPIKeyUsesProviderSpecificEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	key := ResolveAPIKey(Config{APIKey: "fallback"}, sdk.APIProviderOpenAI)
	if key != "openai-key" {
		t.Fatalf("unexpected key: got %q", key)
	}
}

func TestApplyRuntimeEnvFromConfig(t *testing.T) {
	_ = os.Unsetenv("SESHAT_RUNTIME_ROOT")

	cfg := Config{RuntimeRoot: "/tmp/seshat-runtime"}
	ApplyRuntimeEnv(cfg)

	if got := os.Getenv("SESHAT_RUNTIME_ROOT"); got != filepath.FromSlash("/tmp/seshat-runtime") {
		t.Fatalf("unexpected runtime root env: got %q", got)
	}
}

func TestSaveAtWritesLoadableConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".seshat.yaml")

	cfg := Config{
		Model:           "openai:gpt-5.5",
		APIKey:          "secret",
		ProviderBaseURL: "https://example.invalid",
	}

	if err := SaveAt(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if len(content) == 0 {
		t.Fatalf("expected config file content")
	}
}

func TestLoadIntoBindsBrowserRuntimeEnv(t *testing.T) {
	t.Setenv("SESHAT_BROWSER_REMOTE_CONTROL_URL", "ws://127.0.0.1:9222/devtools/browser/test")
	t.Setenv("SESHAT_BROWSER_EXECUTABLE_PATH", "/usr/bin/chromium")

	var cfg Config
	if err := LoadInto(&cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.BrowserRemoteControlURL != "ws://127.0.0.1:9222/devtools/browser/test" {
		t.Fatalf("unexpected browser remote url: %q", cfg.BrowserRemoteControlURL)
	}
	if cfg.BrowserExecutablePath != "/usr/bin/chromium" {
		t.Fatalf("unexpected browser executable path: %q", cfg.BrowserExecutablePath)
	}
}

func TestLoadIntoBindsTitleModelEnv(t *testing.T) {
	t.Setenv("SESHAT_TITLE_MODEL", "ollama:llama3.2:1b")
	t.Setenv("SESHAT_TITLE_MODEL_BASE_URL", "http://localhost:11434")
	t.Setenv("SESHAT_TITLE_MODEL_API_KEY", "title-key")
	t.Setenv("SESHAT_TITLE_LOCAL_ENABLED", "true")
	t.Setenv("SESHAT_TITLE_LOCAL_RUNTIME", "llama.cpp")
	t.Setenv("SESHAT_TITLE_LOCAL_EXECUTABLE", "llama-cli")
	t.Setenv("SESHAT_TITLE_LOCAL_MODEL_PATH", "/models/title.gguf")
	t.Setenv("SESHAT_TITLE_LOCAL_HF_REPO", "Qwen/Qwen2.5-0.5B-Instruct-GGUF")
	t.Setenv("SESHAT_TITLE_LOCAL_HF_FILE", "qwen2.5-0.5b-instruct-q4_k_m.gguf")
	t.Setenv("SESHAT_TITLE_LOCAL_CACHE_DIR", "/cache/title")
	t.Setenv("SESHAT_TITLE_LOCAL_TIMEOUT", "20s")

	var cfg Config
	if err := LoadInto(&cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.TitleModel != "ollama:llama3.2:1b" {
		t.Fatalf("unexpected title model: %q", cfg.TitleModel)
	}
	if cfg.TitleModelBaseURL != "http://localhost:11434" {
		t.Fatalf("unexpected title model base url: %q", cfg.TitleModelBaseURL)
	}
	if cfg.TitleModelAPIKey != "title-key" {
		t.Fatalf("unexpected title model api key: %q", cfg.TitleModelAPIKey)
	}
	if !cfg.TitleLocalEnabled {
		t.Fatal("expected local title enabled")
	}
	if cfg.TitleLocalRuntime != "llama.cpp" || cfg.TitleLocalExe != "llama-cli" {
		t.Fatalf("unexpected local title runtime: %q exe=%q", cfg.TitleLocalRuntime, cfg.TitleLocalExe)
	}
	if cfg.TitleLocalModel != "/models/title.gguf" {
		t.Fatalf("unexpected local title model path: %q", cfg.TitleLocalModel)
	}
	if cfg.TitleLocalHFRepo != "Qwen/Qwen2.5-0.5B-Instruct-GGUF" {
		t.Fatalf("unexpected local title hf repo: %q", cfg.TitleLocalHFRepo)
	}
	if cfg.TitleLocalHFFile != "qwen2.5-0.5b-instruct-q4_k_m.gguf" {
		t.Fatalf("unexpected local title hf file: %q", cfg.TitleLocalHFFile)
	}
	if cfg.TitleLocalCache != "/cache/title" {
		t.Fatalf("unexpected local title cache dir: %q", cfg.TitleLocalCache)
	}
	if cfg.TitleLocalTimeout != "20s" {
		t.Fatalf("unexpected local title timeout: %q", cfg.TitleLocalTimeout)
	}
}

func TestLoadIntoBindsStorageGCEnv(t *testing.T) {
	t.Setenv("SESHAT_STORAGE_GC_ENABLED", "true")
	t.Setenv("SESHAT_STORAGE_GC_INTERVAL", "30m")
	t.Setenv("SESHAT_STORAGE_GC_LIMIT", "128")
	t.Setenv("SESHAT_STORAGE_GC_NAMESPACES", "artifacts/web,artifacts/browser/downloads")

	var cfg Config
	if err := LoadInto(&cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if !cfg.StorageGCEnabled {
		t.Fatal("expected storage GC enabled")
	}
	if cfg.StorageGCInterval != "30m" {
		t.Fatalf("unexpected storage gc interval: %q", cfg.StorageGCInterval)
	}
	if cfg.StorageGCLimit != 128 {
		t.Fatalf("unexpected storage gc limit: %d", cfg.StorageGCLimit)
	}
	if cfg.StorageGCNamespaces != "artifacts/web,artifacts/browser/downloads" {
		t.Fatalf("unexpected storage gc namespaces: %q", cfg.StorageGCNamespaces)
	}
}

func TestEffectiveSessionDBPathFallsBackToDBPath(t *testing.T) {
	cfg := Config{DBPath: "/tmp/backend.sqlite"}
	if got := EffectiveSessionDBPath(cfg); got != "/tmp/backend.sqlite" {
		t.Fatalf("unexpected session db path: %q", got)
	}
}

func TestEffectiveRuntimePathsUseUnifiedRoot(t *testing.T) {
	cfg := Config{RuntimeRoot: "/tmp/seshat-runtime"}
	runtimeRoot := filepath.FromSlash("/tmp/seshat-runtime")
	if got := EffectiveRuntimeRoot(cfg); got != runtimeRoot {
		t.Fatalf("unexpected runtime root: %q", got)
	}
	if got := EffectiveDBPath(cfg); got != filepath.Join(runtimeRoot, "seshat.db") {
		t.Fatalf("unexpected db path: %q", got)
	}
	if got := EffectiveStorageLocalPath(cfg); got != filepath.Join(runtimeRoot, "storage") {
		t.Fatalf("unexpected storage path: %q", got)
	}
}

func TestEffectiveSessionDBPathPrefersExplicitSessionPath(t *testing.T) {
	cfg := Config{
		DBPath:        "/tmp/backend.sqlite",
		SessionDBPath: "/tmp/runtime.sqlite",
	}
	if got := EffectiveSessionDBPath(cfg); got != "/tmp/runtime.sqlite" {
		t.Fatalf("unexpected session db path: %q", got)
	}
}

func TestLoadIntoBindsBackendAndSessionDBEnv(t *testing.T) {
	t.Setenv("SESHAT_DB_DRIVER", "postgres")
	t.Setenv("SESHAT_DB_DSN", "postgres://user:pass@localhost:5432/seshat")
	t.Setenv("SESHAT_DB_AUTO_MIGRATE", "false")
	t.Setenv("SESHAT_SESSION_DB_PATH", "/tmp/runtime.sqlite")

	var cfg Config
	if err := LoadInto(&cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.DBDriver != "postgres" {
		t.Fatalf("unexpected db driver: %q", cfg.DBDriver)
	}
	if cfg.DBDSN != "postgres://user:pass@localhost:5432/seshat" {
		t.Fatalf("unexpected db dsn: %q", cfg.DBDSN)
	}
	if cfg.DBAutoMigrate {
		t.Fatal("expected db_auto_migrate=false from env")
	}
	if cfg.SessionDBPath != "/tmp/runtime.sqlite" {
		t.Fatalf("unexpected session db path: %q", cfg.SessionDBPath)
	}
}

func TestLoadIntoBindsPgVectorEnv(t *testing.T) {
	t.Setenv("SESHAT_PGVECTOR_CREATE_EXTENSION", "false")
	t.Setenv("SESHAT_PGVECTOR_DSN", "postgres://vector:test@localhost:5432/vectors")
	t.Setenv("SESHAT_PGVECTOR_INDEX_METHOD", "ivfflat")
	t.Setenv("SESHAT_PGVECTOR_HNSW_M", "24")
	t.Setenv("SESHAT_PGVECTOR_HNSW_EF_CONSTRUCTION", "96")
	t.Setenv("SESHAT_PGVECTOR_IVFFLAT_LISTS", "256")

	var cfg Config
	if err := LoadInto(&cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.PgVectorCreateExtension {
		t.Fatal("expected pgvector_create_extension=false from env")
	}
	if cfg.PgVectorIndexMethod != "ivfflat" {
		t.Fatalf("unexpected pgvector index method: %q", cfg.PgVectorIndexMethod)
	}
	if cfg.PgVectorDSN != "postgres://vector:test@localhost:5432/vectors" {
		t.Fatalf("unexpected pgvector dsn: %q", cfg.PgVectorDSN)
	}
	if cfg.PgVectorHNSWM != 24 {
		t.Fatalf("unexpected pgvector hnsw m: %d", cfg.PgVectorHNSWM)
	}
	if cfg.PgVectorHNSWEF != 96 {
		t.Fatalf("unexpected pgvector hnsw ef construction: %d", cfg.PgVectorHNSWEF)
	}
	if cfg.PgVectorIVFFlatLists != 256 {
		t.Fatalf("unexpected pgvector ivfflat lists: %d", cfg.PgVectorIVFFlatLists)
	}
}

func TestLoadIntoBindsOpenSearchEnv(t *testing.T) {
	t.Setenv("SESHAT_VECTOR_STORE", "opensearch")
	t.Setenv("OPENSEARCH_ADDRESSES", "https://search-1.local:9200,https://search-2.local:9200")
	t.Setenv("OPENSEARCH_USERNAME", "seshat")
	t.Setenv("OPENSEARCH_PASSWORD", "secret")
	t.Setenv("OPENSEARCH_API_KEY", "api-key")
	t.Setenv("OPENSEARCH_INDEX_PREFIX", "company-rag")
	t.Setenv("OPENSEARCH_CREATE_INDEX", "false")
	t.Setenv("OPENSEARCH_KNN", "true")
	t.Setenv("OPENSEARCH_INSECURE_SKIP_VERIFY", "true")

	var cfg Config
	if err := LoadInto(&cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.VectorStore != "opensearch" {
		t.Fatalf("unexpected vector store: %q", cfg.VectorStore)
	}
	if cfg.OpenSearchAddresses != "https://search-1.local:9200,https://search-2.local:9200" {
		t.Fatalf("unexpected opensearch addresses: %q", cfg.OpenSearchAddresses)
	}
	if cfg.OpenSearchUsername != "seshat" {
		t.Fatalf("unexpected opensearch username: %q", cfg.OpenSearchUsername)
	}
	if cfg.OpenSearchPassword != "secret" {
		t.Fatalf("unexpected opensearch password: %q", cfg.OpenSearchPassword)
	}
	if cfg.OpenSearchAPIKey != "api-key" {
		t.Fatalf("unexpected opensearch api key: %q", cfg.OpenSearchAPIKey)
	}
	if cfg.OpenSearchIndexPrefix != "company-rag" {
		t.Fatalf("unexpected opensearch index prefix: %q", cfg.OpenSearchIndexPrefix)
	}
	if cfg.OpenSearchCreateIndex {
		t.Fatal("expected opensearch_create_index=false from env")
	}
	if !cfg.OpenSearchKNN {
		t.Fatal("expected opensearch_knn=true from env")
	}
	if !cfg.OpenSearchInsecureSkipVerify {
		t.Fatal("expected opensearch_insecure_skip_verify=true from env")
	}
}

func TestLoadIntoSupportsLegacyVectorBackendEnv(t *testing.T) {
	t.Setenv("SESHAT_VECTOR_BACKEND", "opensearch")

	var cfg Config
	if err := LoadInto(&cfg); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.VectorStore != "opensearch" {
		t.Fatalf("expected legacy vector backend env to populate vector store, got %q", cfg.VectorStore)
	}
}

func TestAvailableProvidersIncludesOpenAI(t *testing.T) {
	providers := AvailableProviders()
	if len(providers) == 0 {
		t.Fatalf("expected providers")
	}

	found := false
	for _, provider := range providers {
		if provider.Name == sdk.APIProviderOpenAI {
			found = true
			if len(provider.Models) == 0 {
				t.Fatalf("expected openai models")
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected openai provider")
	}
}

func TestAvailableProvidersIncludesOpenAICompatibleAdditions(t *testing.T) {
	for _, provider := range []sdk.APIProvider{
		sdk.APIProviderCodex,
		sdk.APIProviderDeepSeek,
		sdk.APIProviderOpenCode,
		sdk.APIProviderMistral,
		sdk.APIProviderKimi,
	} {
		info, ok := GetProviderInfo(provider)
		if !ok {
			t.Fatalf("expected provider info for %q", provider)
		}
		if len(info.Models) == 0 {
			t.Fatalf("expected models for %q", provider)
		}
	}
}

func TestProviderForModelUsesCatalog(t *testing.T) {
	if provider := ProviderForModel("gpt-5.5"); provider != sdk.APIProviderOpenAI {
		t.Fatalf("unexpected provider: got %q", provider)
	}
	if provider := ProviderForModel("deepseek-flash"); provider != sdk.APIProviderDeepSeek {
		t.Fatalf("unexpected provider: got %q", provider)
	}
	if provider := ProviderForModel("grok-4.7"); provider != sdk.APIProviderOpenCode {
		t.Fatalf("unexpected provider: got %q", provider)
	}
}

func TestResolveProviderOpenAICompatibleAdditions(t *testing.T) {
	cases := map[string]sdk.APIProvider{
		"codex":        sdk.APIProviderCodex,
		"deepseek":     sdk.APIProviderDeepSeek,
		"deep-seek":    sdk.APIProviderDeepSeek,
		"opencode":     sdk.APIProviderOpenCode,
		"opencode-zen": sdk.APIProviderOpenCode,
		"mistral":      sdk.APIProviderMistral,
		"mistral-ai":   sdk.APIProviderMistral,
		"kimi":         sdk.APIProviderKimi,
		"moonshot":     sdk.APIProviderKimi,
	}
	for raw, want := range cases {
		if got := ResolveProvider(raw); got != want {
			t.Fatalf("ResolveProvider(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestProviderCredentialEnvVarsOpenAICompatibleAdditions(t *testing.T) {
	cases := map[sdk.APIProvider]string{
		sdk.APIProviderCodex:    "CODEX_API_KEY",
		sdk.APIProviderDeepSeek: "DEEPSEEK_API_KEY",
		sdk.APIProviderOpenCode: "OPENCODE_API_KEY",
		sdk.APIProviderMistral:  "MISTRAL_API_KEY",
		sdk.APIProviderKimi:     "MOONSHOT_API_KEY",
	}
	for provider, want := range cases {
		vars := ProviderCredentialEnvVars(provider)
		if len(vars) == 0 || vars[0] != want {
			t.Fatalf("ProviderCredentialEnvVars(%q) = %v, want first %q", provider, vars, want)
		}
	}
}

func TestValidateProviderSetup(t *testing.T) {
	err := ValidateProviderSetup(Config{}, sdk.APIProviderOpenAI)
	if err == nil {
		t.Fatalf("expected validation error")
	}

	err = ValidateProviderSetup(Config{APIKey: "secret"}, sdk.APIProviderOpenAI)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

// TestValidateProviderSetup_WorkersAIRequiresAccountID: Cloudflare Workers AI is a generic catalog provider whose base URL carries the
// account id. Without it the setup fails with a message that names it, instead of a malformed-URL error deep in the request path.
func TestValidateProviderSetup_WorkersAIRequiresAccountID(t *testing.T) {
	const workersAI = sdk.APIProvider("cloudflare-workers-ai")
	t.Setenv("CLOUDFLARE_API_KEY", "secret")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	err := ValidateProviderSetup(Config{}, workersAI)
	if err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_ACCOUNT_ID") {
		t.Fatalf("expected an error naming CLOUDFLARE_ACCOUNT_ID, got %v", err)
	}

	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-123")
	if err := ValidateProviderSetup(Config{}, workersAI); err != nil {
		t.Fatalf("unexpected validation error once the account id is set: %v", err)
	}
}
