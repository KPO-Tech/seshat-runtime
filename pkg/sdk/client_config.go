package sdk

import (
	"context"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	bashTool "github.com/KPO-Tech/seshat-runtime/internal/tools/bash"
	"github.com/KPO-Tech/seshat-runtime/pkg/companion"
	"github.com/KPO-Tech/seshat-runtime/pkg/documentreader"
	"github.com/KPO-Tech/seshat-runtime/pkg/pdfsmart"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
)

// SandboxKind selects an OS-level sandbox backend for bash tool execution.
type SandboxKind = sandbox.EnvironmentKind

// Sandbox backend kind values for ClientConfig.SandboxKind.
const (
	// SandboxKindLocal is the zero value — Landlock on Linux, unconfined
	// elsewhere unless RequireSandbox is also set. Today's existing behavior.
	SandboxKindLocal = sandbox.EnvironmentLocal
	// SandboxKindDocker routes bash execution through a persistent Docker
	// container (internal/sandbox.DockerExecutor) — real filesystem/process
	// isolation plus enforced CPU/memory caps and optional network isolation.
	// Falls back to SandboxKindLocal with a logged warning if Docker isn't
	// reachable when the client is constructed.
	SandboxKindDocker = sandbox.EnvironmentDocker
)

// SandboxDockerConfig configures the Docker sandbox backend used when
// ClientConfig.SandboxKind is SandboxKindDocker. See
// sandbox.DockerExecutorConfig for field-by-field documentation.
type SandboxDockerConfig = sandbox.DockerExecutorConfig

// CredentialResolver resolves the API key for an LLM provider at client
// creation time. The provider argument matches types.APIProvider values
// (e.g. "anthropic", "openai", "codex").
//
// CLI / headless mode: not needed — the default env-var → FileStore
// resolution in internal/providers/auth.go applies automatically.
//
// Server mode: implement this interface to inject per-user credentials
// from a database instead of relying on a single global API key.
type CredentialResolver interface {
	ResolveAPIKey(ctx context.Context, provider string) (string, error)
}

// ImageGenerationConfig controls the generate_image built-in tool.
type ImageGenerationConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

// TextToSpeechConfig controls the text_to_speech built-in tool.
type TextToSpeechConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Voice    string `json:"voice,omitempty"`
	Format   string `json:"format,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

// SpeechToTextConfig controls the speech_to_text built-in tool.
type SpeechToTextConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Language string `json:"language,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

