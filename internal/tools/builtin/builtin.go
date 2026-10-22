package builtin

import (
	"context"
	"fmt"

	agentsTool "github.com/KPO-Tech/seshat-runtime/internal/tools/agents"
	automationTool "github.com/KPO-Tech/seshat-runtime/internal/tools/automation"
	bashTool "github.com/KPO-Tech/seshat-runtime/internal/tools/bash"
	documentreaderTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/documentreader"
	editTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/edit"
	fsTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/fs"
	globTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/glob"
	grepTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/grep"
	patchTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/patch"
	fileReadTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/read"
	searchsessionTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/searchsession"
	writeTool "github.com/KPO-Tech/seshat-runtime/internal/tools/files/write"
	gitTool "github.com/KPO-Tech/seshat-runtime/internal/tools/git"
	calculatorTool "github.com/KPO-Tech/seshat-runtime/internal/tools/math/calculator"
	financialTool "github.com/KPO-Tech/seshat-runtime/internal/tools/math/financial"
	statisticsTool "github.com/KPO-Tech/seshat-runtime/internal/tools/math/statistics"
	unitsTool "github.com/KPO-Tech/seshat-runtime/internal/tools/math/units"
	multimediaTool "github.com/KPO-Tech/seshat-runtime/internal/tools/multimedia"
	notebookTool "github.com/KPO-Tech/seshat-runtime/internal/tools/notebook"
	discordTool "github.com/KPO-Tech/seshat-runtime/internal/tools/notifications/discord"
	emailTool "github.com/KPO-Tech/seshat-runtime/internal/tools/notifications/email"
	slackTool "github.com/KPO-Tech/seshat-runtime/internal/tools/notifications/slack"
	telegramTool "github.com/KPO-Tech/seshat-runtime/internal/tools/notifications/telegram"
	whatsappTool "github.com/KPO-Tech/seshat-runtime/internal/tools/notifications/whatsapp"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	researchTools "github.com/KPO-Tech/seshat-runtime/internal/tools/research"
	devtoTool "github.com/KPO-Tech/seshat-runtime/internal/tools/social/devto"
	hnTool "github.com/KPO-Tech/seshat-runtime/internal/tools/social/hackernews"
	twitterTool "github.com/KPO-Tech/seshat-runtime/internal/tools/social/twitter"
	askUserQuestionTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/ask_user"
	configTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/config"
	contextUsageTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/context_usage"
	fimtool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/fim"
	goalTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/goal"
	lspTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/lsp"
	memoryTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/memory"
	ragTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/rag"
	repomapTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/repomap"
	requestPermissionsTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/request_permissions"
	toolSearchTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/tool_search"
	workflowTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/workflow"
	worktreeTool "github.com/KPO-Tech/seshat-runtime/internal/tools/special/worktree"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/system/mcp"
	planTool "github.com/KPO-Tech/seshat-runtime/internal/tools/system/plan"
	seshatSkillTool "github.com/KPO-Tech/seshat-runtime/internal/tools/system/seshatskill"
	skillTool "github.com/KPO-Tech/seshat-runtime/internal/tools/system/skills"
	taskTool "github.com/KPO-Tech/seshat-runtime/internal/tools/task"
	browsercore "github.com/KPO-Tech/seshat-runtime/internal/tools/web/browser"
	webfetchTool "github.com/KPO-Tech/seshat-runtime/internal/tools/web/fetch"
	webSearchTool "github.com/KPO-Tech/seshat-runtime/internal/tools/web/search"
)

// RegisterBuiltinTools registers all built-in tools in the registry
func RegisterBuiltinTools(reg *tool.Registry) error {
	return RegisterBuiltinToolsWithConfig(reg, nil)
}

