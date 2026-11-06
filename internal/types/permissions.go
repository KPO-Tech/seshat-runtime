package types

import "context"

// PermissionMode represents the mode of APPROVAL checking.
// This determines WHO approves actions (user, classifier, etc.)
type PermissionMode string

const (
	// PermissionModeOnRequest asks the user for permission on non-automated actions.
	// This is the default/standard mode - the model decides when to ask for approval.
	PermissionModeOnRequest PermissionMode = "onRequest"

	// PermissionModeAuto automatically approves actions via classifier.
	PermissionModeAuto PermissionMode = "auto"

	// PermissionModeAcceptEdits automatically allows safe file operations in the working directory.
	// Aligned with OpenClaude's acceptEdits mode (permissions.ts:16-22).
	PermissionModeAcceptEdits PermissionMode = "acceptEdits"

	// PermissionModeBypass bypasses all permission checks.
	PermissionModeBypass PermissionMode = "bypass"

	// PermissionModeNever never asks the user to approve commands.
	// Failures are immediately returned to the model, never escalated to user.
	// Used for headless/background agents that cannot display UI prompts.
	PermissionModeNever PermissionMode = "never"

	// PermissionModeGranular provides fine-grained controls for individual approval flows.
	PermissionModeGranular PermissionMode = "granular"
)

// ExecutionOrigin indicates whether a run is initiated by an interactive user flow
// or a future background automation.
type ExecutionOrigin string

const (
	ExecutionOriginInteractive ExecutionOrigin = "interactive"
	ExecutionOriginAutomation  ExecutionOrigin = "automation"
	ExecutionOriginSkillAgent  ExecutionOrigin = "skill_agent"
)

func NormalizePermissionMode(raw string) (PermissionMode, bool) {
	mode := PermissionMode(raw)
	switch mode {
	case PermissionModeOnRequest,
		PermissionModeAuto,
		PermissionModeAcceptEdits,
		PermissionModeBypass,
		PermissionModeNever,
		PermissionModeGranular:
		return mode, true
	default:
		return "", false
	}
}

// PermissionModeStrictness orders the permission modes from the most
// restrictive (0) to the most permissive. It answers one question: "may a
// session in mode X hand work to a session in mode Y without widening what can
// run unapproved?". The order is, strictest first:
//
//		never < granular < onRequest < acceptEdits < auto < bypass
//
//	  - never fails closed: nothing that needs approval ever runs and nothing is
//	    escalated to a person. It is the strictest.
//	  - granular can only narrow the approval flows (each category of prompt may
//	    be auto-denied) and never approves anything by itself, so it sits below
//	    onRequest. It is kept strictly below onRequest on purpose: a session
//	    whose prompts are partly denied must never be clamped up to onRequest,
//	    which would give those prompts back.
//	  - onRequest asks a person for everything that is not already allowed.
//	  - acceptEdits additionally auto-allows safe edits in the working directory.
//	  - auto lets the classifier approve actions, a wider net than acceptEdits.
//	  - bypass skips the approval checks (bypass-immune denials still hold).
//
// ok is false for a value that is not one of the six modes (including the
// empty string and the legacy "plan"): callers must decide what an unknown
// mode means, it is never silently given a rank.
func PermissionModeStrictness(mode PermissionMode) (rank int, ok bool) {
	switch mode {
	case PermissionModeNever:
		return 0, true
	case PermissionModeGranular:
		return 1, true
	case PermissionModeOnRequest:
		return 2, true
	case PermissionModeAcceptEdits:
		return 3, true
	case PermissionModeAuto:
		return 4, true
	case PermissionModeBypass:
		return 5, true
	default:
		return 0, false
	}
}

// StricterPermissionMode returns whichever of a and b is the more restrictive
// (see PermissionModeStrictness). An unknown mode is never trusted: when one
// side is unknown the result is the other side if that one is known and at
// most as permissive as onRequest, and onRequest otherwise; when both are
// unknown the result is onRequest. So an unknown value can never produce a
// mode more permissive than onRequest.
func StricterPermissionMode(a, b PermissionMode) PermissionMode {
	ra, okA := PermissionModeStrictness(a)
	rb, okB := PermissionModeStrictness(b)
	onRequest, _ := PermissionModeStrictness(PermissionModeOnRequest)
	switch {
	case okA && okB:
		if ra <= rb {
			return a
		}
		return b
	case okA && ra <= onRequest:
		return a
	case okB && rb <= onRequest:
		return b
	default:
		return PermissionModeOnRequest
	}
}

