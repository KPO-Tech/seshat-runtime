package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
	"github.com/spf13/viper"
)

// Config contains host-level runtime settings shared by CLI and server entrypoints.
type Config struct {
	RuntimeRoot       string  `mapstructure:"runtime_root" yaml:"runtime_root,omitempty"`
	Cwd               string  `mapstructure:"cwd" yaml:"cwd,omitempty"`
	Model             string  `mapstructure:"model" yaml:"model,omitempty"`
	TitleModel        string  `mapstructure:"title_model" yaml:"title_model,omitempty"`
	TitleModelBaseURL string  `mapstructure:"title_model_base_url" yaml:"title_model_base_url,omitempty"`
	TitleModelAPIKey  string  `mapstructure:"title_model_api_key" yaml:"-"`
	TitleLocalEnabled bool    `mapstructure:"title_local_enabled" yaml:"title_local_enabled,omitempty"`
	TitleLocalRuntime string  `mapstructure:"title_local_runtime" yaml:"title_local_runtime,omitempty"`
	TitleLocalExe     string  `mapstructure:"title_local_executable" yaml:"title_local_executable,omitempty"`
	TitleLocalModel   string  `mapstructure:"title_local_model_path" yaml:"title_local_model_path,omitempty"`
	TitleLocalHFRepo  string  `mapstructure:"title_local_hf_repo" yaml:"title_local_hf_repo,omitempty"`
	TitleLocalHFFile  string  `mapstructure:"title_local_hf_file" yaml:"title_local_hf_file,omitempty"`
	TitleLocalCache   string  `mapstructure:"title_local_cache_dir" yaml:"title_local_cache_dir,omitempty"`
	TitleLocalArgs    string  `mapstructure:"title_local_args" yaml:"title_local_args,omitempty"`
	TitleLocalTimeout string  `mapstructure:"title_local_timeout" yaml:"title_local_timeout,omitempty"`
	MaxTokens         int     `mapstructure:"max_tokens" yaml:"max_tokens,omitempty"`
	Temperature       float64 `mapstructure:"temperature" yaml:"temperature,omitempty"`
	MCPEnabled        bool    `mapstructure:"mcp_enabled" yaml:"mcp_enabled,omitempty"`
	SkillsEnabled     bool    `mapstructure:"skills_enabled" yaml:"skills_enabled,omitempty"`
	Debug             bool    `mapstructure:"debug" yaml:"debug,omitempty"`
	APIKey            string  `mapstructure:"api_key" yaml:"api_key,omitempty"`
	DBPath            string  `mapstructure:"db_path" yaml:"db_path,omitempty"`
	SessionDBPath     string  `mapstructure:"session_db_path" yaml:"session_db_path,omitempty"`
	ProviderBaseURL   string  `mapstructure:"provider_base_url" yaml:"provider_base_url,omitempty"`
	ProviderRegion    string  `mapstructure:"provider_region" yaml:"provider_region,omitempty"`
	ProviderProjectID string  `mapstructure:"provider_project_id" yaml:"provider_project_id,omitempty"`
	ProviderResource  string  `mapstructure:"provider_resource" yaml:"provider_resource,omitempty"`
	AdminEmail        string  `mapstructure:"admin_email" yaml:"admin_email,omitempty"`
	AdminPassword     string  `mapstructure:"admin_password" yaml:"-"`
	AdminPasswordHash string  `mapstructure:"admin_password_hash" yaml:"admin_password_hash,omitempty"`

	// Database driver configuration (multi-driver support)
	DBDriver      string `mapstructure:"db_driver" yaml:"db_driver,omitempty"`
	DBDSN         string `mapstructure:"db_dsn" yaml:"db_dsn,omitempty"`
	DBAutoMigrate bool   `mapstructure:"db_auto_migrate" yaml:"db_auto_migrate,omitempty"`

	// Embedder configuration for the RAG / knowledge domain
	EmbedderBaseURL  string `mapstructure:"embedder_base_url" yaml:"embedder_base_url,omitempty"`
	EmbedderAPIKey   string `mapstructure:"embedder_api_key" yaml:"embedder_api_key,omitempty"`
	EmbedderModel    string `mapstructure:"embedder_model" yaml:"embedder_model,omitempty"`
	EmbedderProvider string `mapstructure:"embedder_provider" yaml:"embedder_provider,omitempty"`

	// Vector store (sqlite|pgvector|qdrant|opensearch|chroma|memory). Defaults to sqlite.
	VectorStore string `mapstructure:"vector_store" yaml:"vector_store,omitempty"`

	// Deprecated: use VectorStore / SESHAT_VECTOR_STORE.
	VectorBackend                string `mapstructure:"vector_backend" yaml:"-"`
	QdrantHost                   string `mapstructure:"qdrant_host" yaml:"qdrant_host,omitempty"`
	QdrantPort                   int    `mapstructure:"qdrant_port" yaml:"qdrant_port,omitempty"`
	QdrantAPIKey                 string `mapstructure:"qdrant_api_key" yaml:"qdrant_api_key,omitempty"`
	QdrantPrefix                 string `mapstructure:"qdrant_prefix" yaml:"qdrant_prefix,omitempty"`
	OpenSearchAddresses          string `mapstructure:"opensearch_addresses" yaml:"opensearch_addresses,omitempty"`
	OpenSearchUsername           string `mapstructure:"opensearch_username" yaml:"opensearch_username,omitempty"`
	OpenSearchPassword           string `mapstructure:"opensearch_password" yaml:"-"`
	OpenSearchAPIKey             string `mapstructure:"opensearch_api_key" yaml:"-"`
	OpenSearchIndexPrefix        string `mapstructure:"opensearch_index_prefix" yaml:"opensearch_index_prefix,omitempty"`
	OpenSearchCreateIndex        bool   `mapstructure:"opensearch_create_index" yaml:"opensearch_create_index,omitempty"`
	OpenSearchKNN                bool   `mapstructure:"opensearch_knn" yaml:"opensearch_knn,omitempty"`
	OpenSearchInsecureSkipVerify bool   `mapstructure:"opensearch_insecure_skip_verify" yaml:"opensearch_insecure_skip_verify,omitempty"`
	OpenSearchBulkSize           int    `mapstructure:"opensearch_bulk_size" yaml:"opensearch_bulk_size,omitempty"`
	PgVectorCreateExtension      bool   `mapstructure:"pgvector_create_extension" yaml:"pgvector_create_extension,omitempty"`
	PgVectorDSN                  string `mapstructure:"pgvector_dsn" yaml:"pgvector_dsn,omitempty"`
	PgVectorIndexMethod          string `mapstructure:"pgvector_index_method" yaml:"pgvector_index_method,omitempty"`
	PgVectorHNSWM                int    `mapstructure:"pgvector_hnsw_m" yaml:"pgvector_hnsw_m,omitempty"`
	PgVectorHNSWEF               int    `mapstructure:"pgvector_hnsw_ef_construction" yaml:"pgvector_hnsw_ef_construction,omitempty"`
	PgVectorIVFFlatLists         int    `mapstructure:"pgvector_ivfflat_lists" yaml:"pgvector_ivfflat_lists,omitempty"`
	ChromaURL                    string `mapstructure:"chroma_url" yaml:"chroma_url,omitempty"`
	ChromaAPIKey                 string `mapstructure:"chroma_api_key" yaml:"chroma_api_key,omitempty"`
	ChromaTenant                 string `mapstructure:"chroma_tenant" yaml:"chroma_tenant,omitempty"`
	ChromaDatabase               string `mapstructure:"chroma_database" yaml:"chroma_database,omitempty"`

	// Storage configuration
	StorageProvider         string `mapstructure:"storage_provider" yaml:"storage_provider,omitempty"`
	StorageLocalPath        string `mapstructure:"storage_local_path" yaml:"storage_local_path,omitempty"`
	S3Endpoint              string `mapstructure:"s3_endpoint" yaml:"s3_endpoint,omitempty"`
	S3Bucket                string `mapstructure:"s3_bucket" yaml:"s3_bucket,omitempty"`
	S3AccessKeyID           string `mapstructure:"s3_access_key_id" yaml:"s3_access_key_id,omitempty"`
	S3SecretAccessKey       string `mapstructure:"s3_secret_access_key" yaml:"s3_secret_access_key,omitempty"`
	S3Region                string `mapstructure:"s3_region" yaml:"s3_region,omitempty"`
	S3KeyPrefix             string `mapstructure:"s3_key_prefix" yaml:"s3_key_prefix,omitempty"`
	StorageGCEnabled        bool   `mapstructure:"storage_gc_enabled" yaml:"storage_gc_enabled,omitempty"`
	StorageGCInterval       string `mapstructure:"storage_gc_interval" yaml:"storage_gc_interval,omitempty"`
	StorageGCLimit          int    `mapstructure:"storage_gc_limit" yaml:"storage_gc_limit,omitempty"`
	StorageGCNamespaces     string `mapstructure:"storage_gc_namespaces" yaml:"storage_gc_namespaces,omitempty"`
	MemoryEnabled           bool   `mapstructure:"memory_enabled" yaml:"memory_enabled,omitempty"`
	MemoryConsolidate       bool   `mapstructure:"memory_consolidate" yaml:"memory_consolidate,omitempty"`
	BrowserRemoteControlURL string `mapstructure:"browser_remote_control_url" yaml:"browser_remote_control_url,omitempty"`
	BrowserExecutablePath   string `mapstructure:"browser_executable_path" yaml:"browser_executable_path,omitempty"`
	DocumentReaderURL       string `mapstructure:"document_reader_url" yaml:"document_reader_url,omitempty"`
	AutomationServiceURL    string `mapstructure:"automation_service_url" yaml:"automation_service_url,omitempty"`
	AutomationAPIKey        string `mapstructure:"-" yaml:"-"` // loaded from env only, never persisted

	// Search tool integration keys — loaded from the credentials DB at runtime,
	// never stored in the YAML config file.
	TavilyAPIKey      string `mapstructure:"-" yaml:"-"`
	ExaAPIKey         string `mapstructure:"-" yaml:"-"`
	JinaAPIKey        string `mapstructure:"-" yaml:"-"`
	WebSearchProvider string `mapstructure:"web_search_provider" yaml:"web_search_provider,omitempty"`

	// API rate limiting — max requests per minute per authenticated user (0 = disabled)
	RateLimitPerMinute int `mapstructure:"rate_limit_per_minute" yaml:"rate_limit_per_minute,omitempty"`

	// User management flags
	EnableSignup    bool   `mapstructure:"enable_signup" yaml:"enable_signup,omitempty"`
	DefaultUserRole string `mapstructure:"default_user_role" yaml:"default_user_role,omitempty"`
	EnableAPIKeys   bool   `mapstructure:"enable_api_keys" yaml:"enable_api_keys,omitempty"`

	// Skill repository collections — comma-separated git URLs cloned at startup.
	// Example: SESHAT_SKILL_REPOS=https://github.com/romainsimon/paperasse
	SkillRepos string `mapstructure:"skill_repos" yaml:"skill_repos,omitempty"`

	// FeaturedSkillRepos — comma-separated git URLs shown as installable catalog entries in the UI.
	// Example: SESHAT_FEATURED_SKILL_REPOS=https://github.com/romainsimon/paperasse
	FeaturedSkillRepos string `mapstructure:"featured_skill_repos" yaml:"featured_skill_repos,omitempty"`

	// TrustedProxies — comma-separated CIDR ranges (or bare IPs) of reverse proxies whose
	// X-Forwarded-For / X-Real-Ip headers should be trusted for IP resolution.
	// Leave empty (the default) for a desktop deployment without a reverse proxy.
	// Example: SESHAT_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12
	TrustedProxies string `mapstructure:"trusted_proxies" yaml:"trusted_proxies,omitempty"`

	// SkillRepoHosts — comma-separated list of git hosting domains allowed when installing
	// a skill repo via the API. Defaults to github.com, gitlab.com, bitbucket.org, codeberg.org.
	// Example: SESHAT_SKILL_REPO_HOSTS=github.com,mygitlab.company.com
	SkillRepoHosts string `mapstructure:"skill_repo_hosts" yaml:"skill_repo_hosts,omitempty"`

	// DefaultSkillRepo — git URL of the official Seshat skills collection cloned
	// silently in the background on first boot. Defaults to the canonical seshat-skills
	// repo. Set to "none" to disable automatic install.
	// Example: SESHAT_DEFAULT_SKILL_REPO=https://github.com/KPO-Tech/seshat-skills
	DefaultSkillRepo string `mapstructure:"default_skill_repo" yaml:"default_skill_repo,omitempty"`

	// Hooks — user-defined shell commands that fire on lifecycle events.
	// Currently supported event: pre_tool_use (fires before every tool call).
	//
	// Example config.yaml (the actual file LoadInto reads via viper - this
	// comment used to say .seshat.yaml, which is not a name this loader ever
	// looks for; M5d in TODO.md):
	//   hooks:
	//     pre_tool_use:
	//       - command: "jq '.command' && exit 0"
	//         matcher: "bash"
	//         timeout: 10
	Hooks map[string][]HookEntry `mapstructure:"hooks" yaml:"hooks,omitempty"`
}