// LocalTitleConfig runs a small local model for session title generation,
// without Ollama or a long-running local server. The first runtime is
// llama.cpp-compatible GGUF through llama-cli.
type LocalTitleConfig struct {
	Enabled        bool          `json:"enabled,omitempty"`
	Runtime        string        `json:"runtime,omitempty"`
	ExecutablePath string        `json:"executable_path,omitempty"`
	ModelPath      string        `json:"model_path,omitempty"`
	HFRepo         string        `json:"hf_repo,omitempty"`
	HFFile         string        `json:"hf_file,omitempty"`
	CacheDir       string        `json:"cache_dir,omitempty"`
	Args           []string      `json:"args,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
}

// TitleGenerator generates a short automatic title from a session's first user
// message. Hosts can inject a local implementation for tiny HF/GGUF models.
type TitleGenerator interface {
	GenerateTitle(ctx context.Context, firstUserMsg string) (string, error)
}

// ClientConfig represents the client configuration.
type ClientConfig struct {
	APIKey     string          `json:"api_key"`
	Model      ModelIdentifier `json:"model"`
	TitleModel ModelIdentifier `json:"title_model,omitempty"`
	// CompactSummaryModel overrides which model generates compaction summaries.
	// Zero value means "use Model" (the session's own model), so compaction
	// works on any provider without needing an Anthropic key.
	CompactSummaryModel ModelIdentifier `json:"compact_summary_model,omitempty"`

	// CompactModelRules choose the model that writes the compaction summary by the provider and model of the session,
	// first match wins. They apply when CompactSummaryModel is not set and take precedence over the built-in rules. A
	// rule's Use model without a provider stays on the session's provider.
	CompactModelRules []CompactModelRule `json:"compact_model_rules,omitempty"`

	// NoDefaultCompactModels switches off the built-in rules (glm-4.5-air for z.ai sessions, the Haiku model for
	// Anthropic sessions), so that a session without a matching rule of its own summarizes with its own model.
	NoDefaultCompactModels bool `json:"no_default_compact_models,omitempty"`

	// CompactThreshold is the share of the context window at which the conversation is compacted automatically
	// (for example 0.85). Zero uses the default. It must be below 1.
	CompactThreshold float64 `json:"compact_threshold,omitempty"`

	// CompactTarget is the share of the window the recent messages kept as they are may take after a compaction
	// (for example 0.5): lower keeps less verbatim and leaves more room before the next one. Zero uses the default.
	// It must be below CompactThreshold.
	CompactTarget float64 `json:"compact_target,omitempty"`

	// TrustProjectInstructions gates SESHAT.md/AGENTS.md/.seshat/instructions.md
	// found in WorkingDir behind project trust before they are injected into
	// the system prompt (see docs/project-trust.md). It is called with the
	// file's exact path and content; return true to trust and inject it. nil
	// fails closed - no project you didn't write or explicitly trust can put
	// its own instructions in the model's prompt. A host that already trusts
	// projects for other configuration (the terminal interface's `seshat
	// trust`) should wire this to the same hash-based check.
	TrustProjectInstructions func(path string, content []byte) bool `json:"-"`
	PermissionMode           PermissionMode                         `json:"permission_mode"`
	MaxTurns                 int                                    `json:"max_turns"`
	AutoCompact              bool                                   `json:"auto_compact"`

	// Session persistence
	PersistSessions   bool           `json:"persist_sessions"`
	SessionStorageDir string         `json:"session_storage_dir"`
	SessionSQLitePath string         `json:"session_sqlite_path"`
	SessionBackend    SessionBackend `json:"-"`
	SessionStore      SessionStore   `json:"-"`

	// MCP
	MCPServers []MCPServerConfig `json:"mcp_servers"`

	// Callbacks
	PromptFn        PromptFn            `json:"-"`
	ProgressFn      func(ToolProgress)  `json:"-"`
	ResponseChunkFn func(ResponseChunk) `json:"-"`
	RuntimeEventFn  func(RuntimeEvent)  `json:"-"`
	// OnSessionTitled is called after the engine auto-generates a title for a
	// session (once, after the first completed turn). The id is the session ID
	// and title is the short, AI-generated label. Use this to refresh the UI.
	OnSessionTitled func(id SessionID, title string) `json:"-"`

	// Filesystem
	WorkingDir string `json:"working_dir"`

	// Browser
	BrowserRemoteControlURL string `json:"browser_remote_control_url"`
	BrowserExecutablePath   string `json:"browser_executable_path"`
	// BrowserSessionTargetResolver, when set, lets a caller (the desktop app)
	// attach a session's browser tool calls directly to an existing CDP tab
	// it already shows live to the user, instead of opening an invisible
	// incognito context - see internal/web/browser.Config.TargetResolver for
	// the full rationale. Optional; nil preserves the default behavior.
	BrowserSessionTargetResolver func(ctx context.Context, sessionID SessionID) (targetID string, ok bool) `json:"-"`

	// Storage
	StorageConfig       *StorageConfig `json:"-"`
	ArtifactStore       ArtifactStore  `json:"-"`
	StorageGCEnabled    bool           `json:"storage_gc_enabled"`
	StorageGCInterval   time.Duration  `json:"storage_gc_interval"`
	StorageGCLimit      int            `json:"storage_gc_limit"`
	StorageGCNamespaces []string       `json:"storage_gc_namespaces"`

	// CredentialResolver, if set, is called at client creation to resolve the
	// API key for the configured provider. Takes precedence over APIKey.
	// Use in server mode to inject per-user credentials from a database.
	CredentialResolver CredentialResolver `json:"-"`

	// Provider override
	ProviderConfig *providers.Config `json:"-"`
	// TitleProviderConfig overrides the provider client used only for automatic
	// session title generation. This allows a tiny local/Hugging Face-served
	// model to title chats while the main chat model stays unchanged.
	TitleProviderConfig *providers.Config `json:"-"`
	// TitleGenerator takes precedence over TitleProviderConfig/TitleModel.
	// Use this for local, no-server title models.
	TitleGenerator TitleGenerator `json:"-"`
	// LocalTitle configures the default local title generator. It is ignored
	// when TitleGenerator is set.
	LocalTitle *LocalTitleConfig `json:"local_title,omitempty"`

	// Monitoring
	Monitoring *MonitoringSystem `json:"-"`

	// Interactive prompting
	EnablePromptReaderFallback bool `json:"enable_prompt_reader_fallback"`

	// Memory
	EnableMemory   bool `json:"enable_memory"`
	MemoryFailFast bool `json:"memory_fail_fast"`

	// Feature flags
	EnableHooks            bool `json:"enable_hooks"`
	EnableMonitoring       bool `json:"enable_monitoring"`
	DisableTitleGeneration bool `json:"disable_title_generation"`

	// Model parameters
	MaxTokens               int `json:"max_tokens"`
	MaxIterations           int `json:"max_iterations"`
	TurnTokenBudget         int `json:"turn_token_budget"`
	BudgetContinuationLimit int `json:"budget_continuation_limit"`
	ContinuationNudgeLimit  int `json:"continuation_nudge_limit"`
	// TaskNudgeLimit caps how many task-list reminders one turn sends before the
	// turn is allowed to end while tracked tasks are still open. 0 = default (3);
	// a negative value disables the reminder entirely.
	TaskNudgeLimit        int `json:"task_nudge_limit"`
	MaxConsecutiveDenials int `json:"max_consecutive_denials"`

	// System prompt
	SystemPromptTemplate string             `json:"system_prompt_template"`
	PromptConfig         *PromptConfig      `json:"-"`
	Companion            *companion.Profile `json:"companion,omitempty"`

	// ManagedPolicy is what the organization that runs the host imposes on every agent: a text to
	// follow and tools never to use. Set it from an administrator's configuration, not from the end
	// user's. See ManagedPolicy.
	ManagedPolicy *ManagedPolicy `json:"-"`

	// Stop hooks
	StopHooks []StopHook `json:"-"`

	// PreToolHooks are shell-based hooks that fire before each tool execution.
	// Each entry is a HookConfig: {Matcher, Command, Timeout}.
	// Compatible with crush's pre_tool_use hook format:
	//   - exit 0 + no JSON → allow (no-op)
	//   - exit 0 + {"decision":"allow"} → allow + skip permission prompt
	//   - exit 0 + {"updatedInput":"..."} → rewrite tool input
	//   - exit 2 + stderr → deny with reason
	//   - exit 49 → halt entire turn
	// Set via ClientConfig.PreToolHooks or loaded automatically from
	// config.Hooks["pre_tool_use"] by newClient().
	PreToolHooks []PreToolHookConfig `json:"-"`

	// RAG / Plan / Memory
	RAGService     *RAGService    `json:"-"`
	PlanStore      PlanStore      `json:"-"`
	LongTermMemory LongTermMemory `json:"-"`

	// ExtractMemories makes the client read each conversation when its session closes and store what is worth
	// remembering in LongTermMemory (preferences and corrections, procedures that worked, pitfalls). It does nothing
	// without LongTermMemory. Off by default: it stores facts from conversations without the user asking, and costs
	// one model call per session of three user turns or more.
	ExtractMemories bool `json:"extract_memories,omitempty"`

	// RecallMemories puts what LongTermMemory knows about the user in the system prompt of each new session: the
	// preferences and pitfalls, and whatever shares words with the first message, about 1,500 tokens at most. Without
	// it the agent must think of searching memory itself. It needs a LongTermMemory that can list entities (the SQLite
	// store can). Off by default.
	RecallMemories bool `json:"recall_memories,omitempty"`

	// ConsolidateMemories makes the client rewrite a user's memory smaller once it holds 60 observations or more, at
	// most once a day: duplicates merged, conflicting statements resolved in favour of the newer, obsolete ones
	// dropped. It needs ExtractMemories and a LongTermMemory that can be rewritten and can copy itself first (the SQLite
	// store can both); the copy is kept and ListMemorySnapshots and RestoreMemorySnapshot put it back. Off by
	// default because it rewrites what was stored; a result that would lose most of it is refused.
	ConsolidateMemories bool `json:"consolidate_memories,omitempty"`

	// MemoryExtractionModel is the model that reads the conversation for ExtractMemories. Zero uses Model.
	MemoryExtractionModel ModelIdentifier `json:"memory_extraction_model,omitempty"`

	// UserID identifies the caller for PlanStore.CreateOrUpdate (submit_plan)
	// - a host embedding this SDK for more than one user must set this per
	// request/session so plan documents get persisted against the actual
	// caller, not left unattributed. Never sent anywhere itself; only
	// threaded through to PlanStore.
	UserID string `json:"-"`

	// Document conversion
	// DocumentReaderURL is the base URL of a document-reader service. Used
	// to build the default DocumentConverter when that field is left nil.
	DocumentReaderURL string `json:"document_reader_url,omitempty"`

	// DocumentConverter, when set, is used instead of building a
	// service client from DocumentReaderURL. Inject a custom converter
	// implementation (local native reader, seshat-intelligence, marker, etc.)
	// to back the SDK's document-conversion tools. Takes precedence over
	// DocumentReaderURL.
	DocumentConverter documentreader.Converter `json:"-"`

	// DocumentPageRenderer, when set, backs render_document_page so agents can
	// inspect visual pages with a multimodal model after markdown extraction.
	DocumentPageRenderer pdfsmart.PageRenderer `json:"-"`

	// Automation daemon connection (seshat-automation).
	// When set, the schedule_job / list_jobs / update_job / delete_job / pause_job /
	// resume_job / run_job_now tools become functional.
	AutomationServiceURL string `json:"automation_service_url,omitempty"`
	AutomationAPIKey     string `json:"-"`

	// WebSearchKeys provides per-execution web search provider keys.
	// When set, the web_search tool uses these keys instead of reading from the
	// process environment, preventing key leakage across concurrent sessions.
	// Keys are keyed by provider name: "tavily", "exa", "jina", "langsearch".
	WebSearchKeys map[string]string `json:"-"`

	// ResearchKeys provides per-execution credentials for the research tools: reddit_client_id,
	// reddit_client_secret, youtube_api_key, google_places_api_key, trustpilot_api_key. When set, only
	// these are used and the process environment is not read, preventing key leakage across concurrent
	// sessions of a shared server. Without them the environment variables of the same names (upper case)
	// are used, which suits a single-user host. A research tool without its keys is not offered to the model.
	ResearchKeys map[string]string `json:"-"`

	// Optional capability-specific providers for multimodal built-in tools.
	ImageGeneration *ImageGenerationConfig `json:"image_generation,omitempty"`
	TextToSpeech    *TextToSpeechConfig    `json:"text_to_speech,omitempty"`
	SpeechToText    *SpeechToTextConfig    `json:"speech_to_text,omitempty"`

	// RequireSandbox makes the bash tool refuse to run commands when no
	// OS-level sandbox (currently: Landlock, Linux-only) is available on the
	// host, instead of silently falling back to unconfined execution. Leave
	// false for desktop use where refusing to run bash entirely is worse UX
	// than a visible unsandboxed-execution warning; set true for multi-tenant
	// server deployments where unconfined execution must never happen.
	RequireSandbox bool `json:"require_sandbox"`

	// SandboxKind selects the OS-level sandbox backend for bash tool
	// execution. Empty (SandboxKindLocal) preserves existing behavior.
	// SandboxKindDocker routes execution through a persistent Docker
	// container — see SandboxDocker.
	SandboxKind SandboxKind `json:"sandbox_kind,omitempty"`
	// SandboxDocker configures the Docker sandbox backend when SandboxKind
	// is SandboxKindDocker. Zero value uses sane, security-conscious
	// defaults (see sandbox.DefaultDockerConfig).
	SandboxDocker SandboxDockerConfig `json:"-"`

	// EnableToolSearch keeps deferred tools (MCP tools, and any built-in
	// tool registering with ShouldDefer, e.g. skill) out of the model's
	// initial tool list - the model calls the tool_search tool to discover
	// and use them instead. False (the default) leaves the
	// ENABLE_TOOL_SEARCH environment variable as the decider (so existing
	// deployments setting it keep working); true enables it regardless of
	// that variable. There is no way to force it off through this field
	// alone if the environment variable is also set - unset that variable
	// too for a host that wants this field to be the single source of truth.
	EnableToolSearch bool `json:"enable_tool_search,omitempty"`

	// MainAgentTools, when non-empty, limits the tools the main agent sees to
	// exactly these names (e.g. a host's general-purpose agent with a short,
	// curated list). Sub-agents started through the agent tools are not
	// limited by it. Empty (the default) offers every enabled tool.
	MainAgentTools []string `json:"main_agent_tools,omitempty"`

	// MainAgentDeferredTools are tools the main agent does not have loaded but can find with tool_search and then use.
	// Together with MainAgentTools it makes the three levels of a general agent: always loaded, found on demand, and (for
	// everything else) not offered. Sub-agents are not affected. Empty keeps today's behaviour.
	MainAgentDeferredTools []string `json:"main_agent_deferred_tools,omitempty"`
}