func NormalizePermissionModeOrDefault(mode PermissionMode, fallback PermissionMode) PermissionMode {
	if normalized, ok := NormalizePermissionMode(string(mode)); ok {
		return normalized
	}
	if normalized, ok := NormalizePermissionMode(string(fallback)); ok {
		return normalized
	}
	return PermissionModeOnRequest
}

func NormalizeExecutionOrigin(raw string) ExecutionOrigin {
	switch ExecutionOrigin(raw) {
	case ExecutionOriginAutomation:
		return ExecutionOriginAutomation
	case ExecutionOriginSkillAgent:
		return ExecutionOriginSkillAgent
	default:
		return ExecutionOriginInteractive
	}
}

// GranularConfig provides fine-grained approval controls.
// Each field controls whether prompts in that category are allowed.
// When a field is true, commands in that category are auto-approved.
// When false, those requests are automatically denied instead of shown to the user.
type GranularConfig struct {
	// SandboxApproval controls whether sandbox approval prompts are allowed.
	// When true, commands that match sandbox policy can request approval.
	// When false, sandbox prompts are auto-denied.
	SandboxApproval bool `json:"sandbox_approval,omitempty"`

	// RulesApproval controls whether exec-policy "prompt" rules trigger approval prompts.
	// When true, rules with behavior=ask can prompt for approval.
	// When false, rule-based prompts are auto-denied.
	RulesApproval bool `json:"rules_approval,omitempty"`

	// SkillApproval controls whether skill script execution triggers approval prompts.
	// When true, skill scripts can request additional permissions.
	// When false, skill approval prompts are auto-denied.
	SkillApproval bool `json:"skill_approval,omitempty"`

	// RequestPermissionsApproval controls whether request_permissions tool triggers prompts.
	// When true, users can request additional permissions via tool.
	// When false, permission requests are auto-denied.
	RequestPermissionsApproval bool `json:"request_permissions_approval,omitempty"`
}

// DefaultGranularConfig returns the default granular configuration.
// All approvals are allowed by default.
func DefaultGranularConfig() GranularConfig {
	return GranularConfig{
		SandboxApproval:            true,
		RulesApproval:              true,
		SkillApproval:              true,
		RequestPermissionsApproval: true,
	}
}

// AllowsSandboxApproval returns true if sandbox approval prompts are allowed.
func (g GranularConfig) AllowsSandboxApproval() bool {
	return g.SandboxApproval
}

// AllowsRulesApproval returns true if exec-policy rule approval prompts are allowed.
func (g GranularConfig) AllowsRulesApproval() bool {
	return g.RulesApproval
}

// AllowsSkillApproval returns true if skill approval prompts are allowed.
func (g GranularConfig) AllowsSkillApproval() bool {
	return g.SkillApproval
}

// AllowsRequestPermissionsApproval returns true if request_permissions tool prompts are allowed.
func (g GranularConfig) AllowsRequestPermissionsApproval() bool {
	return g.RequestPermissionsApproval
}

// PermissionContext represents the permission mode context for a session.
// Aligned with OpenClaude's ToolPermissionContext (Tool.ts:123-138).
type PermissionContext struct {
	// Mode is the current permission mode (who approves)
	Mode PermissionMode `json:"mode"`

	// ExecutionMode is the current execution mode (what agent does: execute, plan)
	ExecutionMode string `json:"execution_mode,omitempty"`

	// PrePlanMode stores the mode before entering plan mode for restoration
	PrePlanMode PermissionMode `json:"pre_plan_mode,omitempty"`

	// IsBypassPermissionsModeAvailable indicates if bypass permissions were available before plan mode
	IsBypassPermissionsModeAvailable bool `json:"is_bypass_permissions_mode_available,omitempty"`

	// IsAutoModeAvailable indicates if auto mode is available for this session
	IsAutoModeAvailable bool `json:"is_auto_mode_available,omitempty"`

	// StrippedDangerousRules stores permissions that were stripped for auto mode safety
	StrippedDangerousRules map[string][]string `json:"stripped_dangerous_rules,omitempty"`
}