// HookEntry mirrors hooks.HookConfig but lives in pkg/config to avoid import cycles.
type HookEntry struct {
	Matcher string `mapstructure:"matcher" yaml:"matcher,omitempty"`
	Command string `mapstructure:"command" yaml:"command"`
	Timeout int    `mapstructure:"timeout" yaml:"timeout,omitempty"`
}

// DefaultConfig returns the shared defaults used by host applications.
func DefaultConfig() Config {
	return Config{
		RuntimeRoot:             "",
		Cwd:                     ".",
		Model:                   "",
		MaxTokens:               4096,
		Temperature:             0.7,
		MCPEnabled:              true,
		SkillsEnabled:           true,
		Debug:                   false,
		APIKey:                  "",
		DBPath:                  "",
		SessionDBPath:           "",
		ProviderBaseURL:         "",
		ProviderRegion:          "",
		ProviderProjectID:       "",
		ProviderResource:        "",
		AdminEmail:              "",
		AdminPassword:           "",
		AdminPasswordHash:       "",
		DBAutoMigrate:           true,
		OpenSearchCreateIndex:   true,
		OpenSearchKNN:           true,
		PgVectorCreateExtension: true,
		PgVectorIndexMethod:     "hnsw",
		PgVectorHNSWM:           16,
		PgVectorHNSWEF:          64,
		PgVectorIVFFlatLists:    100,
		StorageGCEnabled:        true,
		StorageGCInterval:       "1h",
		StorageGCLimit:          512,
		EnableSignup:            true,
		DefaultUserRole:         "member",
		EnableAPIKeys:           true,
		StorageGCNamespaces: strings.Join([]string{
			"artifacts/web",
			"artifacts/browser/screenshots",
			"artifacts/browser/downloads",
		}, ","),
	}
}

