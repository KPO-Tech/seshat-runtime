package permissions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	automode "github.com/KPO-Tech/seshat-runtime/internal/permissions/auto"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/internal/utils"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
)

// Integrator integrates permission checking with tool execution.
// The Orchestrator is the primary execution path and has its own safety checks
// via SafetyChecker. The Integrator provides a PermissionResolver for contexts
// that need standalone permission resolution (e.g. the query loop).
type Integrator struct {
	engine *Engine

	promptFn types.PromptFn

	mu           sync.RWMutex
	sessionTools map[types.SessionID]map[string]bool

	// turnGrants holds request_permissions grants made with scope="turn" -
	// in-memory only, never written to permissions.json. A grant only
	// matches lookups carrying the same TurnID, so it naturally stops
	// applying once the turn it was made for ends, without needing an
	// explicit expiry sweep.
	turnGrants map[turnScopeKey]map[string]bool
}

// turnScopeKey identifies one turn within one session, for turn-scoped
// (as opposed to session-scoped/disk-persisted) permission grants.
type turnScopeKey struct {
	SessionID types.SessionID
	TurnID    types.TurnID
}

// NewIntegrator creates a new permission integrator.
func NewIntegrator(engine *Engine) *Integrator {
	return &Integrator{
		engine:       engine,
		sessionTools: make(map[types.SessionID]map[string]bool),
		turnGrants:   make(map[turnScopeKey]map[string]bool),
	}
}

// SetPromptFn sets the prompt function for asking users.
func (i *Integrator) SetPromptFn(fn types.PromptFn) {
	i.promptFn = fn
}

// effectivePromptFn prefers the per-turn prompt bridge carried on ctx (see
// types.WithPromptFn) over i.promptFn, the value set via SetPromptFn. A host
// that reuses one *Integrator (owned by one *sdk.Client) across concurrent
// turns - e.g. a per-user/provider client cache - must use the context path
// to stay turn-isolated: i.promptFn is a single shared field, so two
// concurrent turns calling SetPromptFn would otherwise race and could route
// a permission prompt to the wrong session. i.promptFn remains as a fallback
// for callers that never share an Integrator across turns.
func (i *Integrator) effectivePromptFn(ctx context.Context) types.PromptFn {
	if fn := types.PromptFnFromContext(ctx); fn != nil {
		return fn
	}
	return i.promptFn
}

// HasPromptFn reports whether a prompt function is actually available for
// ctx - the same check effectivePromptFn uses to resolve an ask, exposed so
// a caller can decide ahead of time whether this context can show an
// interactive prompt at all (see ExecuteRequest.ShouldAvoidPermissionPrompts).
func (i *Integrator) HasPromptFn(ctx context.Context) bool {
	return i.effectivePromptFn(ctx) != nil
}

// Resolver creates a typed PermissionResolver that integrates with the permission engine.
func (i *Integrator) Resolver(sessionID types.SessionID, turnID types.TurnID, mode types.PermissionMode) types.PermissionResolver {
	return i.ResolverWithContext(sessionID, turnID, &types.PermissionContext{Mode: mode}, nil)
}