// RegisterBuiltinToolsWithConfig registers all built-in tools in the registry for a host runtime.
func RegisterBuiltinToolsWithConfig(reg *tool.Registry, config *Config) error {
	config = normalizeConfig(config)

	// Connect Skills to MCP manager
	skillTool.SetMCPManager(config.MCPManager)

	askUserConfig := &askUserQuestionTool.Config{
		Timeout:  askUserQuestionTool.DefaultConfig().Timeout,
		PromptFn: config.PromptFn,
	}
	if config.EnablePromptReaderFallback {
		askUserConfig.InputReader = config.InputReader
		if askUserConfig.InputReader == nil {
			askUserConfig.InputReader = askUserQuestionTool.DefaultConfig().InputReader
		}
	}

	skillRuntimeTool := skillTool.NewSkillTool(nil)
	skillRuntimeTool.SetCwd(config.WorkingDir)
	skillRuntimeTool.SetUserID(config.UserID)

	webFetchConfig := webfetchTool.DefaultToolConfig()
	webFetchConfig.BrowserManager = config.BrowserManager
	webFetchConfig.ArtifactStore = config.ArtifactStore

	fileReadConfig := fileReadTool.DefaultToolConfig()
	fileReadConfig.DocumentReaderURL = config.DocumentReaderURL
	fileReadConfig.DocumentConverter = config.DocumentConverter

	documentReaderConfig := documentreaderTool.Config{
		DocumentReaderURL:    config.DocumentReaderURL,
		DocumentConverter:    config.DocumentConverter,
		DocumentPageRenderer: config.DocumentPageRenderer,
	}

	bashConfig := bashTool.DefaultToolConfig()
	bashConfig.WorkingDirectory = config.WorkingDir
	bashConfig.RequireSandbox = config.RequireSandbox
	bashConfig.SandboxKind = config.SandboxKind
	bashConfig.SandboxDocker = config.SandboxDocker

	tools := []tool.Tool{
		bashTool.NewTool(bashConfig),
		bashTool.NewWriteStdinTool(),
		bashTool.NewJobOutputTool(),
		bashTool.NewJobKillTool(),
		fileReadTool.NewTool(fileReadConfig),
		documentreaderTool.NewReadURLTool(documentReaderConfig),
		documentreaderTool.NewConvertTool(documentReaderConfig, config.WorkingDir),
		documentreaderTool.NewRenderPageTool(documentReaderConfig, config.WorkingDir),
		globTool.NewGlobTool(config.WorkingDir),
		grepTool.NewGrepTool(config.WorkingDir),
		searchsessionTool.NewTool(config.WorkingDir),
		writeTool.NewWriteTool(config.WorkingDir),
		editTool.NewEditTool(config.WorkingDir),
		configTool.NewTool(config.WorkingDir),
		repomapTool.NewTool(config.WorkingDir),
		workflowTool.NewTool(),
		patchTool.NewApplyPatchTool(config.WorkingDir),
		fsTool.NewCreateDirectoryTool(config.WorkingDir),
		fsTool.NewGetMetadataTool(config.WorkingDir),
		fsTool.NewListDirectoryTool(config.WorkingDir),
		fsTool.NewRemoveTool(config.WorkingDir),
		notebookTool.NewCreateTool(),
		notebookTool.NewReadTool(),
		notebookTool.NewWriteTool(),
		notebookTool.NewEditTool(),
		notebookTool.NewExecuteTool(),
		notebookTool.NewRunTool(),
		notebookTool.NewKernelTool(),
		askUserQuestionTool.NewTool(askUserConfig),
		webfetchTool.NewTool(webFetchConfig),
		newWebSearchTool(config.WebSearchKeys),
		browsercore.NewOpenTool(config.BrowserManager),
		browsercore.NewNavigateTool(config.BrowserManager),
		browsercore.NewSnapshotTool(config.BrowserManager),
		browsercore.NewExtractTool(config.BrowserManager),
		browsercore.NewListPagesTool(config.BrowserManager),
		browsercore.NewNetworkListTool(config.BrowserManager),
		browsercore.NewDownloadListTool(config.BrowserManager),
		browsercore.NewSearchContentTool(config.BrowserManager),
		browsercore.NewGetNetworkPolicyTool(config.BrowserManager),
		browsercore.NewSetNetworkPolicyTool(config.BrowserManager),
		browsercore.NewSelectPageTool(config.BrowserManager),
		browsercore.NewClosePageTool(config.BrowserManager),
		browsercore.NewClickTool(config.BrowserManager),
		browsercore.NewTypeTool(config.BrowserManager),
		browsercore.NewPressTool(config.BrowserManager),
		browsercore.NewScrollTool(config.BrowserManager),
		browsercore.NewWaitTool(config.BrowserManager),
		browsercore.NewScreenshotTool(config.BrowserManager),
		lspTool.NewLspTool(config.WorkingDir),
		bashTool.NewMonitorTool(config.WorkingDir),
		taskTool.NewTaskStopTool(),
		taskTool.NewTaskListTool(),
		taskTool.NewTaskGetTool(),
		taskTool.NewTaskCreateTool(),
		taskTool.NewTaskUpdateTool(),
		planTool.NewEnterPlanModeTool(planTool.DefaultEnterPlanModeConfig("")),
		planTool.NewExitPlanModeTool(planTool.DefaultExitPlanModeConfig("")),
		planTool.NewSubmitPlanTool(buildSubmitPlanConfig(config)),
		toolSearchTool.NewToolSearchTool(reg),
		mcp.NewMCPTool(config.MCPManager),
		skillRuntimeTool,
		seshatSkillTool.NewListTool(),
		seshatSkillTool.NewReadTool(),
		seshatSkillTool.NewValidateTool(),
		worktreeTool.NewEnterWorktreeTool(worktreeTool.DefaultEnterWorktreeConfig()),
		worktreeTool.NewExitWorktreeTool(worktreeTool.DefaultExitWorktreeConfig()),
		ragTool.NewSearchTool(config.RAGService),
		ragTool.NewIngestTool(config.RAGService),
		ragTool.NewDeleteTool(config.RAGService),
		multimediaTool.NewImageGenTool(config.ImageGenerator),
		multimediaTool.NewTTSTool(config.TTSGenerator),
		multimediaTool.NewPresentTool(),
		multimediaTool.NewSTTTool(config.STTTranscriber),
		fimtool.New(config.FIMCompleter),
		requestPermissionsTool.NewTool(),
		memoryTool.NewCreateEntitiesTool(config.LongTermMemory),
		memoryTool.NewAddObservationsTool(config.LongTermMemory),
		memoryTool.NewSearchNodesTool(config.LongTermMemory),
		memoryTool.NewOpenNodesTool(config.LongTermMemory),
		// Goal system (Phase 6) — store is a process-level singleton, no config needed.
		goalTool.NewCreateGoalTool(),
		goalTool.NewGetGoalTool(),
		goalTool.NewUpdateGoalTool(),
		// Context window (the loop records the size of each request; the tool only reads it).
		contextUsageTool.NewTool(),
		// Agent control plane (Phase 5) — wait/list/send/close use the default async manager.
		// spawn_agent is registered in sdk/client.go (needs the live engine instance).
		agentsTool.NewWaitAgentTool(),
		agentsTool.NewListAgentsTool(),
		agentsTool.NewSendAgentMessageTool(),
		agentsTool.NewCloseAgentTool(),
		// agentTool (synchronous): registered separately in sdk/client.go after engine creation.

		// VCS — git tools (stubs, IsEnabled=false until implemented via os/exec)
		gitTool.NewStatusTool(),
		gitTool.NewLogTool(),
		gitTool.NewDiffTool(),
		gitTool.NewCommitTool(),
		gitTool.NewBranchTool(),

		// Notifications — messaging platforms (stubs, IsEnabled=false until implemented)
		slackTool.NewSendTool(),
		discordTool.NewSendTool(),
		telegramTool.NewSendTool(),
		emailTool.NewSendTool(),
		whatsappTool.NewSendTool(),

		// Social / community tools (fully implemented, no auth required)
		hnTool.NewSearchTool(),
		hnTool.NewStoriesTool(),
		hnTool.NewItemTool(),
		devtoTool.NewFeedTool(),
		devtoTool.NewArticleTool(),
		devtoTool.NewPublishTool(),

		// Twitter/X — posts to a public account; also a stub (IsEnabled=false
		// until a real API v2 client replaces the ErrNotImplemented bodies, see
		// the package doc comment). Registered like the stubs above rather than
		// left out entirely, so it shows up in introspection the same way.
		twitterTool.NewSearchTool(),
		twitterTool.NewTweetTool(),
	}
	// Research tools (app store reviews, Reddit, YouTube, Google Places, Trustpilot): a tool whose keys
	// are missing reports itself disabled, so the model is never offered what it cannot use.
	tools = append(tools, researchTools.Tools(researchTools.NewKeys(config.ResearchKeys))...)

	for _, builtinTool := range tools {
		if builtinTool == nil {
			continue // Skip tools that couldn't be created (e.g., missing required config)
		}
		if err := reg.Register(builtinTool); err != nil {
			return err
		}
	}

	// Math tools use factory functions that return (Tool, error).
	mathFactories := []func() (tool.Tool, error){
		calculatorTool.New,
		unitsTool.New,
		statisticsTool.New,
		financialTool.New,
	}
	for _, factory := range mathFactories {
		t, err := factory()
		if err != nil {
			return fmt.Errorf("failed to build math tool: %w", err)
		}
		if err := reg.Register(t); err != nil {
			return err
		}
	}

	// Automation tools — talk to seshat-automation daemon via HTTP.
	automationCfg := automationTool.Config{
		ServiceURL: config.AutomationServiceURL,
		APIKey:     config.AutomationAPIKey,
	}
	automationFactories := []func(automationTool.Config) (tool.Tool, error){
		automationTool.NewScheduleJobTool,
		automationTool.NewListJobsTool,
		automationTool.NewUpdateJobTool,
		automationTool.NewDeleteJobTool,
		automationTool.NewPauseJobTool,
		automationTool.NewResumeJobTool,
		automationTool.NewRunJobNowTool,
	}
	for _, factory := range automationFactories {
		t, err := factory(automationCfg)
		if err != nil {
			return fmt.Errorf("failed to build automation tool: %w", err)
		}
		if err := reg.Register(t); err != nil {
			return err
		}
	}

	return nil
}