// Load returns a fully initialized shared runtime config.
func Load() (Config, error) {
	config := DefaultConfig()
	return config, LoadInto(&config)
}

// LoadInto refreshes a caller-owned config from env files, env vars, and config files.
func LoadInto(config *Config) error {
	if config == nil {
		return fmt.Errorf("config is nil")
	}

	*config = DefaultConfig()
	loadEnvFile(".env") //nolint:errcheck // best-effort .env loading

	v := viper.New()
	v.SetConfigType("yaml")
	v.SetEnvPrefix("SESHAT")

	// Primary location: derived from SESHAT_RUNTIME_ROOT (or ~/.config/seshat by default).
	// CLI sets SESHAT_RUNTIME_ROOT=~/.config/seshat-cli before calling Load().
	v.SetConfigName("config")
	v.AddConfigPath(runtimepath.ResolveRoot(""))

	v.AddConfigPath("$HOME")

	v.BindEnv("runtime_root", runtimepath.EnvRuntimeRoot)
	v.BindEnv("cwd", "SESHAT_CWD")
	v.BindEnv("model", "SESHAT_MODEL")
	v.BindEnv("title_model", "SESHAT_TITLE_MODEL")
	v.BindEnv("title_model_base_url", "SESHAT_TITLE_MODEL_BASE_URL")
	v.BindEnv("title_model_api_key", "SESHAT_TITLE_MODEL_API_KEY")
	v.BindEnv("title_local_enabled", "SESHAT_TITLE_LOCAL_ENABLED")
	v.BindEnv("title_local_runtime", "SESHAT_TITLE_LOCAL_RUNTIME")
	v.BindEnv("title_local_executable", "SESHAT_TITLE_LOCAL_EXECUTABLE")
	v.BindEnv("title_local_model_path", "SESHAT_TITLE_LOCAL_MODEL_PATH")
	v.BindEnv("title_local_hf_repo", "SESHAT_TITLE_LOCAL_HF_REPO")
	v.BindEnv("title_local_hf_file", "SESHAT_TITLE_LOCAL_HF_FILE")
	v.BindEnv("title_local_cache_dir", "SESHAT_TITLE_LOCAL_CACHE_DIR")
	v.BindEnv("title_local_args", "SESHAT_TITLE_LOCAL_ARGS")
	v.BindEnv("title_local_timeout", "SESHAT_TITLE_LOCAL_TIMEOUT")
	v.BindEnv("debug", "SESHAT_DEBUG")
	v.BindEnv("api_key", "SESHAT_API_KEY")
	v.BindEnv("db_path", "SESHAT_DB_PATH")
	v.BindEnv("session_db_path", "SESHAT_SESSION_DB_PATH")
	v.BindEnv("provider_base_url", "SESHAT_PROVIDER_BASE_URL")
	v.BindEnv("provider_region", "SESHAT_PROVIDER_REGION")
	v.BindEnv("provider_project_id", "SESHAT_PROVIDER_PROJECT_ID")
	v.BindEnv("provider_resource", "SESHAT_PROVIDER_RESOURCE")
	v.BindEnv("admin_email", "SESHAT_ADMIN_EMAIL")
	v.BindEnv("admin_password", "SESHAT_ADMIN_PASSWORD")
	v.BindEnv("admin_password_hash", "SESHAT_ADMIN_PASSWORD_HASH")
	v.BindEnv("web_search_provider", "WEB_SEARCH_PROVIDER")

	v.BindEnv("storage_provider", "SESHAT_STORAGE_PROVIDER")
	v.BindEnv("storage_local_path", "SESHAT_STORAGE_LOCAL_PATH")
	v.BindEnv("s3_endpoint", "SESHAT_S3_ENDPOINT")
	v.BindEnv("s3_bucket", "SESHAT_S3_BUCKET")
	v.BindEnv("s3_access_key_id", "SESHAT_S3_ACCESS_KEY_ID")
	v.BindEnv("s3_secret_access_key", "SESHAT_S3_SECRET_ACCESS_KEY")
	v.BindEnv("s3_region", "SESHAT_S3_REGION")
	v.BindEnv("s3_key_prefix", "SESHAT_S3_KEY_PREFIX")
	v.BindEnv("storage_gc_enabled", "SESHAT_STORAGE_GC_ENABLED")
	v.BindEnv("storage_gc_interval", "SESHAT_STORAGE_GC_INTERVAL")
	v.BindEnv("storage_gc_limit", "SESHAT_STORAGE_GC_LIMIT")
	v.BindEnv("storage_gc_namespaces", "SESHAT_STORAGE_GC_NAMESPACES")
	v.BindEnv("memory_enabled", "SESHAT_MEMORY_ENABLED")
	v.BindEnv("memory_consolidate", "SESHAT_MEMORY_CONSOLIDATE")
	v.BindEnv("browser_remote_control_url", "SESHAT_BROWSER_REMOTE_CONTROL_URL")
	v.BindEnv("browser_executable_path", "SESHAT_BROWSER_EXECUTABLE_PATH")
	v.BindEnv("db_driver", "SESHAT_DB_DRIVER")
	v.BindEnv("db_dsn", "SESHAT_DB_DSN")
	v.BindEnv("db_auto_migrate", "SESHAT_DB_AUTO_MIGRATE")
	v.SetDefault("db_driver", "sqlite")
	v.SetDefault("db_auto_migrate", true)

	v.BindEnv("embedder_base_url", "RAG_EMBEDDING_URL")
	v.BindEnv("embedder_api_key", "RAG_EMBEDDING_API_KEY")
	v.BindEnv("embedder_model", "RAG_EMBEDDING_MODEL")
	v.BindEnv("embedder_provider", "RAG_EMBEDDING_PROVIDER")

	v.BindEnv("vector_store", "SESHAT_VECTOR_STORE")
	v.BindEnv("vector_backend", "SESHAT_VECTOR_BACKEND")
	v.BindEnv("qdrant_host", "QDRANT_HOST")
	v.BindEnv("qdrant_port", "QDRANT_PORT")
	v.BindEnv("qdrant_api_key", "QDRANT_API_KEY")
	v.BindEnv("qdrant_prefix", "QDRANT_PREFIX")
	v.BindEnv("opensearch_addresses", "OPENSEARCH_ADDRESSES")
	v.BindEnv("opensearch_username", "OPENSEARCH_USERNAME")
	v.BindEnv("opensearch_password", "OPENSEARCH_PASSWORD")
	v.BindEnv("opensearch_api_key", "OPENSEARCH_API_KEY")
	v.BindEnv("opensearch_index_prefix", "OPENSEARCH_INDEX_PREFIX")
	v.BindEnv("opensearch_create_index", "OPENSEARCH_CREATE_INDEX")
	v.BindEnv("opensearch_knn", "OPENSEARCH_KNN")
	v.BindEnv("opensearch_insecure_skip_verify", "OPENSEARCH_INSECURE_SKIP_VERIFY")
	v.BindEnv("opensearch_bulk_size", "OPENSEARCH_BULK_SIZE")
	v.BindEnv("pgvector_create_extension", "SESHAT_PGVECTOR_CREATE_EXTENSION")
	v.BindEnv("pgvector_dsn", "SESHAT_PGVECTOR_DSN")
	v.BindEnv("pgvector_index_method", "SESHAT_PGVECTOR_INDEX_METHOD")
	v.BindEnv("pgvector_hnsw_m", "SESHAT_PGVECTOR_HNSW_M")
	v.BindEnv("pgvector_hnsw_ef_construction", "SESHAT_PGVECTOR_HNSW_EF_CONSTRUCTION")
	v.BindEnv("pgvector_ivfflat_lists", "SESHAT_PGVECTOR_IVFFLAT_LISTS")
	v.BindEnv("chroma_url", "CHROMA_URL")
	v.BindEnv("chroma_api_key", "CHROMA_API_KEY")
	v.BindEnv("chroma_tenant", "CHROMA_TENANT")
	v.BindEnv("chroma_database", "CHROMA_DATABASE")

	v.BindEnv("enable_signup", "SESHAT_ENABLE_SIGNUP")
	v.BindEnv("default_user_role", "SESHAT_DEFAULT_USER_ROLE")
	v.BindEnv("enable_api_keys", "SESHAT_ENABLE_API_KEYS")
	v.BindEnv("skill_repos", "SESHAT_SKILL_REPOS")
	v.BindEnv("featured_skill_repos", "SESHAT_FEATURED_SKILL_REPOS")
	v.BindEnv("trusted_proxies", "SESHAT_TRUSTED_PROXIES")
	v.BindEnv("skill_repo_hosts", "SESHAT_SKILL_REPO_HOSTS")
	v.BindEnv("automation_service_url", "AUTOMATION_SERVICE_URL")
	v.SetDefault("enable_signup", true)
	v.SetDefault("default_user_role", "member")
	v.SetDefault("enable_api_keys", true)

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) {
			// Config not found — no fallback, config is optional.
		} else {
			return fmt.Errorf("failed to read config: %w", err)
		}
	}

	if err := v.Unmarshal(config); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if strings.TrimSpace(config.VectorStore) == "" {
		config.VectorStore = strings.TrimSpace(config.VectorBackend)
	}

	config.RuntimeRoot = runtimepath.ResolveRoot(config.RuntimeRoot)

	// Evaluate any $(...) shell substitutions in credential fields.
	// This allows api_key: "$(vault kv get -field=token secret/api)" in config.yaml.
	ExpandShellValues(config)

	ApplyRuntimeEnv(*config)
	return nil
}