// ResolverWithContext creates a typed PermissionResolver that carries the live
// session permission context and transcript into the permission engine.
func (i *Integrator) ResolverWithContext(
	sessionID types.SessionID,
	turnID types.TurnID,
	permissionContext *types.PermissionContext,
	transcript []types.Message,
) types.PermissionResolver {
	return types.CanUseToolFn(func(ctx context.Context, request types.ToolPermissionRequest) types.PermissionResult {
		toolName := request.ToolName
		toolInput := request.ToolInput
		activePermissionContext := cloneSessionPermissionContext(permissionContext)
		activePermissionContext.NormalizeLegacyPlanMode()
		requestMode := request.PermissionMode
		if requestMode == "" {
			requestMode = activePermissionContext.Mode
		}
		if requestMode == "" {
			requestMode = types.PermissionModeOnRequest
		}
		requestSessionID := request.SessionID
		if requestSessionID == "" {
			requestSessionID = sessionID
		}
		requestTurnID := request.TurnID
		if requestTurnID == "" {
			requestTurnID = turnID
		}

		// Whatever this specific request is asking to do to a path (write,
		// create, read, ...) - derived from the sandbox.PermissionRequest the
		// tool itself built (see sandbox.ResolveToolPermission), not from
		// toolName, so it matches regardless of which tool is asking. Empty
		// for tools that don't go through the sandbox bridge or declare no
		// paths (nothing to match a request_permissions grant against).
		grantKeys := filesystemGrantLookupKeys(request.Metadata)

		metadata := clonePermissionMetadata(request.Metadata)
		if metadata == nil {
			metadata = make(map[string]any)
		}
		if request.WorkingDirectory != "" {
			metadata["working_directory"] = request.WorkingDirectory
		}
		if len(transcript) > 0 {
			metadata["transcript_messages"] = append([]types.Message(nil), transcript...)
		}

		pctx := &PermissionContext{
			Mode:                             requestMode,
			ExecutionMode:                    activePermissionContext.ExecutionMode,
			ToolName:                         toolName,
			ToolInput:                        toolInput,
			SessionID:                        requestSessionID,
			TurnID:                           requestTurnID,
			Stage:                            request.Stage,
			Intent:                           request.Intent,
			IsConcurrent:                     false,
			ToolUseID:                        request.ToolUseID,
			IsBypassPermissionsModeAvailable: activePermissionContext.IsBypassPermissionsModeAvailable,
			Additional:                       metadata,
		}
		if resolvedTool := tool.ToolFromMetadata(metadata); resolvedTool != nil {
			pctx.Tool = resolvedTool
		}
		// Read ShouldAvoidPermissionPrompts from metadata.
		if metadata != nil {
			if v, ok := metadata["should_avoid_permission_prompts"].(bool); ok {
				pctx.ShouldAvoidPermissionPrompts = v
			}
		}

		// What the user approved for the session applies unless a deny rule does:
		// a rule written to forbid something must outlast a click on "always
		// allow". An ask rule does not outrank it: that is how a tool is made to ask,
		// and "always allow" is the answer to it.
		if requestSessionID != "" && !i.engine.hasDenyRule(ctx, pctx) &&
			i.sessionApproves(requestSessionID, requestTurnID, toolName, toolInput, grantKeys) {
			return types.AllowWithInputAndDecisionReason("auto-approved for session", utils.CloneInput(toolInput), &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonMode,
				Source: "session",
				Reason: "auto-approved for session",
			})
		}

		result, err := i.engine.CheckPermission(ctx, pctx)
		if result.UpdatedInput == nil && toolInput != nil {
			result.UpdatedInput = toolInput
		}
		if err != nil {
			return types.AskWithDecisionReason(fmt.Sprintf("permission check failed: %v", err), &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonOther,
				Source: "integrator",
				Reason: err.Error(),
			})
		}

		// Non-ask results (allow, deny, passthrough) are returned directly.
		if result.Behavior != types.PermissionBehaviorAsk {
			return result
		}

		// dontAsk mode: transform ask → deny at the integrator level too,
		// for cases where the engine returned ask (e.g., from hooks or
		// tool-level checks that the engine passed through).
		if requestMode == types.PermissionModeNever {
			return types.DenyWithDecisionReason(
				fmt.Sprintf("permission to use %s denied: running in dontAsk mode", toolName),
				&types.PermissionDecisionReason{
					Type:   types.PermissionDecisionReasonMode,
					Source: "dontAsk",
					Reason: "dontAsk mode enabled",
				},
			)
		}

		// Headless auto-deny: when permission prompts should be avoided.
		if pctx.ShouldAvoidPermissionPrompts {
			return types.DenyWithDecisionReason(
				fmt.Sprintf("permission to use %s denied: permission prompts not available in this context", toolName),
				&types.PermissionDecisionReason{
					Type:   types.PermissionDecisionReasonAsyncAgent,
					Source: "headless",
					Reason: "permission prompts are not available in this context",
				},
			)
		}

		promptFn := i.effectivePromptFn(ctx)
		if promptFn == nil {
			return result
		}

		// Emit tool.permission_required before prompting, so a host that
		// only listens on the runtime-event channel (not PromptFn) can show
		// its own approval UI too - this event existed but nothing ever
		// constructed it (H6 in TODO.md).
		if emitter, ok := ctx.Value(types.RuntimeEventEmitterKey).(func(types.RuntimeEvent)); ok && emitter != nil {
			reqCopy := request
			emitter(types.RuntimeEvent{
				Type:              types.RuntimeEventTypeToolPermissionRequired,
				SessionID:         requestSessionID,
				TurnID:            requestTurnID,
				Timestamp:         time.Now(),
				PermissionRequest: &reqCopy,
			})
		}

		message := fmt.Sprintf("Allow tool '%s'?", toolName)
		if result.DecisionReason != nil && result.DecisionReason.Source == planApprovalSource && result.Reason != "" {
			message = result.Reason
		}
		promptReq := types.PromptRequest{
			Type:    types.PromptTypeConfirm,
			Message: message,
			Metadata: map[string]any{
				"tool_name":         toolName,
				"tool_input":        toolInput,
				"tool_use_id":       request.ToolUseID,
				"working_directory": request.WorkingDirectory,
			},
		}

		response, err := promptFn(ctx, promptReq)
		if err != nil {
			return types.DenyWithDecisionReason(fmt.Sprintf("prompt failed: %v", err), &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonPrompt,
				Source: "prompt",
				Reason: err.Error(),
			})
		}

		if response.Cancelled {
			return types.DenyWithDecisionReason("user cancelled", &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonPrompt,
				Source: "prompt",
				Reason: "user cancelled",
			})
		}

		approved, always := promptApproval(response.Value)

		if approved {
			reason := i.recordApproval(requestSessionID, requestTurnID, toolName, toolInput, metadata, always)
			return types.AllowWithInputAndDecisionReason(reason, result.UpdatedInput, &types.PermissionDecisionReason{
				Type:   types.PermissionDecisionReasonPrompt,
				Source: "prompt",
				Reason: reason,
			})
		}

		denyReason := promptDenyReason(response)
		return types.DenyWithDecisionReason(denyReason, &types.PermissionDecisionReason{
			Type:   types.PermissionDecisionReasonPrompt,
			Source: "prompt",
			Reason: denyReason,
		})
	})
}

