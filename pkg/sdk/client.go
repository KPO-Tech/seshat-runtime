package sdk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreagent "github.com/KPO-Tech/seshat-runtime/internal/agent"
	coregoal "github.com/KPO-Tech/seshat-runtime/internal/agent/goal"
	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	coretasks "github.com/KPO-Tech/seshat-runtime/internal/runtime/tasks"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"github.com/KPO-Tech/seshat-runtime/internal/storage"
	"github.com/KPO-Tech/seshat-runtime/internal/titlegen"
	agentTool "github.com/KPO-Tech/seshat-runtime/internal/tools/agents"
	bashTool "github.com/KPO-Tech/seshat-runtime/internal/tools/bash"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/system/mcp"
	skillTool "github.com/KPO-Tech/seshat-runtime/internal/tools/system/skills"
	taskTool "github.com/KPO-Tech/seshat-runtime/internal/tools/task"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	browsercore "github.com/KPO-Tech/seshat-runtime/internal/web/browser"
	"github.com/KPO-Tech/seshat-runtime/pkg/companion"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
)

// Client provides a high-level SDK for headless AI operations.
type Client struct {
	queryEngine     *engine.Engine
	orchestrator    *execution.Orchestrator
	registry        *registry.Registry
	store           SessionStore
	config          *ClientConfig
	memoryInitErr   error
	mcpResult       *MCPIntegrationResult
	mcpMu           sync.Mutex
	ownedStore      bool
	browser         browsercore.Manager
	artifacts       ArtifactStore
	reaper          *storage.Reaper
	closeOnce       sync.Once
	closeErr        error
	untitledCounter atomic.Int64
	taskStorePath   string
	goalBackend     *coregoal.SQLiteBackend
}

type promptAwareTool interface {
	SetPromptFn(PromptFn)
}

