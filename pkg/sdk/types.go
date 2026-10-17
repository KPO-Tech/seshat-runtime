package sdk

import (
	"github.com/KPO-Tech/seshat-runtime/internal/engine"
	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/hooks"
	longterm "github.com/KPO-Tech/seshat-runtime/internal/memory/longterm"
	"github.com/KPO-Tech/seshat-runtime/internal/modes"
	"github.com/KPO-Tech/seshat-runtime/internal/monitoring"
	"github.com/KPO-Tech/seshat-runtime/internal/rag"
	"github.com/KPO-Tech/seshat-runtime/internal/runtime/state"
	"github.com/KPO-Tech/seshat-runtime/internal/storage"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/builtin"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/system/mcp"
	websearchtool "github.com/KPO-Tech/seshat-runtime/internal/tools/web/search"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Public aliases keep the SDK consumable without forcing callers onto internal packages.
type (
	// Core types
	APIProvider       = types.APIProvider
	CallResult        = contract.CallResult
	ContentBlock      = types.ContentBlock
	ImageContent      = types.ImageContent
	EntryType         = types.EntryType
	ExecutionMode     = modes.ExecutionMode
	Message           = types.Message
	MessageID         = types.MessageID
	ModelIdentifier   = types.ModelIdentifier
	PermissionContext = types.PermissionContext
	PermissionMode    = types.PermissionMode
	PromptFn          = types.PromptFn
	PromptOption      = types.PromptOption
	PromptRequest     = types.PromptRequest
	PromptResponse    = types.PromptResponse
	SessionID         = types.SessionID
	Checkpoint        = state.Checkpoint
	SessionInfo       = state.SessionInfo
	SessionMetadata   = types.SessionMetadata
	SessionStatus     = types.SessionStatus
	Surface           = registry.Surface
	TextContent       = types.TextContent
	TokenUsage        = types.TokenUsage
	Tool              = contract.Tool
	ToolDefinition    = registry.ToolDefinition
	ToolHook          = execution.ToolHook
	ToolHookInput     = execution.ToolHookInput
	ToolHookStage     = execution.ToolHookStage
	// ToolHookResult and ToolHookStop are what ToolHook.Execute returns. They
	// were missing from this alias block (H5 in TODO.md), so a host in
	// another module could name ToolHook itself but not the type its own
	// Execute method must return, making AddToolHook unusable from outside
	// this module.
	ToolHookResult            = execution.ToolHookResult
	ToolHookStop              = execution.ToolHookStop
	ToolProgress              = types.ToolProgress
	ToolProgressStage         = types.ToolProgressStage
	ResponseChunk             = types.APIResponseChunk
	ResponseChunkType         = types.APIChunkType
	ThinkingContent           = types.ThinkingContent
	TranscriptEntry           = types.TranscriptEntry
	TurnID                    = types.TurnID
	ToolResultContent         = types.ToolResultContent
	ToolUseContent            = types.ToolUseContent
	Role                      = types.Role
	RuntimeEvent              = types.RuntimeEvent
	RuntimeEventType          = types.RuntimeEventType
	PlanRuntimeEvent          = types.PlanRuntimeEvent
	TaskRuntimeEvent          = types.TaskRuntimeEvent
	StageRuntimeEvent         = types.StageRuntimeEvent
	ProviderRetryRuntimeEvent = types.ProviderRetryRuntimeEvent
	CompactionRuntimeEvent    = types.CompactionRuntimeEvent
	ContextUsageRuntimeEvent  = types.ContextUsageRuntimeEvent

	// Lifecycle hooks
	HookEvent        = types.HookEvent
	HookProgress     = types.HookProgress
	HookResult       = types.HookResult
	HookHandler      = hooks.HookHandler
	HookState        = hooks.HookState
	HookRegistration = hooks.HookRegistration
	HookRegistry     = hooks.Registry

	// Stop hooks — post-turn policy hooks that can request one more loop cycle.
	StopHook       = engine.StopHook
	StopHookInput  = engine.StopHookInput
	StopHookResult = engine.StopHookResult

	// SDK-owned eventing, monitoring, and MCP integration types.
	EventQueue             = execution.EventQueue
	EventQueueStats        = execution.EventQueueStats
	MCPIntegrationOptions  = mcp.IntegrationOptions
	MCPIntegrationResult   = mcp.IntegrationResult
	MCPServerConfig        = mcp.ServerConfig
	MCPServerResult        = mcp.ServerResult
	MCPTransportType       = mcp.TransportType
	MonitoringSystem       = monitoring.System
	RuntimeEventQueue      = execution.RuntimeEventQueue
	RuntimeEventQueueStats = execution.RuntimeEventQueueStats

	// RAGService — re-exported so SDK callers can inject it without importing internal/rag.
	RAGService = rag.Service

	// PlanStore — re-exported so SDK callers can inject it without importing internal/tools/builtin.
	PlanStore = builtin.PlanStore

	// LongTermMemory — re-exported so SDK callers can inject it without importing internal/memory/longterm.
	LongTermMemory = longterm.Store

	// WebSearchRunnerFn — re-exported so the query layer can wire the DB-backed
	// provider chain into the web_search tool without importing the tool package directly.
	WebSearchRunnerFn = websearchtool.RunnerFn

	// Storage data types — re-exported so SDK callers don't need to import internal/storage.
	ArtifactGCOptions      = storage.GCOptions
	ArtifactGCReport       = storage.GCReport
	ArtifactListOptions    = storage.ListOptions
	ArtifactMetadata       = storage.ArtifactMetadata
	ArtifactNamespace      = storage.ArtifactNamespace
	ArtifactPutRequest     = storage.ArtifactPutRequest
	ArtifactRef            = storage.ArtifactRef
	ArtifactRetentionClass = storage.ArtifactRetentionClass

	// Permissions
	PermissionBehavior  = types.PermissionBehavior
	PermissionResult    = types.PermissionResult
	DenialLimitConfig   = types.DenialLimitConfig
	DenialTrackingState = types.DenialTrackingState
)

const (
	RoleUser      = types.RoleUser
	RoleAssistant = types.RoleAssistant
	RoleSystem    = types.RoleSystem

	APIProviderAnthropic = types.APIProviderAnthropic

	APIProviderBedrock    = types.APIProviderBedrock
	APIProviderCodex      = types.APIProviderCodex
	APIProviderDeepSeek   = types.APIProviderDeepSeek
	APIProviderFoundry    = types.APIProviderFoundry
	APIProviderGemini     = types.APIProviderGemini
	APIProviderMistral    = types.APIProviderMistral
	APIProviderMiniMax    = types.APIProviderMiniMax
	APIProviderOllama     = types.APIProviderOllama
	APIProviderOpenCode   = types.APIProviderOpenCode
	APIProviderOpenAI     = types.APIProviderOpenAI
	APIProviderOpenRouter = types.APIProviderOpenRouter
	APIProviderVertex     = types.APIProviderVertex
	APIProviderWorkersAI  = types.APIProviderWorkersAI
	APIProviderZAi        = types.APIProviderZAi
	APIProviderKimi       = types.APIProviderKimi

	ExecutionModeExecute = modes.ExecutionModeExecute
	ExecutionModePlan    = modes.ExecutionModePlan

	PermissionModeAuto        = types.PermissionModeAuto
	PermissionModeBypass      = types.PermissionModeBypass
	PermissionModeOnRequest   = types.PermissionModeOnRequest
	PermissionModeNever       = types.PermissionModeNever
	PermissionModeAcceptEdits = types.PermissionModeAcceptEdits
	PermissionModeGranular    = types.PermissionModeGranular

	SessionStatusActive = types.SessionStatusActive
	SessionStatusClosed = types.SessionStatusClosed

	EntryTypeMessage = types.EntryTypeMessage
	EntryTypeTurn    = types.EntryTypeTurn
	EntryTypeCompact = types.EntryTypeCompact
	EntryTypeSystem  = types.EntryTypeSystem
	EntryTypeControl = types.EntryTypeControl

	ResponseChunkTypeContentBlockStart = types.APIChunkTypeContentBlockStart
	ResponseChunkTypeContentBlockDelta = types.APIChunkTypeContentBlockDelta
	ResponseChunkTypeContentBlockStop  = types.APIChunkTypeContentBlockStop
	ResponseChunkTypeMessageDelta      = types.APIChunkTypeMessageDelta
	ResponseChunkTypeMessageStop       = types.APIChunkTypeMessageStop
	ResponseChunkTypeError             = types.APIChunkTypeError

	ToolHookStagePre  = execution.ToolHookStagePre
	ToolHookStagePost = execution.ToolHookStagePost

	ToolProgressStagePending   = types.ToolProgressStagePending
	ToolProgressStageRunning   = types.ToolProgressStageRunning
	ToolProgressStageCompleted = types.ToolProgressStageCompleted
	ToolProgressStageFailed    = types.ToolProgressStageFailed

	NamespaceDocuments          = storage.NamespaceDocuments
	NamespaceWebArtifacts       = storage.NamespaceWebArtifacts
	NamespaceBrowserScreenshots = storage.NamespaceBrowserScreenshots
	NamespaceBrowserDownloads   = storage.NamespaceBrowserDownloads
	NamespaceRAGDocuments       = storage.NamespaceRAGDocuments

	RetentionDurable   = storage.RetentionDurable
	RetentionTemporary = storage.RetentionTemporary
	RetentionSession   = storage.RetentionSession

	RuntimeEventTypeTurnStarted          = types.RuntimeEventTypeTurnStarted
	RuntimeEventTypeTurnCompleted        = types.RuntimeEventTypeTurnCompleted
	RuntimeEventTypeTurnFailed           = types.RuntimeEventTypeTurnFailed
	RuntimeEventTypeResponseChunk        = types.RuntimeEventTypeResponseChunk
	RuntimeEventTypeToolProgress         = types.RuntimeEventTypeToolProgress
	RuntimeEventTypePlanSubmitted        = types.RuntimeEventTypePlanSubmitted
	RuntimeEventTypeExecutionModeChanged = types.RuntimeEventTypeExecutionModeChanged
	RuntimeEventTypeTaskChanged          = types.RuntimeEventTypeTaskChanged
	RuntimeEventTypeBrowserSession       = types.RuntimeEventTypeBrowserSession
	RuntimeEventTypeBrowserAction        = types.RuntimeEventTypeBrowserAction
	RuntimeEventTypeBrowserPage          = types.RuntimeEventTypeBrowserPage
	RuntimeEventTypeBrowserSnapshot      = types.RuntimeEventTypeBrowserSnapshot
	RuntimeEventTypeBrowserScreenshot    = types.RuntimeEventTypeBrowserScreenshot
	RuntimeEventTypeStage                = types.RuntimeEventTypeStage
	RuntimeEventTypeProviderRetry        = types.RuntimeEventTypeProviderRetry
	RuntimeEventTypeCompaction           = types.RuntimeEventTypeCompaction
	RuntimeEventTypeContextUsage         = types.RuntimeEventTypeContextUsage
	RuntimeEventTypeCompacting           = types.RuntimeEventTypeCompacting

	// HookEvent constants — lifecycle events emitted throughout the engine.
	HookEventSessionStart       = types.HookEventSessionStart
	HookEventSessionEnd         = types.HookEventSessionEnd
	HookEventQueryStart         = types.HookEventQueryStart
	HookEventQueryComplete      = types.HookEventQueryComplete
	HookEventIterationStart     = types.HookEventIterationStart
	HookEventIterationStop      = types.HookEventIterationStop
	HookEventIterationContinue  = types.HookEventIterationContinue
	HookEventIterationComplete  = types.HookEventIterationComplete
	HookEventToolUsesStart      = types.HookEventToolUsesStart
	HookEventToolUsesComplete   = types.HookEventToolUsesComplete
	HookEventTurnStart          = types.HookEventTurnStart
	HookEventTurnEnd            = types.HookEventTurnEnd
	HookEventTurnStop           = types.HookEventTurnStop
	HookEventStopFailure        = types.HookEventStopFailure
	HookEventPreToolUse         = types.HookEventPreToolUse
	HookEventPostToolUse        = types.HookEventPostToolUse
	HookEventPostToolUseFail    = types.HookEventPostToolUseFail
	HookEventPreCompact         = types.HookEventPreCompact
	HookEventPostCompact        = types.HookEventPostCompact
	HookEventPreAPICall         = types.HookEventPreAPICall
	HookEventPostAPICall        = types.HookEventPostAPICall
	HookEventOnError            = types.HookEventOnError
	HookEventNotification       = types.HookEventNotification
	HookEventUserPromptSubmit   = types.HookEventUserPromptSubmit
	HookEventSubagentStart      = types.HookEventSubagentStart
	HookEventSubagentStop       = types.HookEventSubagentStop
	HookEventPermissionRequest  = types.HookEventPermissionRequest
	HookEventPermissionDenied   = types.HookEventPermissionDenied
	HookEventSetup              = types.HookEventSetup
	HookEventConfigChange       = types.HookEventConfigChange
	HookEventTeammateIdle       = types.HookEventTeammateIdle
	HookEventTaskCreated        = types.HookEventTaskCreated
	HookEventTaskCompleted      = types.HookEventTaskCompleted
	HookEventElicitation        = types.HookEventElicitation
	HookEventElicitationResult  = types.HookEventElicitationResult
	HookEventWorktreeCreate     = types.HookEventWorktreeCreate
	HookEventWorktreeRemove     = types.HookEventWorktreeRemove
	HookEventInstructionsLoaded = types.HookEventInstructionsLoaded
	HookEventCwdChanged         = types.HookEventCwdChanged
	HookEventFileChanged        = types.HookEventFileChanged

	// HookState constants
	HookStateActive   = hooks.HookStateActive
	HookStatePaused   = hooks.HookStatePaused
	HookStateInactive = hooks.HookStateInactive
	HookStateDead     = hooks.HookStateDead

	// PermissionBehavior constants
	PermissionBehaviorAllow       = types.PermissionBehaviorAllow
	PermissionBehaviorDeny        = types.PermissionBehaviorDeny
	PermissionBehaviorAsk         = types.PermissionBehaviorAsk
	PermissionBehaviorPassthrough = types.PermissionBehaviorPassthrough

	// MCPTransport constants
	MCPTransportStdio     MCPTransportType = mcp.TransportTypeStdio
	MCPTransportHTTP      MCPTransportType = mcp.TransportTypeHTTP
	MCPTransportSSE       MCPTransportType = mcp.TransportTypeSSE
	MCPTransportWebSocket MCPTransportType = mcp.TransportTypeWebSocket
)
