# SDK Guide

## Installation

```go
import "github.com/KPO-Tech/seshat-runtime/pkg/sdk"
```

---

## Basic usage

### Single-turn query

```go
client, err := sdk.NewClient(&sdk.ClientConfig{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    Model:  sdk.ModelIdentifier{Provider: "anthropic", Model: "claude-sonnet-5-5"},
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()

response, err := client.Ask(ctx, "Explain how goroutines work.", nil)
if err != nil {
    log.Fatal(err)
}
fmt.Println(response.Content)
```

### Multi-turn session

```go
session, err := client.CreateSession(ctx)
if err != nil {
    log.Fatal(err)
}
defer session.Close()

r1, _ := session.SubmitMessage(ctx, "List the files in the current directory.")
fmt.Println(r1.Content)

r2, _ := session.SubmitMessage(ctx, "Now show me the content of main.go.")
fmt.Println(r2.Content)
```

### Resume a persisted session

```go
// Session IDs are returned in every response
savedID := r1.SessionID

restored, err := client.LoadSession(ctx, sdk.SessionID(savedID))
// Continue the session from where it was left off
```

### Inject a custom session backend

```go
client, err := sdk.NewClient(&sdk.ClientConfig{
    PersistSessions: false,
    SessionBackend:  sdk.NewMemorySessionBackend(),
})
```

### Inject artifact storage

```go
store, err := sdk.NewArtifactStoreFromConfig(sdk.StorageConfig{
    Provider:  sdk.StorageProviderLocal,
    LocalPath: "/tmp/seshat-artifacts",
})

client, err := sdk.NewClient(&sdk.ClientConfig{
    ArtifactStore: store,
})
```

---

## ClientConfig reference

```go
type ClientConfig struct {
    // ── Provider ──────────────────────────────────────────────────────────
    APIKey    string               // required for cloud providers
    Model     ModelIdentifier      // {Provider: "anthropic", Model: "claude-sonnet-5-5"}
    MaxTokens int                  // max output tokens (default 8192)

    // Injected per-request resolver — takes precedence over APIKey.
    // Useful for multi-user deployments where each request has its own key.
    CredentialResolver CredentialResolver

    // ── Loop limits ───────────────────────────────────────────────────────
    MaxTurns      int // total turns per session (default 100)
    MaxIterations int // model→tool cycles per turn (default 100)

    // ── Context management ────────────────────────────────────────────────
    AutoCompact             bool // compact transcript when context window fills (default true)
    CompactThreshold        float64 // share of the window that triggers compaction (default 0.85)
    CompactTarget           float64 // share the recent messages kept verbatim may take afterwards (default 0.5, below the threshold)
    TurnTokenBudget         int  // if set, pause when budget is exhausted per turn
    BudgetContinuationLimit int  // max budget-continuation cycles (0 = unlimited)
    ContinuationNudgeLimit  int  // max text nudges before giving up (default 3)
    TaskNudgeLimit          int  // max task-list reminders per turn before the turn may end with tracked tasks still open (default 3; negative disables)

    // ── Permissions ───────────────────────────────────────────────────────
    PermissionMode PermissionMode // bypass | auto | acceptEdits | onRequest | never | granular

    // ── Persistence ───────────────────────────────────────────────────────
    PersistSessions   bool             // enable default filesystem persistence
    SessionSQLitePath string           // use SQLite-backed session persistence
    SessionBackend    SessionBackend   // inject a custom persistence backend
    SessionStore      SessionStore     // inject a fully-built session store
    WorkingDir        string           // root for file tool operations (default: os.Getwd())

    // ── Features ──────────────────────────────────────────────────────────
    EnableMemory    bool // long-term memory (project/user/cross-session), default true
    MemoryFailFast  bool // return error if memory init fails (default false)
    EnableHooks            bool // lifecycle hooks, default true
    EnableMonitoring       bool // Prometheus metrics, default true
    DisableTitleGeneration bool // disable background session title generation (default false)
    EnableToolSearch       bool // hide deferred tools (MCP, skill) behind the tool_search tool instead of listing them all upfront (default false; ENABLE_TOOL_SEARCH env var is the fallback when this is false)
    MainAgentTools         []string // limit the main agent to exactly these tool names (sub-agents keep the whole registry); empty offers every enabled tool
    MainAgentDeferredTools []string // tools the main agent does not load but finds with tool_search and then uses; a tool in neither list is not offered to it

    // ── MCP ───────────────────────────────────────────────────────────────
    MCPServers []MCPServerConfig // MCP servers to connect on startup

    // ── Storage / artifacts ──────────────────────────────────────────────
    StorageConfig *StorageConfig // per-client artifact storage config
    ArtifactStore ArtifactStore  // inject a fully-built artifact store

    // ── Callbacks ─────────────────────────────────────────────────────────
    ResponseChunkFn func(ResponseChunk)  // streaming text chunks
    RuntimeEventFn  func(RuntimeEvent)   // structured engine events (turns, tools, permissions, …)
    ProgressFn      func(ToolProgress)   // tool execution progress
    PromptFn        PromptFn             // user input callback (headless: return empty string)
    OnSessionTitled func(id SessionID, title string) // callback when AI auto-generates a session title

    // ── Prompt customization ──────────────────────────────────────────────
    SystemPromptTemplate string       // replace default system prompt
    PromptConfig         *PromptConfig // stage overlays, tool hints, append text
    ManagedPolicy        *ManagedPolicy // what an organization imposes on every agent: text to follow, tools never to use

    // Document conversion
    DocumentReaderURL string // optional document-reader base URL for PDF/document conversion
    DocumentConverter documentreader.Converter // overrides DocumentReaderURL with a custom backend; documentreader.GenericClient covers any HTTP server speaking "multipart upload, JSON response"

    // ── Stop hooks ────────────────────────────────────────────────────────
    StopHooks []StopHook // post-turn policy checks (append messages, request continuation)
}
```

