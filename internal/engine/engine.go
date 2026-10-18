package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/hooks"
	"github.com/KPO-Tech/seshat-runtime/internal/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/monitoring"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	browsercore "github.com/KPO-Tech/seshat-runtime/internal/web/browser"
)

// SessionStore persists canonical session metadata and transcript state.
type SessionStore interface {
	SaveSessionState(sessionID types.SessionID, metadata *types.SessionMetadata, previousMessages []types.Message, currentMessages []types.Message) error
}

// sessionRestorer is an optional capability of a SessionStore implementation
// that supports loading a previously persisted session back into memory.
// state.Store satisfies this interface; a nil or stub store does not.
type sessionRestorer interface {
	RestoreSessionState(sessionID types.SessionID) (*types.SessionMetadata, []types.Message, error)
}

// historyLoader is an optional capability of a SessionStore: reading back the messages that compactions archived.
// state.Store satisfies it.
type historyLoader interface {
	LoadHistory(sessionID types.SessionID) (archived, current []types.Message, err error)
}

// Engine orchestrates query sessions.
type Engine struct {
	mu                   sync.RWMutex // protects apiClient and loop.apiClient swaps
	apiClient            *providers.Client
	titleAPIClient       *providers.Client
	titleGenerator       TitleGenerator
	orchestrator         *execution.Orchestrator
	compactor            *compact.Engine
	promptAssembler      *prompt.Assembler
	promptBuilder        *prompt.Builder
	loop                 *Loop
	permissionIntegrator *permissions.Integrator
	toolRegistry         *tool.Registry
	monitoring           *monitoring.System
	sessionStore         SessionStore
	config               *Config
	hookRegistry         *hooks.Registry
	hookExecutor         *hooks.Executor
	promptFn             types.PromptFn
	memoryService        *memory.Service
	browserManager       browsercore.Manager
	// onSessionTitled, when set, is called once after the first completed turn
	// with the AI-generated session title.
	onSessionTitled func(sessionID types.SessionID, title string)
}

// TitleGenerator can generate a short title without going through the regular
// provider client. Hosts use it for local-only title models.
type TitleGenerator interface {
	GenerateTitle(ctx context.Context, firstUserMsg string) (string, error)
}

// NewEngine creates a new query engine.
func NewEngine(
	apiClient *providers.Client,
	orchestrator *execution.Orchestrator,
	compactor *compact.Engine,
	promptAssembler *prompt.Assembler,
	permissionIntegrator *permissions.Integrator,
	toolRegistry *tool.Registry,
	sessionStore SessionStore,
	config *Config,
	memoryService *memory.Service,
	monitoringSys *monitoring.System,
) *Engine {
	if config == nil {
		config = DefaultConfig()
	}
	config.WorkingDirectory = resolveWorkingDirectory(config.WorkingDirectory)

	if monitoringSys == nil {
		// No monitoring system provided; use a discard logger so the engine
		// never writes unexpectedly to stdout/stderr (e.g. during TUI mode).
		monitoringSys = monitoring.NewSystem(monitoring.NewLoggerWithConfig(
			&monitoring.LoggerConfig{Output: "file", FilePath: os.DevNull},
		))
	}

	promptBuilder := prompt.NewBuilder(promptAssembler, prompt.DefaultBuilderConfig())

	hookRegistry := hooks.NewRegistry()
	hookExecutor := hooks.NewExecutor(hookRegistry)

	if orchestrator != nil && config.MaxConsecutiveDenials > 0 {
		orchestrator.SetDenialLimitConfig(types.DenialLimitConfig{MaxConsecutiveDenials: config.MaxConsecutiveDenials})
	}

	var providerConfig *providers.Config
	if apiClient != nil {
		providerConfig = apiClient.Config()
		apiClient.SetMonitoring(monitoringSys)
	}
	loop := NewLoop(
		apiClient,
		orchestrator,
		compactor,
		promptAssembler,
		permissionIntegrator,
		hookExecutor,
		loopConfigFromConfig(config),
		monitoringSys,
		providerConfig,
	)

	if orchestrator != nil {
		orchestrator.SetMonitoring(monitoringSys)
	}

	return &Engine{
		apiClient:            apiClient,
		orchestrator:         orchestrator,
		compactor:            compactor,
		promptAssembler:      promptAssembler,
		promptBuilder:        promptBuilder,
		loop:                 loop,
		permissionIntegrator: permissionIntegrator,
		toolRegistry:         toolRegistry,
		sessionStore:         sessionStore,
		config:               config,
		hookRegistry:         hookRegistry,
		hookExecutor:         hookExecutor,
		memoryService:        memoryService,
		monitoring:           monitoringSys,
		browserManager:       config.BrowserManager,
	}
}