// PreToolHookConfig is a single shell hook that runs before a tool call.
type PreToolHookConfig struct {
	Matcher string // regex against tool name; empty = match all
	Command string // shell command to execute
	Timeout int    // timeout in seconds (default 30)
}

// SandboxAvailable reports whether an OS-level sandbox (currently: Landlock,
// Linux-only) is available on this host to confine bash tool execution.
// Host applications use this to surface the unconfined-execution tradeoff to
// the end user (e.g. in a settings UI) rather than leaving it as a
// backend-log-only warning.
func SandboxAvailable() bool {
	return bashTool.SandboxAvailable()
}

// SandboxBackend names the confinement local bash commands get on this host: "landlock", "srt" or "none". "none"
// means commands run with the user's full rights; a host application should say so rather than show a lock.
func SandboxBackend() string {
	return bashTool.SandboxBackend()
}

// DefaultClientConfig returns default client configuration.
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		Model: ModelIdentifier{
			Provider: APIProviderAnthropic,
			Model:    providers.DefaultSonnetModel,
		},
		PermissionMode:    PermissionModeOnRequest,
		MaxTurns:          100,
		AutoCompact:       true,
		PersistSessions:   true,
		SessionStorageDir: runtimepath.SessionStoreDir(""),
		MaxTokens:         8192,
		EnableMemory:      true,
		EnableHooks:       true,
		EnableMonitoring:  true,
		StorageGCEnabled:  true,
		StorageGCInterval: time.Hour,
		StorageGCLimit:    512,
		// Session-scoped artifacts are cleaned up via DeleteSessionDir on session
		// deletion, not by periodic GC. Global namespaces with expiring content
		// would go here if added in the future.
		StorageGCNamespaces: []string{},
	}
}