// ApplySearchKeys sets search-provider env vars from config fields so that
// the search tool sub-packages pick them up via os.Getenv.
func ApplySearchKeys(config Config) {
	setEnvIfPresent := func(name, value string) {
		if value != "" && os.Getenv(name) == "" {
			os.Setenv(name, value)
		}
	}
	setEnvIfPresent("TAVILY_API_KEY", config.TavilyAPIKey)
	setEnvIfPresent("EXA_API_KEY", config.ExaAPIKey)
	setEnvIfPresent("JINA_API_KEY", config.JinaAPIKey)
	setEnvIfPresent("WEB_SEARCH_PROVIDER", config.WebSearchProvider)
}

// EffectiveDBPath returns the legacy local SQLite path used by CLI/runtime code.
// For backend hosts, prefer EffectiveBackendSQLitePath and EffectiveSessionDBPath.
func EffectiveDBPath(config Config) string {
	if config.DBPath != "" {
		return config.DBPath
	}
	return runtimepath.BackendDBPath(config.RuntimeRoot)
}

// EffectiveBackendSQLitePath returns the SQLite file used when the backend DB
// driver itself is SQLite.
func EffectiveBackendSQLitePath(config Config) string {
	return EffectiveDBPath(config)
}

// EffectiveSessionDBPath returns the SQLite file used for runtime session/core
// persistence. It is independent from the backend DB driver so a Postgres
// backend can still keep the runtime session store in SQLite.
func EffectiveSessionDBPath(config Config) string {
	if config.SessionDBPath != "" {
		return config.SessionDBPath
	}
	return EffectiveDBPath(config)
}