// sessionAllows reports whether the session already approved one of lookupKeys (a tool name, or the content-scoped key of a
// request_permissions call), or whether one of grantKeys (what the request asks to do to a path) was granted for the session or, within
// this turn, for the turn. The grants of a session are read from memory; a session that is not in memory is loaded from its permissions
// file first.
func (i *Integrator) sessionAllows(sessionID types.SessionID, turnID types.TurnID, lookupKeys, grantKeys []string) bool {
	// 1. Fast path: check in-memory map
	i.mu.RLock()
	hasSession := i.sessionTools != nil && i.sessionTools[sessionID] != nil
	var allowed bool
	if hasSession {
		allowed = anyKeyAllowed(i.sessionTools[sessionID], lookupKeys) ||
			anyKeyAllowed(i.sessionTools[sessionID], grantKeys)
	}
	if !allowed {
		if turnMap := i.turnGrants[turnScopeKey{SessionID: sessionID, TurnID: turnID}]; turnMap != nil {
			allowed = anyKeyAllowed(turnMap, grantKeys)
		}
	}
	i.mu.RUnlock()

	// 2. Slow path: if session is not in memory, try to load from disk
	if !hasSession {
		i.mu.Lock()
		// Double-check inside lock
		if i.sessionTools == nil {
			i.sessionTools = make(map[types.SessionID]map[string]bool)
		}
		if i.sessionTools[sessionID] == nil {
			filePath := runtimepath.SessionPermissionsPath("", string(sessionID))
			loadedMap := make(map[string]bool)
			if data, err := os.ReadFile(filePath); err == nil {
				_ = json.Unmarshal(data, &loadedMap)
			}
			i.sessionTools[sessionID] = loadedMap
		}
		allowed = anyKeyAllowed(i.sessionTools[sessionID], lookupKeys) ||
			anyKeyAllowed(i.sessionTools[sessionID], grantKeys)
		if !allowed {
			if turnMap := i.turnGrants[turnScopeKey{SessionID: sessionID, TurnID: turnID}]; turnMap != nil {
				allowed = anyKeyAllowed(turnMap, grantKeys)
			}
		}
		i.mu.Unlock()
	}
	return allowed
}