// SetTitleAPIClient sets the provider client used only for session title
// generation. Passing nil falls back to the main API client.
func (e *Engine) SetTitleAPIClient(apiClient *providers.Client) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.titleAPIClient = apiClient
}

// SetTitleGenerator sets a local title generator. When present it is tried
// before the provider-backed title client.
func (e *Engine) SetTitleGenerator(generator TitleGenerator) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.titleGenerator = generator
}

// SetAPIClient swaps the loop-facing API client.
func (e *Engine) SetAPIClient(apiClient *providers.Client) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.apiClient = apiClient
	if e.loop != nil {
		e.loop.apiClient = apiClient
		if apiClient != nil {
			e.loop.providerConfig = apiClient.Config()
		} else {
			e.loop.providerConfig = nil
		}
	}
	if e.permissionIntegrator != nil {
		e.permissionIntegrator.SetAutoModeProviderClient(apiClient, e.config.Model)
	}
}

// OpenSession loads a previously persisted session by ID so it can receive new
// turns. Returns an error if the store does not support session restoration or
// if the session is not found.
func (e *Engine) OpenSession(ctx context.Context, sessionID types.SessionID) (*Session, error) {
	r, ok := e.sessionStore.(sessionRestorer)
	if !ok {
		return nil, fmt.Errorf("session store does not support restoration")
	}
	meta, msgs, err := r.RestoreSessionState(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to restore session %s: %w", sessionID, err)
	}
	return e.NewSessionFromState(ctx, sessionID, meta, msgs)
}

// HookRegistry returns the engine's hook registry for external hook registration.
func (e *Engine) HookRegistry() *hooks.Registry {
	return e.hookRegistry
}

// HookExecutor returns the engine's hook executor for external use.
func (e *Engine) HookExecutor() *hooks.Executor {
	return e.hookExecutor
}

type promptAwareTool interface {
	SetPromptFn(types.PromptFn)
}

// SetPromptFn wires the runtime prompt bridge into permissions and prompt-aware tools.
func (e *Engine) SetPromptFn(fn types.PromptFn) {
	e.promptFn = fn
	if e.permissionIntegrator != nil {
		e.permissionIntegrator.SetPromptFn(fn)
	}
	if e.toolRegistry == nil {
		return
	}
	for _, registeredTool := range e.toolRegistry.List() {
		if promptAware, ok := registeredTool.(promptAwareTool); ok {
			promptAware.SetPromptFn(fn)
		}
	}
}

// AutoModeAvailable reports whether the permission integrator has an AI-backed
// auto-mode classifier wired up and ready.
func (e *Engine) AutoModeAvailable() bool {
	if e == nil || e.permissionIntegrator == nil {
		return false
	}
	return e.permissionIntegrator.AutoModeAvailable()
}

// GetMonitoring returns the monitoring system for external integration.
func (e *Engine) GetMonitoring() *monitoring.System {
	return e.monitoring
}

func (e *Engine) workingDirectory() string {
	if e == nil || e.config == nil {
		return "."
	}
	return resolveWorkingDirectory(e.config.WorkingDirectory)
}