// EffectiveRuntimeRoot returns the runtime root of a configuration: its RuntimeRoot when it is not blank, otherwise the SESHAT_RUNTIME_ROOT environment variable, otherwise ~/.config/seshat (a seshat directory under the temporary directory when no home can be found). A leading "~" is expanded and the path is cleaned.
func EffectiveRuntimeRoot(config Config) string {
	return runtimepath.ResolveRoot(config.RuntimeRoot)
}

// EffectiveStorageLocalPath returns the directory of the local storage of a configuration: its StorageLocalPath when it is set, with a leading "~" expanded, otherwise the storage directory under the runtime root.
func EffectiveStorageLocalPath(config Config) string {
	if trimmed := strings.TrimSpace(config.StorageLocalPath); trimmed != "" {
		return filepath.Clean(runtimepath.ExpandTilde(trimmed))
	}
	return runtimepath.StorageDir(config.RuntimeRoot)
}

// EffectiveAPIKeyAndProvider resolves the configured provider credentials in priority order.
func EffectiveAPIKeyAndProvider(config Config) (string, sdk.APIProvider) {
	if preferred := preferredProvider(config); preferred != "" {
		return ResolveAPIKey(config, preferred), preferred
	}

	for _, provider := range AvailableProviders() {
		if key := resolveAPIKeyFromEnv(provider.Name); key != "" {
			return key, provider.Name
		}
	}

	if config.APIKey != "" {
		return config.APIKey, inferProviderFromAPIKey(config.APIKey)
	}

	return "", DetectProviderFromModel(config.Model)
}