// recordApproval remembers what the user just approved and returns the reason to give for the decision: "always" persists the tool (for a shell tool: the program or the exact line, see rememberApproval) for
// the session, a request_permissions call with scope=session persists under its content-scoped key, and the filesystem escalation it
// asked for is registered for the session or, by default, for the turn only.
func (i *Integrator) recordApproval(sessionID types.SessionID, turnID types.TurnID, toolName string, toolInput, metadata map[string]any, always bool) string {
	reason := "user approved"
	if always && sessionID != "" {
		i.rememberApproval(sessionID, toolName, toolInput)
		reason = "always approved for session"
	}
	// request_permissions itself asked for scope=session — persist under
	// its content-scoped key (not the plain tool name) regardless of
	// whether the UI has an "always" concept, since the scope was
	// declared by the model's own input, not a special user response.
	grantScope, _ := metadata["grant_scope"].(string)
	if grantScope == "session" && sessionID != "" {
		if key := requestPermissionsSessionKey(toolName, toolInput); key != "" {
			i.persistSessionApproval(sessionID, key)
			reason = "approved for session (request_permissions scope=session)"
		}
	}
	// The actual point of request_permissions: register the escalation
	// itself so the operation it was requested FOR (write_file,
	// edit_file, ...) finds it too, not just a repeat request_permissions
	// call asking for the same thing (that's what the block above
	// covers). scope="session" survives on disk across turns/restarts;
	// the default scope="turn" stays in memory and only matches
	// lookups within this same turn.
	if toolName == requestPermissionsToolName && sessionID != "" {
		if grantKeys := requestedFilesystemGrantKeys(toolInput); len(grantKeys) > 0 {
			if grantScope == "session" {
				for _, key := range grantKeys {
					i.persistSessionApproval(sessionID, key)
				}
			} else {
				i.grantForTurn(sessionID, turnID, grantKeys)
			}
		}
	}
	return reason
}

// promptApproval reads the answer of the user to a permission prompt: a boolean is the decision itself, and the text "always" approves
// and asks to remember the approval for the session. Anything else is a refusal.
func promptApproval(value any) (approved, always bool) {
	if b, ok := value.(bool); ok {
		return b, false
	}
	if s, ok := value.(string); ok && s == "always" {
		return true, true
	}
	return false, false
}

// promptDenyReason is the reason given when the user denies: the one in the metadata of the answer, or "user denied".
func promptDenyReason(response types.PromptResponse) string {
	denyReason := "user denied"
	if response.Metadata != nil {
		if r, ok := response.Metadata["reason"].(string); ok && r != "" {
			denyReason = r
		}
	}
	return denyReason
}

// CanUseTool creates a CanUseToolFn that integrates with the permission engine.
func (i *Integrator) CanUseTool(sessionID types.SessionID, turnID types.TurnID, mode types.PermissionMode) types.CanUseToolFn {
	return types.CanUseToolFunc(i.Resolver(sessionID, turnID, mode))
}

// AutoModeAvailable reports whether the underlying permission engine has an
// operational auto-mode classifier configured.
func (i *Integrator) AutoModeAvailable() bool {
	if i == nil || i.engine == nil {
		return false
	}
	return i.engine.IsAutoModeAvailable()
}

// SetAutoModeProviderClient wires the auto-mode classifier to the given provider client.
func (i *Integrator) SetAutoModeProviderClient(apiClient *providers.Client, model types.ModelIdentifier) {
	if i == nil || i.engine == nil || apiClient == nil {
		return
	}
	classifierConfig := automode.DefaultTwoStageConfig()
	classifierConfig.Model = model.ProviderModelName()
	autoClassifier := automode.NewTwoStageClassifierWithAPI(classifierConfig, automode.NewClassifierAPIClient(apiClient).WithProvider(model.Provider))
	i.engine.SetClassifier(providerBackedAutoModeClassifier{classifier: autoClassifier})
	i.engine.SetAdvancedClassifier(autoClassifier)
}