// NewClient creates a new SDK client.
func NewClient(config *ClientConfig) (*Client, error) {
	// perf: NewClient is not cached anywhere upstream (seshat-ai's
	// seshat-backend calls it fresh on every chat turn, see
	// seshat-backend/internal/query/runtime.go's clientForInput) - registryMs
	// and mcpMs below are logged to find out how much of that per-turn cost
	// is tool-registry bootstrap vs re-synchronizing MCP servers.
	newClientStart := time.Now()
	var registryMs, mcpMs, storeMs, memMs int64

	if config == nil {
		config = DefaultClientConfig()
	}
	if config.WorkingDir == "" {
		workingDir, err := os.Getwd()
		if err != nil || workingDir == "" {
			workingDir = "."
		}
		config.WorkingDir = workingDir
	}

	// Resolve API key: CredentialResolver takes precedence over APIKey.
	apiKey := config.APIKey
	if config.CredentialResolver != nil {
		if resolved, resolveErr := config.CredentialResolver.ResolveAPIKey(context.Background(), string(config.Model.Provider)); resolveErr == nil && resolved != "" {
			apiKey = resolved
		} else if resolveErr != nil {
			log.Printf("[sdk] CredentialResolver failed for provider %s: %v", config.Model.Provider, resolveErr)
		}
	}

	// Provider
	var apiClient *providers.Client
	if config.ProviderConfig != nil {
		pc := *config.ProviderConfig
		if pc.Provider == "" {
			pc.Provider = config.Model.Provider
		}
		if pc.APIKey == "" {
			pc.APIKey = apiKey
		}
		apiClient = providers.NewClientWithConfig(apiKey, &pc)
	} else {
		apiClient = providers.NewClient(apiKey, config.Model.Provider)
	}
	titleAPIClient := buildTitleAPIClient(config, apiKey, apiClient)
	titleGenerator, err := buildTitleGenerator(config)
	if err != nil {
		return nil, err
	}

	artifactStore := initArtifactStore(config)
	browserManager, reaper := initBrowserManager(config, artifactStore)
	config.BrowserRemoteControlURL = strings.TrimSpace(config.BrowserRemoteControlURL)

	// Core subsystems
	orchestrator := execution.NewOrchestrator()
	compactConfig := compact.DefaultConfig()
	compactConfig.SummaryModel = config.CompactSummaryModel
	compactor := compact.NewEngine(apiClient, compactConfig)
	promptAssembler := prompt.NewAssembler()
	promptAssembler.SetDefaultSections(prompt.DefaultSystemPromptSections())
	permissionEngine, err := newPermissionEngine(config)
	if err != nil {
		return nil, err
	}
	permissionIntegrator := permissions.NewIntegrator(permissionEngine)
	permissionIntegrator.SetAutoModeProviderClient(apiClient, config.Model)

	// Engine config
	queryConfig := buildEngineConfig(config)
	queryConfig.ManagedInstructions = managedInstructionsText(config.ManagedPolicy)
	queryConfig.BrowserManager = browserManager

	// Tool registry
	registryStart := time.Now()
	reg, err := initBuiltinRegistry(config, browserManager, artifactStore)
	if err != nil {
		return nil, fmt.Errorf("failed to create tool registry: %w", err)
	}
	registryMs = time.Since(registryStart).Milliseconds()

	// MCP servers
	var mcpResult *MCPIntegrationResult
	if len(config.MCPServers) > 0 {
		mcpStart := time.Now()
		mcpResult = mcp.IntegrateMCPServersWithOptions(context.Background(), reg, config.MCPServers, nil)
		mcpMs = time.Since(mcpStart).Milliseconds()
	}

	storeStart := time.Now()
	store, ownedStore, err := initSessionStore(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create session store: %w", err)
	}
	storeMs = time.Since(storeStart).Milliseconds()
	var sessionStore engine.SessionStore
	if store != nil {
		sessionStore = store
	}

	memStart := time.Now()
	memSvc, memInitErr := initMemoryService(config)
	memMs = time.Since(memStart).Milliseconds()
	if memInitErr != nil && config.MemoryFailFast {
		return nil, fmt.Errorf("memory service initialization failed: %w", memInitErr)
	}

	monitoringSys := initMonitoringSystem(config)

	var goalBackend *coregoal.SQLiteBackend
	if config.SessionSQLitePath != "" {
		if err := taskTool.InitializeGlobalTaskStore(config.SessionSQLitePath); err != nil {
			return nil, fmt.Errorf("initialize task store: %w", err)
		}
		var err error
		goalBackend, err = coregoal.OpenSQLiteBackend(config.SessionSQLitePath)
		if err != nil {
			return nil, fmt.Errorf("initialize goal store: %w", err)
		}
		coregoal.ConfigureDefaultStoreBackend(goalBackend)
	}

	queryEngine := engine.NewEngine(
		apiClient, orchestrator, compactor, promptAssembler,
		permissionIntegrator, reg, sessionStore, queryConfig, memSvc, monitoringSys,
	)
	queryEngine.SetTitleAPIClient(titleAPIClient)
	queryEngine.SetTitleGenerator(titleGenerator)
	queryEngine.SetPromptFn(config.PromptFn)

	coretasks.NewDefaultManager(queryEngine, nil)

	agentToolInstance := agentTool.NewAgentTool(agentTool.DefaultAgentToolConfig())
	agentToolInstance.SetEngine(queryEngine)
	if config.EnableMemory || config.EnableHooks {
		agentRunner := coreagent.NewRunner(queryEngine)
		agentRunner.SetEnableMemory(config.EnableMemory)
		agentRunner.SetEnableHooks(config.EnableHooks)
		agentToolInstance.SetRunner(agentRunner)
	}
	if err := reg.Register(agentToolInstance); err != nil {
		return nil, fmt.Errorf("failed to register agent tool: %w", err)
	}

	wireSkillForkRunner(reg, queryEngine)

	// spawn_agent and resume_agent need the live engine instance — registered here, not in builtin.go.
	// nil tools → sub-agent inherits all tools from the engine registry at call time.
	agentRegistry := coreagent.NewAgentRegistry()
	spawnAgentTool := agentTool.NewSpawnAgentTool(queryEngine, nil, agentRegistry)
	if err := reg.Register(spawnAgentTool); err != nil {
		return nil, fmt.Errorf("failed to register spawn_agent tool: %w", err)
	}
	resumeAgentTool := agentTool.NewResumeAgentTool(queryEngine, nil, agentRegistry)
	if err := reg.Register(resumeAgentTool); err != nil {
		return nil, fmt.Errorf("failed to register resume_agent tool: %w", err)
	}

	client := &Client{
		queryEngine:   queryEngine,
		orchestrator:  orchestrator,
		registry:      reg,
		store:         store,
		config:        config,
		memoryInitErr: memInitErr,
		mcpResult:     mcpResult,
		ownedStore:    ownedStore,
		browser:       browserManager,
		artifacts:     artifactStore,
		reaper:        reaper,
		taskStorePath: config.SessionSQLitePath,
		goalBackend:   goalBackend,
	}

	// Register shell pre-tool hooks from ClientConfig.
	if len(config.PreToolHooks) > 0 {
		client.registerShellPreToolHooks(config.PreToolHooks, config.WorkingDir)
	}

	// Wire the session title callback into the engine.
	// The callback runs in a goroutine spawned by the engine after the first
	// completed turn. It updates the persisted title and then notifies the
	// host via config.OnSessionTitled (e.g. so the TUI can refresh the list).
	if !config.DisableTitleGeneration {
		queryEngine.SetOnSessionTitled(func(id types.SessionID, title string) {
			if store != nil {
				type titleUpdater interface {
					UpdateSessionTitle(types.SessionID, string) error
				}
				if tu, ok := store.(titleUpdater); ok {
					_ = tu.UpdateSessionTitle(id, title)
				}
			}
			if config.OnSessionTitled != nil {
				config.OnSessionTitled(SessionID(id), title)
			}
		})
	}

	log.Printf("[perf] sdk.NewClient registry_ms=%d mcp_ms=%d mcp_servers=%d store_ms=%d mem_ms=%d total_ms=%d",
		registryMs, mcpMs, len(config.MCPServers), storeMs, memMs, time.Since(newClientStart).Milliseconds())

	return client, nil
}