// DetectProviderFromModel infers the provider from the requested model name.
func DetectProviderFromModel(model string) sdk.APIProvider {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}

	if provider := ProviderForModel(model); provider != "" {
		return provider
	}

	switch {
	case strings.HasPrefix(model, "glm"):
		return sdk.APIProviderZAi
	case strings.HasPrefix(model, "@cf/"):
		return sdk.APIProviderWorkersAI
	case strings.HasPrefix(model, "anthropic/"),
		strings.HasPrefix(model, "openai/"),
		strings.HasPrefix(model, "deepseek/"),
		strings.HasPrefix(model, "qwen/"),
		strings.HasPrefix(model, "meta-llama/"):
		return sdk.APIProviderOpenRouter
	case strings.Contains(model, "claude"):
		return sdk.APIProviderAnthropic
	case strings.Contains(model, "gpt") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3"):
		return sdk.APIProviderOpenAI
	case strings.Contains(model, "gemini"):
		return sdk.APIProviderGemini
	case strings.HasPrefix(model, "minimax-"), strings.HasPrefix(model, "m2"):
		return sdk.APIProviderMiniMax
	case strings.Contains(model, ":") && (strings.Contains(model, "qwen") ||
		strings.Contains(model, "deepseek") ||
		strings.Contains(model, "llama") ||
		strings.Contains(model, "mistral") ||
		strings.Contains(model, "codellama") ||
		strings.Contains(model, "phi")):
		return sdk.APIProviderOllama
	default:
		return ""
	}
}