// NewBuiltinRegistry creates a new registry with all built-in tools registered
func NewBuiltinRegistry() (*tool.Registry, error) {
	return NewBuiltinRegistryWithConfig(nil)
}

// NewBuiltinRegistryWithConfig creates a new registry with all built-in tools registered for a host runtime.
func NewBuiltinRegistryWithConfig(config *Config) (*tool.Registry, error) {
	config = normalizeConfig(config)
	regConfig := tool.DefaultRegistryConfig()
	regConfig.EnableToolSearch = config.EnableToolSearch
	reg := tool.NewRegistryWithConfig(regConfig)
	if err := RegisterBuiltinToolsWithConfig(reg, config); err != nil {
		return nil, err
	}
	return reg, nil
}

// newWebSearchTool creates a web_search tool and, when explicit provider keys
// are provided, installs a per-execution runner so searches are isolated to
// the owner's configured keys instead of the process-wide environment.
func newWebSearchTool(keys map[string]string) *webSearchTool.Tool {
	t := webSearchTool.NewTool()
	if len(keys) > 0 {
		t.SetRunner(webSearchTool.NewRunnerFromKeys(keys))
	}
	return t
}

func buildSubmitPlanConfig(config *Config) *planTool.SubmitPlanConfig {
	if config == nil || config.PlanStore == nil {
		return nil
	}
	return &planTool.SubmitPlanConfig{
		UserID: config.UserID,
		PersistFn: func(ctx context.Context, planID, sessionID, userID, slug, filename, content string) (string, int, error) {
			return config.PlanStore.CreateOrUpdate(ctx, planID, sessionID, userID, slug, filename, content)
		},
	}
}