---

## PromptConfig

```go
type PromptConfig struct {
    // Replaces the entire system prompt (overrides all defaults)
    SystemPrompt *string

    // Text appended after all assembled sections
    AppendSystemPrompt *string

    // Active execution stage (injects a stage-specific overlay section)
    Stage ExecutionStage  // "tool_call" | "tool_result" | "continuation" | "plan"

    // Per-stage custom overlay text (overrides built-in templates)
    StageOverrides map[ExecutionStage]string

    // Per-tool extra guidance appended to each tool's description
    ToolHints map[string]string
}
```

Example — restrict bash to read-only and activate plan stage:

```go
client, _ := sdk.NewClient(&sdk.ClientConfig{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    Model:  sdk.ModelIdentifier{Provider: "anthropic", Model: "claude-sonnet-5-5"},
    PromptConfig: &sdk.PromptConfig{
        Stage: sdk.StagePlan,
        ToolHints: map[string]string{
            "bash": "Only run read-only commands. Do not modify any files.",
        },
    },
})
```

---

## ManagedPolicy

A host that serves an organization (SeshatCloud does) imposes rules on every agent through `ClientConfig.ManagedPolicy`. Set it from an administrator's configuration, never from the end user's own settings.

```go
client, _ := sdk.NewClient(&sdk.ClientConfig{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    ManagedPolicy: &sdk.ManagedPolicy{
        Instructions:   "Never send customer data outside the company.",
        ForbiddenTools: []string{"bash", "mcp__crm__delete_contact"},
    },
})
```

- `Instructions` is appended to the system prompt, after everything else including a per-session append prompt, so a session cannot replace or drop it. It is a soft guarantee: a model follows it very reliably, not absolutely.
- `ForbiddenTools` is a hard guarantee, applied by the program: the tools are removed from the list the model sees, and a deny rule that outranks every other permission rule refuses a call to them anyway, in every permission mode, `bypass` included. A name matches the tool and its aliases; an MCP tool is named like `mcp__server__tool`.

## Streaming

### ResponseChunkFn — text deltas

```go
client, _ := sdk.NewClient(&sdk.ClientConfig{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    ResponseChunkFn: func(chunk sdk.ResponseChunk) {
        if chunk.Type == sdk.ResponseChunkTypeContentBlockDelta {
            fmt.Print(chunk.Delta)
        }
    },
})
```

`SubmitMessage` always streams internally. The callback fires on each text delta. The returned `*SessionResponse` is delivered when the turn is complete.

### RuntimeEventFn — structured engine events