// wireSkillForkRunner injects the engine-dependent piece of the skill tool
// (running a `context: fork` skill as a sub-agent) after the fact: the tool
// itself is constructed inside builtin.go, before this engine exists, so -
// like agentToolInstance.SetEngine in NewClient - there is nothing to inject
// into until the registered instance is looked back up. Split out of
// NewClient to keep that function's own branch count down.
func wireSkillForkRunner(reg *registry.Registry, queryEngine *engine.Engine) {
	resolved, ok := reg.Get(skillTool.SkillToolName)
	if !ok {
		return
	}
	skill, ok := resolved.(*skillTool.SkillTool)
	if !ok {
		return
	}
	skill.SetForkRunner(agentTool.NewSkillForkRunner(queryEngine))
}

// buildEngineConfig converts ClientConfig into engine.Config.
func buildEngineConfig(config *ClientConfig) *engine.Config {
	maxTokens := config.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}
	maxTurns := config.MaxTurns
	if maxTurns <= 0 {
		// Matches engine.DefaultConfig()'s MaxTurns - without this, a host
		// that builds ClientConfig directly (bypassing engine.DefaultConfig)
		// got no session-turn limit at all instead of the intended default
		// (M3 in TODO.md).
		maxTurns = 100
	}
	qc := &engine.Config{
		MaxTurns:                 maxTurns,
		AutoCompact:              config.AutoCompact,
		PermissionMode:           config.PermissionMode,
		Model:                    config.Model,
		TitleModel:               config.TitleModel,
		CompactSummaryModel:      config.CompactSummaryModel,
		TrustProjectInstructions: config.TrustProjectInstructions,
		MaxTokens:                maxTokens,
		WorkingDirectory:         config.WorkingDir,
		SystemPromptTemplate:     config.SystemPromptTemplate,
		MCPServers:               config.MCPServers,
		EnableMemory:             config.EnableMemory,
		EnableMonitoring:         config.EnableMonitoring,
		MaxIterations:            config.MaxIterations,
		TurnTokenBudget:          config.TurnTokenBudget,
		BudgetContinuationLimit:  config.BudgetContinuationLimit,
		ContinuationNudgeLimit:   config.ContinuationNudgeLimit,
		TaskNudgeLimit:           config.TaskNudgeLimit,
		StopHooks:                config.StopHooks,
		MaxConsecutiveDenials:    config.MaxConsecutiveDenials,
		MainAgentTools:           config.MainAgentTools,
		MainAgentDeferredTools:   config.MainAgentDeferredTools,
	}
	if pc := config.PromptConfig; pc != nil {
		if pc.SystemPrompt != nil {
			qc.SystemPromptTemplate = *pc.SystemPrompt
		}
		if pc.AppendSystemPrompt != nil {
			qc.AppendSystemPrompt = *pc.AppendSystemPrompt
		}
		qc.PromptStage = pc.Stage
		qc.PromptStageOverrides = pc.StageOverrides
		qc.PromptToolHints = pc.ToolHints
	}
	if config.Companion != nil {
		if companionPrompt := companion.SystemPrompt(*config.Companion); strings.TrimSpace(companionPrompt) != "" {
			qc.AppendSystemPrompt = joinPromptParts(qc.AppendSystemPrompt, companionPrompt)
		}
	}
	return qc
}