const (
	legacyPlanExecutionMode  = "plan"
	legacyPlanPermissionMode = PermissionMode("plan")
)

// NormalizeLegacyPlanMode rewrites deprecated mode=plan contexts so the
// approval mode remains in Mode while plan state lives in ExecutionMode.
func (p *PermissionContext) NormalizeLegacyPlanMode() {
	if p == nil {
		return
	}

	if p.Mode == legacyPlanPermissionMode {
		if p.PrePlanMode == "" || p.PrePlanMode == legacyPlanPermissionMode {
			p.PrePlanMode = PermissionModeOnRequest
		}
		if p.ExecutionMode == "" {
			p.ExecutionMode = legacyPlanExecutionMode
		}
		p.Mode = p.PrePlanMode
	}

	if p.ExecutionMode == legacyPlanExecutionMode && (p.PrePlanMode == "" || p.PrePlanMode == legacyPlanPermissionMode) {
		restoreMode := p.Mode
		if restoreMode == "" || restoreMode == legacyPlanPermissionMode {
			restoreMode = PermissionModeOnRequest
		}
		p.PrePlanMode = restoreMode
	}

	if p.Mode == "" || p.Mode == legacyPlanPermissionMode {
		p.Mode = PermissionModeOnRequest
	}

	if p.Mode == PermissionModeBypass {
		p.IsBypassPermissionsModeAvailable = true
	}
}

// PermissionBehavior represents what action should be taken.
type PermissionBehavior string

const (
	// PermissionBehaviorAllow allows the action.
	PermissionBehaviorAllow PermissionBehavior = "allow"

	// PermissionBehaviorDeny denies the action.
	PermissionBehaviorDeny PermissionBehavior = "deny"

	// PermissionBehaviorAsk asks the user for permission.
	PermissionBehaviorAsk PermissionBehavior = "ask"

	// PermissionBehaviorPassthrough delegates to the global permission pipeline.
	PermissionBehaviorPassthrough PermissionBehavior = "passthrough"
)

// ToolPermissionStage identifies which permission stage is being resolved.
type ToolPermissionStage string

const (
	ToolPermissionStageWholeTool ToolPermissionStage = "whole_tool"
	ToolPermissionStageGlobal    ToolPermissionStage = "global"
)

// ToolPermissionIntent identifies what kind of decision is being requested.
type ToolPermissionIntent string

const (
	ToolPermissionIntentCheck ToolPermissionIntent = "check"
	ToolPermissionIntentDeny  ToolPermissionIntent = "deny"
	ToolPermissionIntentAsk   ToolPermissionIntent = "ask"
	ToolPermissionIntentAllow ToolPermissionIntent = "allow"
)