// CheckToolUse checks permissions for a specific tool use.
// This is a convenience method that builds the request from tool use content.
func (i *Integrator) CheckToolUse(
	ctx context.Context,
	toolUse types.ToolUseContent,
	toolDef tool.Definition,
	sessionID types.SessionID,
	turnID types.TurnID,
	mode types.PermissionMode,
) (types.PermissionResult, error) {
	resolver := i.Resolver(sessionID, turnID, mode)
	result := resolver.ResolvePermission(ctx, types.GlobalToolPermissionRequest(
		toolUse.Name,
		toolUse.Input,
		toolUse.ID,
		sessionID,
		turnID,
		mode,
		"",
		nil,
	))
	if result.IsDenied() {
		return result, nil
	}

	// Build a richer context for the engine's second pass (with ToolDefinition).
	pctx := &PermissionContext{
		Mode:           mode,
		ToolName:       toolUse.Name,
		ToolInput:      toolUse.Input,
		SessionID:      sessionID,
		TurnID:         turnID,
		Stage:          types.ToolPermissionStageGlobal,
		Intent:         types.ToolPermissionIntentCheck,
		IsConcurrent:   toolDef.IsConcurrencySafe,
		ToolDefinition: &toolDef,
	}

	result, err := i.engine.CheckPermission(ctx, pctx)
	if err != nil {
		return types.PermissionResult{}, types.WrapError(types.ErrCodePermissionError, "permission check failed", err)
	}
	if result.IsPassthrough() {
		return types.AllowWithInput(result.Reason, result.UpdatedInput), nil
	}

	return result, nil
}

// persistSessionApproval records key as granted for sessionID, both in memory
// and on disk (~/.../data/permissions/<id>.json), reusing the same file
// the "always approve this tool" flow already writes to. key is either a bare
// tool name (whole-tool grant) or a requestPermissionsSessionKey signature
// (content-scoped grant).
func (i *Integrator) persistSessionApproval(sessionID types.SessionID, key string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sessionTools == nil {
		i.sessionTools = make(map[types.SessionID]map[string]bool)
	}
	if i.sessionTools[sessionID] == nil {
		i.sessionTools[sessionID] = make(map[string]bool)
	}
	i.sessionTools[sessionID][key] = true

	filePath := runtimepath.SessionPermissionsPath("", string(sessionID))
	if err := os.MkdirAll(filepath.Dir(filePath), 0700); err == nil {
		if data, err := json.Marshal(i.sessionTools[sessionID]); err == nil {
			_ = os.WriteFile(filePath, data, 0600)
		}
	}
}

// anyKeyAllowed reports whether any of keys is marked granted in grants.
func anyKeyAllowed(grants map[string]bool, keys []string) bool {
	for _, k := range keys {
		if grants[k] {
			return true
		}
	}
	return false
}

// requestPermissionsToolName is request_permissions.ToolName, duplicated
// here (a plain string literal, not an import) to avoid the permission
// engine depending on one specific tool's package.
const requestPermissionsToolName = "request_permissions"

// grantForTurn records grantKeys as granted for (sessionID, turnID) only -
// in memory, never written to disk. Used for request_permissions calls with
// the default scope="turn": the grant must stop applying once this turn
// ends, which persistSessionApproval's disk-backed, indefinite grant can't
// express.
func (i *Integrator) grantForTurn(sessionID types.SessionID, turnID types.TurnID, grantKeys []string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := turnScopeKey{SessionID: sessionID, TurnID: turnID}
	if i.turnGrants == nil {
		i.turnGrants = make(map[turnScopeKey]map[string]bool)
	}
	if i.turnGrants[key] == nil {
		i.turnGrants[key] = make(map[string]bool)
	}
	for _, gk := range grantKeys {
		i.turnGrants[key][gk] = true
	}
}