func buildTitleAPIClient(config *ClientConfig, defaultAPIKey string, mainClient *providers.Client) *providers.Client {
	if config == nil || strings.TrimSpace(config.TitleModel.Model) == "" || config.TitleModel.Provider == "" {
		return nil
	}
	titleProvider := config.TitleModel.Provider
	if config.TitleProviderConfig == nil && titleProvider == config.Model.Provider {
		return nil
	}

	apiKey := defaultAPIKey
	if config.CredentialResolver != nil {
		if resolved, err := config.CredentialResolver.ResolveAPIKey(context.Background(), string(titleProvider)); err == nil && resolved != "" {
			apiKey = resolved
		} else if err != nil {
			log.Printf("[sdk] CredentialResolver failed for title provider %s: %v", titleProvider, err)
		}
	}
	if config.TitleProviderConfig != nil {
		pc := *config.TitleProviderConfig
		if pc.Provider == "" {
			pc.Provider = titleProvider
		}
		if pc.APIKey == "" {
			pc.APIKey = apiKey
		}
		return providers.NewClientWithConfig(pc.APIKey, &pc)
	}
	return providers.NewClient(apiKey, titleProvider)
}

func buildTitleGenerator(config *ClientConfig) (TitleGenerator, error) {
	if config == nil {
		return nil, nil
	}
	if config.TitleGenerator != nil {
		return config.TitleGenerator, nil
	}
	if config.LocalTitle == nil || !config.LocalTitle.Enabled {
		return nil, nil
	}
	generator, err := titlegen.NewLocalGenerator(titlegen.LocalConfig{
		Enabled:        config.LocalTitle.Enabled,
		Runtime:        config.LocalTitle.Runtime,
		ExecutablePath: config.LocalTitle.ExecutablePath,
		ModelPath:      config.LocalTitle.ModelPath,
		HFRepo:         config.LocalTitle.HFRepo,
		HFFile:         config.LocalTitle.HFFile,
		CacheDir:       config.LocalTitle.CacheDir,
		Args:           config.LocalTitle.Args,
		Timeout:        config.LocalTitle.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize local title generator: %w", err)
	}
	return generator, nil
}

func joinPromptParts(existing, extra string) string {
	existing = strings.TrimSpace(existing)
	extra = strings.TrimSpace(extra)
	if existing == "" {
		return extra
	}
	if extra == "" {
		return existing
	}
	return existing + "\n\n" + extra
}

// Ask performs a single-turn query (convenience method).
func (c *Client) Ask(ctx context.Context, prompt string, tools []Tool) (*AskResponse, error) {
	session, err := c.CreateSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	for _, t := range tools {
		if err := session.RegisterTool(t); err != nil {
			return nil, fmt.Errorf("failed to register tool %q: %w", t.Definition().Name, err)
		}
	}

	response, err := session.SubmitMessage(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to submit message: %w", err)
	}

	lastMessage, ok := lastAssistantMessage(response.Messages)
	if !ok {
		return nil, fmt.Errorf("no assistant message in response")
	}

	return &AskResponse{
		Content:     extractTextContent(lastMessage),
		Thinking:    extractThinkingFromMessages(assistantMessagesForLatestTurn(response.Messages)),
		ToolUses:    response.ToolUses,
		ToolResults: response.ToolResults,
		Usage:       response.Usage,
		IsComplete:  response.IsComplete,
	}, nil
}

// RunAgentTask runs task as agentType (a built-in agent type, e.g.
// AgentTypeGeneralPurpose) for up to maxTurns autonomous turns - 0 means a
// single turn, same as Ask - and returns its final text output. Used by
// RunWorkflow's default executor when a workflow.Node declares Agent/
// MaxTurns; exposed publicly so a custom Executor can reuse the same
// mechanism instead of reimplementing it.
func (c *Client) RunAgentTask(ctx context.Context, agentType, task string, maxTurns int) (string, error) {
	if c.queryEngine == nil {
		return "", fmt.Errorf("engine not available")
	}
	result, err := coreagent.RunAgent(&coreagent.RunConfig{
		AgentType:      agentType,
		Task:           task,
		Engine:         c.queryEngine,
		Context:        ctx,
		MaxTurns:       maxTurns,
		PermissionMode: types.PermissionModeBypass,
	})
	if err != nil {
		return "", err
	}
	if !result.Success {
		return "", fmt.Errorf("agent task failed: %s", result.Error)
	}
	return result.Output, nil
}

// CreateSession creates a new session for multi-turn conversations.
// The session is assigned a sequential human-readable title of the form
// "untitled_session_N" where N is 1 + the highest N already in use.
func (c *Client) CreateSession(ctx context.Context) (*Session, error) {
	title := c.nextUntitledTitle()
	now := time.Now()
	meta := &types.SessionMetadata{
		Title:         title,
		CreatedAt:     now,
		UpdatedAt:     now,
		SchemaVersion: types.SessionMetadataSchemaVersion,
	}
	querySession, err := c.queryEngine.NewSessionFromState(ctx, types.SessionID(""), meta, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	return newSDKSession(c, querySession), nil
}

// nextUntitledTitle returns the next "untitled_session_N" title using an
// atomic counter — O(1) and safe under concurrent CreateSession calls.
func (c *Client) nextUntitledTitle() string {
	return fmt.Sprintf("untitled_session_%d", c.untitledCounter.Add(1))
}

// LoadSession loads an existing session.
func (c *Client) LoadSession(ctx context.Context, sessionID SessionID) (*Session, error) {
	if c.store == nil {
		return nil, fmt.Errorf("session persistence not enabled")
	}
	metadata, messages, err := c.store.RestoreSessionState(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to restore session state: %w", err)
	}
	if metadata.Status == SessionStatusClosed {
		return nil, fmt.Errorf("session is closed")
	}
	querySession, err := c.queryEngine.NewSessionFromState(ctx, sessionID, metadata, messages)
	if err != nil {
		return nil, fmt.Errorf("failed to restore session: %w", err)
	}
	return newSDKSession(c, querySession), nil
}

// RegisterTool registers a tool globally.
func (c *Client) RegisterTool(tool Tool) error {
	if promptAware, ok := tool.(promptAwareTool); ok {
		promptAware.SetPromptFn(c.config.PromptFn)
	}
	return c.registry.Register(tool)
}

// RegisterTools registers multiple tools globally.
func (c *Client) RegisterTools(tools []Tool) error {
	for _, t := range tools {
		if err := c.RegisterTool(t); err != nil {
			return err
		}
	}
	return nil
}

// SandboxKind reports which sandbox backend this client's bash tool actually
// resolved to at construction (SandboxKindDocker only if it was requested via
// ClientConfig.SandboxKind AND Docker passed its health check at startup;
// SandboxKindLocal otherwise). Host applications use this instead of the
// package-level SandboxAvailable(), which only ever checks Landlock and so
// can't reflect a Docker sandbox this specific client is actually using.
func (c *Client) SandboxKind() SandboxKind {
	toolInstance, ok := c.registry.Get(bashTool.ToolName)
	if !ok {
		return SandboxKindLocal
	}
	confinable, ok := toolInstance.(interface {
		SandboxKind() sandbox.EnvironmentKind
	})
	if !ok {
		return SandboxKindLocal
	}
	return confinable.SandboxKind()
}

// ListSessions lists all sessions.
func (c *Client) ListSessions() ([]*SessionInfo, error) {
	if c.store == nil {
		return nil, fmt.Errorf("session persistence not enabled")
	}
	return c.store.GetAllSessionsInfo()
}

// SearchTranscriptsByContent lists sessions whose stored transcript contains needle.
func (c *Client) SearchTranscriptsByContent(needle string, limit int) ([]SessionID, error) {
	if c.store == nil {
		return nil, fmt.Errorf("session persistence not enabled")
	}
	return c.store.SearchTranscriptsByContent(needle, limit)
}

// DeleteSession deletes a session and all associated artifacts from storage.
func (c *Client) DeleteSession(sessionID SessionID) error {
	if c.store == nil {
		return fmt.Errorf("session persistence not enabled")
	}
	if err := c.store.DeleteSession(sessionID); err != nil {
		return err
	}
	// The session's directory (plans, pastes, screenshots, tool output) and its permission grants are
	// not artifacts in the store, so they are removed here. Best effort, like the artifact cleanup.
	_ = runtimepath.RemoveSessionData("", string(sessionID))
	if c.artifacts != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		deleteSessionArtifacts(ctx, c.artifacts, string(sessionID))
	}
	return nil
}

// deleteSessionArtifacts removes all artifacts stored under workspaces/{id}/, and under the sessions/{id}/
// prefix older versions used. This handles S3 storage where os.RemoveAll is not available.
// Errors are intentionally ignored - artifact cleanup is best-effort.
func deleteSessionArtifacts(ctx context.Context, store ArtifactStore, sessionID string) {
	for _, prefix := range []string{storage.SessionKeyPrefix, storage.LegacySessionKeyPrefix} {
		refs, err := store.List(ctx, ArtifactListOptions{Prefix: prefix + "/" + sessionID})
		if err != nil {
			continue
		}
		for _, ref := range refs {
			_ = store.Delete(ctx, ref.Key)
		}
	}
}

// Close releases SDK-owned resources. Safe to call multiple times.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		var errs []error
		if c.mcpResult != nil {
			if err := c.mcpResult.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if c.ownedStore && c.store != nil {
			if err := c.store.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if c.taskStorePath != "" {
			if err := taskTool.CloseGlobalTaskStore(c.taskStorePath); err != nil {
				errs = append(errs, err)
			}
		}
		if c.goalBackend != nil {
			if err := c.goalBackend.Close(); err != nil {
				errs = append(errs, err)
			}
			coregoal.ConfigureDefaultStoreBackend(nil)
		}
		if c.reaper != nil {
			c.reaper.Stop()
		}
		if c.browser != nil {
			if err := c.browser.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}