// ToolPermissionRequest represents a structured permission check request.
type ToolPermissionRequest struct {
	// ToolName is the name of the tool being checked.
	ToolName string `json:"tool_name"`

	// Description is a human-readable explanation for the approval UI.
	Description string `json:"description,omitempty"`

	// ToolInput is the candidate tool input.
	ToolInput map[string]any `json:"tool_input"`

	// ToolUseID identifies the tool use when available.
	ToolUseID string `json:"tool_use_id,omitempty"`

	// SessionID identifies the session when available.
	SessionID SessionID `json:"session_id,omitempty"`

	// TurnID identifies the turn when available.
	TurnID TurnID `json:"turn_id,omitempty"`

	// PermissionMode is the active permission mode.
	PermissionMode PermissionMode `json:"permission_mode,omitempty"`

	// WorkingDirectory is the working directory visible to the tool runtime.
	WorkingDirectory string `json:"working_directory,omitempty"`

	// IsToolRunningInSandbox indicates whether the tool will run inside a sandbox.
	// Used for sandbox auto-allow logic.
	IsToolRunningInSandbox bool `json:"is_tool_running_in_sandbox,omitempty"`

	// Stage identifies which permission stage is being resolved.
	Stage ToolPermissionStage `json:"stage,omitempty"`

	// Intent identifies the decision kind requested for the stage.
	Intent ToolPermissionIntent `json:"intent,omitempty"`

	// Metadata carries optional runtime metadata.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// PermissionResolver resolves permission decisions for the execution runtime.
type PermissionResolver interface {
	ResolvePermission(ctx context.Context, request ToolPermissionRequest) PermissionResult
}

// CanUseToolFn is a function that checks if a tool can be used.
// It is the key interface that decouples permissions from tool execution.
// CanUseToolFn also implements PermissionResolver.
type CanUseToolFn func(ctx context.Context, request ToolPermissionRequest) PermissionResult

// ResolvePermission implements PermissionResolver for CanUseToolFn.
func (f CanUseToolFn) ResolvePermission(ctx context.Context, request ToolPermissionRequest) PermissionResult {
	return f(ctx, request)
}

// WholeToolPermissionRequest builds a whole-tool permission request for a specific stage.
func WholeToolPermissionRequest(
	toolName string,
	toolUseID string,
	sessionID SessionID,
	turnID TurnID,
	mode PermissionMode,
	workingDirectory string,
	intent ToolPermissionIntent,
	metadata map[string]any,
) ToolPermissionRequest {
	return ToolPermissionRequest{
		ToolName:         toolName,
		ToolUseID:        toolUseID,
		SessionID:        sessionID,
		TurnID:           turnID,
		PermissionMode:   mode,
		WorkingDirectory: workingDirectory,
		Stage:            ToolPermissionStageWholeTool,
		Intent:           intent,
		Metadata:         metadata,
	}
}

// GlobalToolPermissionRequest builds a full-input global permission request.
func GlobalToolPermissionRequest(
	toolName string,
	toolInput map[string]any,
	toolUseID string,
	sessionID SessionID,
	turnID TurnID,
	mode PermissionMode,
	workingDirectory string,
	metadata map[string]any,
) ToolPermissionRequest {
	return ToolPermissionRequest{
		ToolName:         toolName,
		ToolInput:        toolInput,
		ToolUseID:        toolUseID,
		SessionID:        sessionID,
		TurnID:           turnID,
		PermissionMode:   mode,
		WorkingDirectory: workingDirectory,
		Stage:            ToolPermissionStageGlobal,
		Intent:           ToolPermissionIntentCheck,
		Metadata:         metadata,
	}
}

// CanUseToolFunc adapts a PermissionResolver to the CanUseToolFn form.
func CanUseToolFunc(resolver PermissionResolver) CanUseToolFn {
	if resolver == nil {
		return nil
	}
	return resolver.ResolvePermission
}

// PermissionDecisionReasonType classifies why a permission decision was made.
type PermissionDecisionReasonType string

const (
	PermissionDecisionReasonRule        PermissionDecisionReasonType = "rule"
	PermissionDecisionReasonMode        PermissionDecisionReasonType = "mode"
	PermissionDecisionReasonTool        PermissionDecisionReasonType = "tool"
	PermissionDecisionReasonPrompt      PermissionDecisionReasonType = "prompt"
	PermissionDecisionReasonSafetyCheck PermissionDecisionReasonType = "safetyCheck"
	PermissionDecisionReasonAsyncAgent  PermissionDecisionReasonType = "asyncAgent"
	PermissionDecisionReasonOther       PermissionDecisionReasonType = "other"
)

// PermissionDecisionReason records the structured reason for a permission decision.
type PermissionDecisionReason struct {
	Type PermissionDecisionReasonType `json:"type"`

	// Source identifies the subsystem or rule source that produced the decision.
	Source string `json:"source,omitempty"`

	// Reason is a human-readable detail for the decision.
	Reason string `json:"reason,omitempty"`

	// RuleBehavior is set when Type is "rule" to indicate the original rule behavior
	// (allow, deny, ask). This lets the pipeline distinguish a content-specific
	// ask-rule from a passthrough ask, and treat ask-rules as bypass-immune.
	RuleBehavior PermissionBehavior `json:"rule_behavior,omitempty"`

	// ClassifierApprovable is set on safetyCheck decisions to indicate that
	// the auto-mode classifier may override this decision. Non-approvable
	// safety checks stay immune to ALL auto-approve paths.
	ClassifierApprovable bool `json:"classifier_approvable,omitempty"`

	// SubcommandResults contains results for each subcommand in a complex command.
	// Used by tools like bash that can execute multiple operations in a single call.
	// Aligned with OpenClaude's subcommandResults (permissions.ts:165-188).
	SubcommandResults map[string]PermissionResult `json:"subcommand_results,omitempty"`

	// Reasons contains detailed reasons for each subcommand when Type is "subcommandResults".
	// Aligned with OpenClaude's decisionReason.reasons (permissions.ts:167).
	Reasons map[string]PermissionDecisionReason `json:"reasons,omitempty"`
}

// PermissionResult represents the result of a permission check.
type PermissionResult struct {
	Behavior PermissionBehavior `json:"behavior"`

	// Reason provides a human-readable explanation.
	Reason string `json:"reason,omitempty"`

	// UpdatedInput contains normalized or rewritten input approved by permissions.
	UpdatedInput map[string]any `json:"updated_input,omitempty"`

	// Confidence indicates confidence in the decision (0-1).
	// Only applicable in auto mode with classifier.
	Confidence *float64 `json:"confidence,omitempty"`

	// Metadata contains additional information.
	Metadata map[string]any `json:"metadata,omitempty"`

	// DecisionReason captures the structured reason for this decision.
	DecisionReason *PermissionDecisionReason `json:"decision_reason,omitempty"`

	// DenialTracking tracks denial state when available.
	// Used for auto-mode fallback behavior.
	DenialTracking *DenialTrackingState `json:"denial_tracking,omitempty"`
}

// AllowWithUpdatedInput attaches normalized input to an allow result.
func AllowWithUpdatedInput(updatedInput map[string]any) PermissionResult {
	return PermissionResult{
		Behavior:     PermissionBehaviorAllow,
		UpdatedInput: updatedInput,
	}
}

// AllowWithDecisionReason creates an allow result with a structured reason.
func AllowWithDecisionReason(reason string, decisionReason *PermissionDecisionReason) PermissionResult {
	return PermissionResult{
		Behavior:       PermissionBehaviorAllow,
		Reason:         reason,
		DecisionReason: decisionReason,
	}
}

// AllowWithInput creates an Allow result with normalized input.
func AllowWithInput(reason string, updatedInput map[string]any) PermissionResult {
	return PermissionResult{
		Behavior:     PermissionBehaviorAllow,
		Reason:       reason,
		UpdatedInput: updatedInput,
	}
}

// AllowWithInputAndDecisionReason creates an allow result with normalized input and a structured reason.
func AllowWithInputAndDecisionReason(reason string, updatedInput map[string]any, decisionReason *PermissionDecisionReason) PermissionResult {
	return PermissionResult{
		Behavior:       PermissionBehaviorAllow,
		Reason:         reason,
		UpdatedInput:   updatedInput,
		DecisionReason: decisionReason,
	}
}

// Deny creates a Deny result.
func Deny(reason string) PermissionResult {
	return PermissionResult{
		Behavior: PermissionBehaviorDeny,
		Reason:   reason,
	}
}

// DenyWithDecisionReason creates a deny result with a structured reason.
func DenyWithDecisionReason(reason string, decisionReason *PermissionDecisionReason) PermissionResult {
	return PermissionResult{
		Behavior:       PermissionBehaviorDeny,
		Reason:         reason,
		DecisionReason: decisionReason,
	}
}

// Ask creates an Ask result.
func Ask(reason string) PermissionResult {
	return PermissionResult{
		Behavior: PermissionBehaviorAsk,
		Reason:   reason,
	}
}

// AskWithDecisionReason creates an ask result with a structured reason.
func AskWithDecisionReason(reason string, decisionReason *PermissionDecisionReason) PermissionResult {
	return PermissionResult{
		Behavior:       PermissionBehaviorAsk,
		Reason:         reason,
		DecisionReason: decisionReason,
	}
}

// Passthrough delegates the decision to the global permission pipeline.
func Passthrough(updatedInput map[string]any) PermissionResult {
	return PermissionResult{
		Behavior:     PermissionBehaviorPassthrough,
		UpdatedInput: updatedInput,
	}
}

// PassthroughWithDecisionReason delegates to the global permission pipeline with a structured reason.
func PassthroughWithDecisionReason(updatedInput map[string]any, decisionReason *PermissionDecisionReason) PermissionResult {
	return PermissionResult{
		Behavior:       PermissionBehaviorPassthrough,
		UpdatedInput:   updatedInput,
		DecisionReason: decisionReason,
	}
}

// IsAllowed returns true if the behavior is Allow.
func (p PermissionResult) IsAllowed() bool {
	return p.Behavior == PermissionBehaviorAllow
}

// IsDenied returns true if the behavior is Deny.
func (p PermissionResult) IsDenied() bool {
	return p.Behavior == PermissionBehaviorDeny
}

// IsAsk returns true if the behavior is Ask.
func (p PermissionResult) IsAsk() bool {
	return p.Behavior == PermissionBehaviorAsk
}

// IsPassthrough returns true if the decision should be delegated.
func (p PermissionResult) IsPassthrough() bool {
	return p.Behavior == PermissionBehaviorPassthrough
}

// IsBypassImmune returns true if this permission result should not be overridden
// by bypass mode. This applies to:
//   - Deny decisions (always immune)
//   - Ask decisions with safetyCheck reason (bypass-immune)
//   - Ask decisions with rule ask reason (content-specific ask rules)
func (p PermissionResult) IsBypassImmune() bool {
	if p.Behavior == PermissionBehaviorDeny {
		return true
	}
	if p.Behavior != PermissionBehaviorAsk {
		return false
	}
	if p.DecisionReason == nil {
		return false
	}
	switch p.DecisionReason.Type {
	case PermissionDecisionReasonSafetyCheck:
		return !p.DecisionReason.ClassifierApprovable
	case PermissionDecisionReasonRule:
		return p.DecisionReason.RuleBehavior == PermissionBehaviorAsk
	}
	return false
}

// PermissionRuleSource represents where a permission rule comes from.
// Aligned with OpenClaude's PermissionRuleSource (permissions.ts:1352-1365).
type PermissionRuleSource string

const (
	PermissionSourceStatic          PermissionRuleSource = "static"
	PermissionSourceDynamic         PermissionRuleSource = "dynamic"
	PermissionSourceClassifier      PermissionRuleSource = "classifier"
	PermissionSourceHook            PermissionRuleSource = "hook"
	PermissionSourceLocalSettings   PermissionRuleSource = "localSettings"
	PermissionSourceUserSettings    PermissionRuleSource = "userSettings"
	PermissionSourceProjectSettings PermissionRuleSource = "projectSettings"
	PermissionSourceCliArg          PermissionRuleSource = "cliArg"
	PermissionSourceSession         PermissionRuleSource = "session"
	// PermissionSourceManaged marks a rule imposed by whoever runs the host (an
	// organization): it outranks every other source and a user cannot change it.
	PermissionSourceManaged PermissionRuleSource = "managed"
)

// POWERSHELL_TOOL_NAME is the constant name for the PowerShell tool.
// Aligned with OpenClaude (permissions.ts:573).
const POWERSHELL_TOOL_NAME = "powershell"

// PermissionUpdate represents a permission rule update.
// Aligned with OpenClaude's PermissionUpdate (permissions.ts:1420-1447).
type PermissionUpdate struct {
	// Type is the update type.
	Type PermissionUpdateType `json:"type"`

	// Rules are the permission rule values to update.
	Rules []PermissionRuleValue `json:"rules"`

	// Behavior is the permission behavior.
	Behavior PermissionBehavior `json:"behavior"`

	// Destination is where to apply the rules.
	Destination PermissionRuleSource `json:"destination"`
}

// PermissionUpdateType represents the type of permission update.
// Aligned with OpenClaude (permissions.ts:1421-1423).
type PermissionUpdateType string

const (
	PermissionUpdateTypeAddRules     PermissionUpdateType = "addRules"
	PermissionUpdateTypeReplaceRules PermissionUpdateType = "replaceRules"
	PermissionUpdateTypeDeleteRules  PermissionUpdateType = "deleteRules"
)

// PermissionRuleValue is the structured value of a permission rule.
type PermissionRuleValue struct {
	// ToolName identifies the tool this rule targets.
	ToolName string `json:"tool_name"`

	// RuleContent carries optional content-specific matching handled by the tool.
	RuleContent string `json:"rule_content,omitempty"`
}