```go
client, _ := sdk.NewClient(&sdk.ClientConfig{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    RuntimeEventFn: func(event sdk.RuntimeEvent) {
        switch event.Type {
        case sdk.RuntimeEventTypeTurnStarted:
            fmt.Printf("turn %d started\n", event.TurnNumber)
        case sdk.RuntimeEventTypeToolProgress:
            fmt.Printf("tool %s: %s\n", event.ToolProgress.ToolName, event.ToolProgress.Message)
        case sdk.RuntimeEventTypeToolPermissionRequired:
            fmt.Printf("permission required: %s\n", event.PermissionRequest.ToolName)
        case sdk.RuntimeEventTypeTurnCompleted:
            fmt.Printf("turn complete: %s\n", event.StopReason)
        }
    },
})
```

Full event type list: `turn.started`, `turn.completed`, `turn.failed`, `response.chunk`, `tool.progress`, `tool.permission_required`, `prompt.request`, `plan.submitted`, `goal.updated`, `agent.spawn.begin`, `agent.spawn.end`. (`plan.status_changed` is declared as a reserved constant but not currently emitted by the engine.)

### EventQueue — per-session async channel

```go
session, _ := client.CreateSession(ctx)
queue := session.GetEventQueue()

// Drain in a goroutine before calling SubmitMessage
go func() {
    for chunk := range queue.Recv() {
        fmt.Printf("[chunk] %s\n", chunk.Delta)
    }
}()

response, _ := session.SubmitMessage(ctx, "hello")
```

---

## Permission modes

```go
const (
    PermissionModeBypass      = "bypass"       // skip all checks — fully autonomous
    PermissionModeAuto        = "auto"         // LLM classifier auto-approves safe actions
    PermissionModeAcceptEdits = "acceptEdits"  // auto-approve file operations in working dir
    PermissionModeOnRequest   = "onRequest"    // ask user for each action (default)
    PermissionModeNever       = "never"        // deny anything non-trivially safe (headless)
)
```

For fully automated pipelines use `bypass`. For headless servers where no user is present use `never`.

---

## Session response

```go
type SessionResponse struct {
    SessionID   string
    Content     string            // extracted final text
    Messages    []types.Message   // full updated transcript
    StopReason  string            // "end_turn" | "max_tokens" | "stop_sequence"
    TurnNumber  int
    IsComplete  bool
    Compacted   bool              // true if auto-compact ran during this turn
    Usage       *types.TokenUsage // InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens
    ToolUses    []types.ToolUseContent
    ToolResults []tool.CallResult
}
```

---

## Session methods reference

```go
// Message submission
func (s *Session) SubmitMessage(ctx context.Context, content string) (*SessionResponse, error)
func (s *Session) SubmitMessageWithContent(ctx context.Context, text string, images []ImageContent) (*SessionResponse, error)

// Session metadata
func (s *Session) GetID() SessionID
func (s *Session) GetStatus() SessionStatus
func (s *Session) GetMetadata() *SessionMetadata
func (s *Session) GetMessages() []Message    // full transcript
func (s *Session) GetTurnNumber() int

// Configuration (per-session overrides)
func (s *Session) SetPermissionMode(mode PermissionMode)
func (s *Session) SetSystemPromptTemplate(text string)
func (s *Session) SetAppendSystemPrompt(text string)
func (s *Session) SetWorkingDirectory(path string)

// Plan mode
func (s *Session) ForcePlanMode() // enter plan mode, starting a fresh plan-review cycle
func (s *Session) ClearPlanMode() // leave plan mode, restoring the previous permission mode
func (s *Session) ApprovePlan()   // record the user's approval of the plan this session submitted

// Plan approval for a host that persists its sessions and reloads them per turn,
// so it has no live *Session at the moment the user decides (see docs/planning.md)
func ApprovePlan(sessionID SessionID)

// Callbacks (override client-level callbacks for this session)
func (s *Session) SetResponseChunkFn(fn func(ResponseChunk))
func (s *Session) SetRuntimeEventFn(fn func(RuntimeEvent))
func (s *Session) SetProgressFn(fn func(ToolProgress))

// Queues
func (s *Session) GetEventQueue() *EventQueue
func (s *Session) GetRuntimeEventQueue() *RuntimeEventQueue

// Tool management
func (s *Session) RegisterTool(tool Tool) error
func (s *Session) UnregisterTool(name string) error

// Lifecycle
func (s *Session) Interrupt() error
func (s *Session) Close() error
```

---

## Long-term memory

Enable long-term memory to persist learnings across sessions:

```go
client, _ := sdk.NewClient(&sdk.ClientConfig{
    EnableMemory:   true,   // default: true
    MemoryFailFast: false,  // continue without memory on init failure (default)
})

// Check if memory init had issues (non-fatal)
if err := client.MemoryInitError(); err != nil {
    log.Printf("memory unavailable: %v", err)
}
```

Three memory tiers are loaded automatically:
- **Project memory** — scoped to the current working directory
- **User memory** — user-wide (`~/.seshat/memory/`)
- **Cross-session** — patterns learned across all sessions

---

## Custom tool

Implement the `Tool` interface from `internal/tools/contract/`:

```go
type MyTool struct{}

func (t *MyTool) Definition() tool.Definition {
    return tool.Definition{
        Name:        "my_tool",
        Description: "Does something useful.",
        InputSchema: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "input": map[string]any{"type": "string"},
            },
            "required": []string{"input"},
        },
        IsReadOnly:        true,
        IsConcurrencySafe: true,
    }
}

func (t *MyTool) Call(ctx context.Context, input tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
    value, _ := input.Parsed["input"].(string)
    return tool.NewTextResult("result: " + value), nil
}

// Remaining interface methods: ValidateInput, BackfillInput, CheckPermissions,
// IsConcurrencySafe, IsReadOnly, IsEnabled, FormatResult, Description
```

Register before creating sessions:

```go
client.RegisterTool(&MyTool{})
// or per-session:
session.RegisterTool(&MyTool{})
```

---

## Hook lifecycle

```go
// Events include HookEventPreToolUse, HookEventPostToolUse, HookEventSessionStart,
// HookEventSessionEnd, HookEventTurnStart and HookEventTurnEnd.
id := client.RegisterHook(sdk.HookEventPreToolUse, func(ctx context.Context, p sdk.HookProgress) (*sdk.HookResult, error) {
    // Return nil, nil to continue, or a result with Action "deny" or "stop".
    return nil, nil
})

client.HookRegistry().Remove(id)
```

---

## MCP servers

```go
client, _ := sdk.NewClient(&sdk.ClientConfig{
    MCPServers: []sdk.MCPServerConfig{
        {Name: "github",   Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}},
        {Name: "postgres", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-postgres", "postgresql://..."}},
    },
})

// Hot-reload MCP servers without recreating the client
client.ReloadMCPServers(ctx, newServers)
```

---

## Observability

### Prometheus metrics

```go
m := client.GetMonitoring()

// Prometheus text format
snapshot, _ := m.GetMetricsSnapshot("prometheus")
fmt.Println(snapshot)

// JSON
snapshot, _ = m.GetMetricsSnapshot("json")
```

Tracked: API request counts/latency, tool call counts/latency, permission denials, circuit breaker state, prompt cache hits/writes, active sessions, memory usage.

### OpenTelemetry tracing

```go
import "github.com/KPO-Tech/seshat-runtime/internal/monitoring"

shutdown, err := monitoring.InitTracer(ctx, "my-service")
defer shutdown(ctx)
```

Sends spans to `OTEL_EXPORTER_OTLP_ENDPOINT` (gRPC). If the env var is unset, a no-op provider is installed and nothing is exported. Set `OTEL_EXPORTER_OTLP_INSECURE=false` to enable TLS.

---

## Error handling

```go
resp, err := session.SubmitMessage(ctx, "prompt")
if err != nil {
    var engineErr *types.EngineError
    if errors.As(err, &engineErr) {
        switch engineErr.Code {
        case types.ErrCodeAPIRateLimit:
            // transient — internally retried with backoff; reaching here means retries exhausted
        case types.ErrCodeAPIAuth:
            // invalid or expired API key
        case types.ErrCodeContextOverflow:
            // conversation too long; enable AutoCompact or start a new session
        case types.ErrCodePermissionDenied:
            // tool call blocked by permission mode
        }
    }
}
```

Permanent errors (auth, invalid input) are never retried. Transient errors (rate limit, network, server overload) are retried with exponential backoff.


### Multimodal Tool Providers

- `ImageGeneration *ImageGenerationConfig`: Selects the provider/model/base URL used by the `generate_image` built-in tool.
- `TextToSpeech *TextToSpeechConfig`: Selects the provider/options used by the `text_to_speech` built-in tool.
- `SpeechToText *SpeechToTextConfig`: Selects the provider/options used by the `speech_to_text` built-in tool.
