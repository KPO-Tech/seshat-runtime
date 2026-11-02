package builtin

import (
	"bufio"
	"context"
	"os"

	"github.com/KPO-Tech/seshat-runtime/internal/audio/stt"
	"github.com/KPO-Tech/seshat-runtime/internal/audio/tts"
	"github.com/KPO-Tech/seshat-runtime/internal/documentreader"
	"github.com/KPO-Tech/seshat-runtime/internal/fim"
	"github.com/KPO-Tech/seshat-runtime/internal/image"
	longterm "github.com/KPO-Tech/seshat-runtime/internal/memory/longterm"
	"github.com/KPO-Tech/seshat-runtime/internal/rag"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/storage"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/system/mcp"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	browsercore "github.com/KPO-Tech/seshat-runtime/internal/web/browser"
	"github.com/KPO-Tech/seshat-runtime/pkg/pdfsmart"
)

// PlanStore is the minimal interface the submit_plan tool needs to persist plan documents.
type PlanStore interface {
	// CreateOrUpdate upserts a plan document by ID. If planID is empty a new document is created.
	CreateOrUpdate(ctx context.Context, planID, sessionID, userID, slug, filename, content string) (id string, version int, err error)
	// SetStatus updates a plan document's status (pending/validated/rejected).
	SetStatus(ctx context.Context, planID string, status string) error
}

// Config controls how builtin tools are assembled for a specific host runtime.
type Config struct {
	WorkingDir                 string
	UserID                     string
	PromptFn                   types.PromptFn
	EnablePromptReaderFallback bool
	InputReader                *bufio.Reader
	MCPManager                 *mcp.MCPClientManager
	BrowserManager             browsercore.Manager
	ArtifactStore              storage.ArtifactStore
	// RAGService enables the rag_search and rag_ingest tools when set.
	// When nil those tools register but always return a "not configured" error.
	RAGService *rag.Service
	// PlanStore enables the submit_plan tool when set.
	PlanStore PlanStore
	// LongTermMemory enables the memory_* tools when set.
	// Provide any implementation that satisfies longterm.Store when wiring the runtime.
	LongTermMemory longterm.Store

	// DocumentReaderURL is the base URL of a document-reader service. Used
	// to build the default DocumentConverter when that field is left nil.
	// When set, the read_file tool converts PDFs to structured markdown.
	// Example: "http://localhost:5001"
	DocumentReaderURL string

	// DocumentConverter, when set, is used instead of building a
	// service client from DocumentReaderURL. It backs read_file,
	// convert_document, and read_document_url, and takes precedence over
	// DocumentReaderURL.
	DocumentConverter documentreader.Converter

	// DocumentPageRenderer renders a specific document page as an image for
	// multimodal inspection tools such as render_document_page.
	DocumentPageRenderer pdfsmart.PageRenderer

	// ImageGenerator enables the generate_image tool when set.
	// Use imageproviders.NewOpenAI(apiKey) or imageproviders.NewGemini(apiKey)
	// from internal/image/providers to create a provider client.
	ImageGenerator image.Generation

	// TTSGenerator enables the text_to_speech tool when set.
	// Use audioproviders.NewOpenAITTS(apiKey) from internal/audio/providers.
	TTSGenerator tts.Generation

	// STTTranscriber enables the speech_to_text tool when set.
	// Use audioproviders.NewOpenAISTT(apiKey) from internal/audio/providers.
	STTTranscriber stt.SpeechToText

	// FIMCompleter enables the code_complete tool when set.
	// Use fimproviders.NewMistral(apiKey) or fimproviders.NewDeepSeek(apiKey)
	// from internal/fim/providers to create a provider client.
	FIMCompleter fim.Completer

	// AutomationServiceURL enables schedule_job and related tools when set.
	// Should point to the seshat-automation daemon, e.g. "http://localhost:8090".
	// When empty those tools register but return a "not configured" error.
	AutomationServiceURL string
	// AutomationAPIKey is the API key the tools use to authenticate with the daemon.
	AutomationAPIKey string

	// WebSearchKeys provides per-execution web search provider API keys.
	// When set, the web_search tool uses these explicit keys instead of reading
	// from the process environment — preventing key leakage across concurrent
	// jobs from different owners. Keys are keyed by provider name:
	// "tavily", "exa", "jina", "langsearch".
	WebSearchKeys map[string]string

	// ResearchKeys provides per-execution credentials for the research tools (Reddit, YouTube, Google
	// Places, Trustpilot). When set, only these keys are used and the process environment is not read,
	// so one organization's key cannot reach another's run. Keys are named reddit_client_id,
	// reddit_client_secret, youtube_api_key, google_places_api_key and trustpilot_api_key.
	ResearchKeys map[string]string

	// RequireSandbox makes the bash tool refuse to run commands when no
	// OS-level sandbox (currently: Landlock, Linux-only) is available on the
	// host, instead of silently falling back to unconfined execution.
	RequireSandbox bool

	// SandboxKind selects the OS-level sandbox backend the bash tool
	// attempts before falling back to Landlock/local. Empty preserves
	// today's behavior. sandbox.EnvironmentDocker routes execution through
	// a persistent Docker container — see SandboxDocker.
	SandboxKind sandbox.EnvironmentKind
	// SandboxDocker configures the Docker sandbox backend when SandboxKind
	// is sandbox.EnvironmentDocker. Zero value uses sandbox.DefaultDockerConfig().
	SandboxDocker sandbox.DockerExecutorConfig
	// Sandboxd configures the sidecar backend when SandboxKind is
	// sandbox.EnvironmentSandboxed. An unavailable sidecar refuses every command.
	Sandboxd sandbox.SandboxdConfig

	// EnableToolSearch keeps deferred tools (including MCP tools and any
	// tool registering with ShouldDefer) out of the model's initial tool
	// list; the model must call tool_search to discover and use them.
	// False (the default) keeps every tool visible from the start.
	EnableToolSearch bool
}

func DefaultConfig() *Config {
	workingDir, err := os.Getwd()
	if err != nil || workingDir == "" {
		workingDir = "."
	}
	artifactStore, _ := storage.DefaultArtifactStore()
	return &Config{
		WorkingDir:     workingDir,
		MCPManager:     mcp.GlobalMCPManager(),
		BrowserManager: browsercore.DefaultManager(),
		ArtifactStore:  artifactStore,
	}
}

func normalizeConfig(config *Config) *Config {
	if config == nil {
		config = DefaultConfig()
	}
	if config.WorkingDir == "" {
		config.WorkingDir = DefaultConfig().WorkingDir
	}
	if config.MCPManager == nil {
		config.MCPManager = mcp.GlobalMCPManager()
	}
	if config.BrowserManager == nil {
		config.BrowserManager = browsercore.DefaultManager()
	}
	if config.ArtifactStore == nil {
		config.ArtifactStore, _ = storage.DefaultArtifactStore()
	}
	return config
}