// ParseModelIdentifier normalizes optional provider prefixes like "openai:gpt-4o".
// Returns an empty ModelIdentifier when raw is empty — callers that need a
// concrete default should apply sdk.DefaultClientConfig().Model themselves.
func ParseModelIdentifier(raw string) sdk.ModelIdentifier {
	value := strings.TrimSpace(raw)
	if value == "" {
		return sdk.ModelIdentifier{}
	}

	provider := sdk.APIProviderAnthropic
	model := value
	if prefix, rest, ok := strings.Cut(value, ":"); ok && strings.TrimSpace(rest) != "" {
		if resolved, ok := parseProvider(prefix); ok {
			provider = resolved
			model = strings.TrimSpace(rest)
		}
	}

	return sdk.ModelIdentifier{
		Provider: provider,
		Model:    model,
	}
}

// HasExplicitProviderPrefix reports whether a model string explicitly carries a recognized provider prefix.
func HasExplicitProviderPrefix(raw string) bool {
	value := strings.TrimSpace(raw)
	prefix, rest, ok := strings.Cut(value, ":")
	if !ok || strings.TrimSpace(rest) == "" {
		return false
	}
	_, ok = parseProvider(prefix)
	return ok
}

func loadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			// Only set if not already present in the process environment,
			// so that vars exported by start.sh take priority over .env files.
			if key != "" && os.Getenv(key) == "" {
				value := strings.TrimSpace(parts[1])
				if reason := dotenvRefusal(key, value); reason != "" {
					fmt.Fprintf(os.Stderr, "seshat: %s: %s ignored (%s); set it in the environment or in your own configuration file instead\n", path, key, reason)
					continue
				}
				if warning := dotenvRedirectWarning(key, value); warning != "" {
					fmt.Fprintf(os.Stderr, "seshat: %s: %s; check that this .env is yours\n", path, warning)
				}
				os.Setenv(key, value)
			}
		}
	}
	return nil
}

func preferredProvider(config Config) sdk.APIProvider {
	raw := strings.TrimSpace(config.Model)
	if raw == "" {
		return ""
	}
	if HasExplicitProviderPrefix(raw) {
		return ParseModelIdentifier(raw).Provider
	}
	return DetectProviderFromModel(raw)
}

func inferProviderFromAPIKey(key string) sdk.APIProvider {
	switch {
	case strings.HasPrefix(key, "sk-ant-"):
		return sdk.APIProviderAnthropic
	case strings.HasPrefix(key, "sk-or-"):
		return sdk.APIProviderOpenRouter
	case strings.HasPrefix(key, "sk-"):
		return sdk.APIProviderOpenAI
	default:
		return sdk.APIProviderZAi
	}
}