// requestedFilesystemGrantKeys reads a request_permissions call's own raw
// input (permissions.filesystem.{paths,access}) and returns one grant key
// per (access kind, path) pair granted - e.g. a request for
// access=[write,create] on paths=[/a, /b] yields 4 keys. These are the same
// keys filesystemGrantLookupKeys derives (by ancestor walk) from a *later*
// tool's own sandbox.PermissionRequest, so a write_file call under one of
// these exact paths matches directly.
func requestedFilesystemGrantKeys(input map[string]any) []string {
	perms, ok := input["permissions"].(map[string]any)
	if !ok {
		return nil
	}
	fs, ok := perms["filesystem"].(map[string]any)
	if !ok {
		return nil
	}
	paths := stringSlice(fs["paths"])
	access := stringSlice(fs["access"])
	if len(paths) == 0 || len(access) == 0 {
		return nil
	}
	keys := make([]string, 0, len(paths)*len(access))
	for _, p := range paths {
		cleaned := filepath.Clean(p)
		for _, a := range access {
			keys = append(keys, fmt.Sprintf("grant::%s::%s", a, cleaned))
		}
	}
	return keys
}

// filesystemGrantLookupKeys derives the grant keys that would cover this
// specific permission request, from the sandbox.PermissionRequest the
// requesting tool built (carried verbatim in metadata by
// sandbox.ResolveToolPermission/PermissionRequest.MetadataMap - see
// sandbox.MetadataRequestKey). For each declared path it includes the exact
// path plus every ancestor directory, since a request_permissions grant on
// a directory covers files created/read underneath it, not just that exact
// path. Returns nil for requests that didn't go through the sandbox bridge
// or declare no paths (nothing a filesystem grant could cover).
func filesystemGrantLookupKeys(metadata map[string]any) []string {
	if metadata == nil {
		return nil
	}
	req, ok := metadata[sandbox.MetadataRequestKey].(sandbox.PermissionRequest)
	if !ok || req.Access == "" || len(req.Paths) == 0 {
		return nil
	}
	var keys []string
	for _, p := range req.Paths {
		clean := filepath.Clean(p)
		for {
			keys = append(keys, fmt.Sprintf("grant::%s::%s", req.Access, clean))
			parent := filepath.Dir(clean)
			if parent == clean {
				break
			}
			clean = parent
		}
	}
	return keys
}

// stringSlice extracts the string elements of a []any (as JSON-decoded tool
// input always is), skipping anything else and trimming/dropping blanks.
func stringSlice(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// requestPermissionsSessionKey builds a stable signature for a
// request_permissions call's escalation target (filesystem paths+access,
// network targets), so a session-scoped grant only auto-approves a future
// request_permissions call asking for the exact same escalation — not just
// any escalation, and not other tools. Returns "" when toolName isn't
// request_permissions or input carries no recognizable permissions payload.
func requestPermissionsSessionKey(toolName string, input map[string]any) string {
	if toolName != "request_permissions" {
		return ""
	}
	perms, ok := input["permissions"].(map[string]any)
	if !ok {
		return ""
	}
	var parts []string
	if fs, ok := perms["filesystem"].(map[string]any); ok {
		parts = append(parts, "fs:"+canonicalStringSlice(fs["paths"])+"/"+canonicalStringSlice(fs["access"]))
	}
	if net, ok := perms["network"].(map[string]any); ok {
		enabled, _ := net["enabled"].(bool)
		parts = append(parts, fmt.Sprintf("net:%s/%v", canonicalStringSlice(net["targets"]), enabled))
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return "request_permissions::" + strings.Join(parts, "|")
}

// canonicalStringSlice renders a []any of strings (as decoded from JSON tool
// input) into a sorted, deduplication-friendly comma-joined string, so two
// requests naming the same paths/targets in a different order still match.
func canonicalStringSlice(raw any) string {
	items, ok := raw.([]any)
	if !ok {
		return ""
	}
	strs := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			strs = append(strs, strings.TrimSpace(s))
		}
	}
	sort.Strings(strs)
	return strings.Join(strs, ",")
}