func (e *Engine) defaultPermissionContext() *types.PermissionContext {
	mode := e.config.PermissionMode
	if mode == "" {
		mode = types.PermissionModeOnRequest
	}
	return &types.PermissionContext{
		Mode:                             mode,
		IsBypassPermissionsModeAvailable: mode == types.PermissionModeBypass,
		IsAutoModeAvailable:              e.permissionIntegrator != nil && e.permissionIntegrator.AutoModeAvailable(),
	}
}

func (e *Engine) memoryContext(projectID string) string {
	if e == nil || e.memoryService == nil {
		return ""
	}
	return e.memoryService.Context(projectID)
}

// SetOnSessionTitled registers a callback that is invoked once — as soon as
// the session's first message is submitted, running concurrently with that
// first turn rather than waiting for it to finish — with the AI-generated
// session title. Passing nil disables the feature.
func (e *Engine) SetOnSessionTitled(fn func(types.SessionID, string)) {
	e.onSessionTitled = fn
}

func (e *Engine) titleClientAndModel() (*providers.Client, types.ModelIdentifier) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	client := e.titleAPIClient
	if client == nil {
		client = e.apiClient
	}
	model := e.config.TitleModel
	if strings.TrimSpace(model.Model) == "" || model.Provider == "" {
		model = e.config.Model
	}
	return client, model
}

func (e *Engine) localTitleGenerator() TitleGenerator {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.titleGenerator
}

// titleSystemPrompt is the system prompt used for session title generation.
const titleSystemPrompt = `You generate ultra-short session titles.
Rules:
- Maximum 6 words
- No quotes, no punctuation at the end
- Match the language of the user message
- Be specific and descriptive, not generic (avoid words like "Chat", "Question", "Discussion")
- Reply with ONLY the title, nothing else`

// generateTitleAsync calls the LLM in a background goroutine to produce a
// short session title from the first user message and then invokes the
// onSessionTitled callback with the result.
func (e *Engine) generateTitleAsync(sessionID types.SessionID, firstUserMsg string) {
	if e.onSessionTitled == nil {
		return
	}
	const maxInputRunes = 500
	runes := []rune(firstUserMsg)
	if len(runes) > maxInputRunes {
		firstUserMsg = string(runes[:maxInputRunes])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if generator := e.localTitleGenerator(); generator != nil {
		if title, err := generator.GenerateTitle(ctx, firstUserMsg); err == nil {
			if title = cleanGeneratedTitle(title); title != "" {
				e.onSessionTitled(sessionID, title)
				return
			}
		}
	}

	apiClient, titleModel := e.titleClientAndModel()
	if apiClient == nil {
		return
	}
	req := types.APIRequest{
		Model: titleModel,
		// Reasoning models spend part of the budget thinking before the title.
		MaxTokens:    512,
		Stream:       false,
		SystemPrompt: titleSystemPrompt,
		Messages: []types.Message{
			types.UserMessage("title-req", firstUserMsg),
		},
	}
	resp, err := apiClient.CreateMessage(ctx, req)
	if err != nil || resp == nil {
		return
	}
	// Extract the text from the first content block.
	title := ""
	for _, block := range resp.Content {
		if t, ok := block.(types.TextContent); ok {
			title = strings.TrimSpace(t.Text)
			break
		}
	}
	if title == "" {
		return
	}
	if title = cleanGeneratedTitle(title); title != "" {
		e.onSessionTitled(sessionID, title)
	}
}

func cleanGeneratedTitle(title string) string {
	if end := strings.LastIndex(title, "</think>"); end >= 0 {
		title = title[end+len("</think>"):]
	}
	title = strings.TrimSpace(title)
	title = strings.Trim(title, "\"'` \t\r\n")
	title = strings.TrimRight(title, ".:;!?")
	return strings.TrimSpace(title)
}