func cloneSessionPermissionContext(ctx *types.PermissionContext) *types.PermissionContext {
	if ctx == nil {
		return &types.PermissionContext{Mode: types.PermissionModeOnRequest}
	}
	cloned := *ctx
	if ctx.StrippedDangerousRules != nil {
		cloned.StrippedDangerousRules = make(map[string][]string, len(ctx.StrippedDangerousRules))
		for key, values := range ctx.StrippedDangerousRules {
			cloned.StrippedDangerousRules[key] = append([]string(nil), values...)
		}
	}
	return &cloned
}

func clonePermissionMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

type providerBackedAutoModeClassifier struct {
	classifier *automode.TwoStageClassifier
}

func (a providerBackedAutoModeClassifier) Classify(ctx context.Context, toolName string, input map[string]any) (Classification, error) {
	result, err := a.classifier.Classify(ctx, toolName, input)
	if err != nil {
		return Classification{}, err
	}
	return Classification{
		Allowed:    result.Allowed,
		Confidence: result.Confidence,
		Reason:     result.Reason,
	}, nil
}

// BatchCheckToolUses checks permissions for multiple tool uses.
func (i *Integrator) BatchCheckToolUses(
	ctx context.Context,
	toolUses []types.ToolUseContent,
	tools map[string]tool.Tool,
	sessionID types.SessionID,
	turnID types.TurnID,
	mode types.PermissionMode,
) ([]types.PermissionResult, error) {
	results := make([]types.PermissionResult, len(toolUses))

	for idx, toolUse := range toolUses {
		t, ok := tools[toolUse.Name]
		if !ok {
			results[idx] = types.Deny(fmt.Sprintf("tool not found: %s", toolUse.Name))
			continue
		}

		result, err := i.CheckToolUse(ctx, toolUse, t.Definition(), sessionID, turnID, mode)
		if err != nil {
			return nil, fmt.Errorf("failed to check tool '%s': %w", toolUse.Name, err)
		}

		results[idx] = result
	}

	return results, nil
}

// FilterAllowedToolUses filters tool uses to only those allowed.
func (i *Integrator) FilterAllowedToolUses(
	ctx context.Context,
	toolUses []types.ToolUseContent,
	tools map[string]tool.Tool,
	sessionID types.SessionID,
	turnID types.TurnID,
	mode types.PermissionMode,
) ([]types.ToolUseContent, []types.PermissionResult, error) {
	results, err := i.BatchCheckToolUses(ctx, toolUses, tools, sessionID, turnID, mode)
	if err != nil {
		return nil, nil, err
	}

	allowed := make([]types.ToolUseContent, 0)
	for idx, result := range results {
		if result.IsAllowed() {
			allowed = append(allowed, toolUses[idx])
		}
	}

	return allowed, results, nil
}

// PermissionMiddleware creates a middleware that checks permissions before tool execution.
func (i *Integrator) PermissionMiddleware(
	sessionID types.SessionID,
	turnID types.TurnID,
	mode types.PermissionMode,
) func(ctx context.Context, toolName string, toolInput map[string]any) error {
	return func(ctx context.Context, toolName string, toolInput map[string]any) error {
		resolver := i.Resolver(sessionID, turnID, mode)
		result := resolver.ResolvePermission(ctx, types.GlobalToolPermissionRequest(
			toolName,
			toolInput,
			"",
			sessionID,
			turnID,
			mode,
			"",
			nil,
		))

		if result.IsDenied() {
			return &PermissionDeniedError{
				ToolName: toolName,
				Reason:   result.Reason,
			}
		}

		if result.IsPassthrough() {
			return nil
		}

		if result.IsAsk() && i.effectivePromptFn(ctx) == nil {
			return &PermissionDeniedError{
				ToolName: toolName,
				Reason:   "permission required but no prompt function available",
			}
		}

		return nil
	}
}

// PermissionDeniedError represents a permission denied error.
type PermissionDeniedError struct {
	ToolName string
	Reason   string
}

func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("permission denied for tool '%s': %s", e.ToolName, e.Reason)
}

// IsPermissionDenied returns true if an error is a permission denied error.
func IsPermissionDenied(err error) bool {
	var target *PermissionDeniedError
	return errors.As(err, &target)
}
