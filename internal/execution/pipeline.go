package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	runtimehooks "github.com/KPO-Tech/seshat-runtime/internal/runtime/hooks"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func (o *Orchestrator) executePreparedTool(
	ctx context.Context,
	prepared preparedToolUse,
	req ExecuteRequest,
	toolCtx tool.ToolUseContext,
) toolExecutionOutcome {
	initialProgress := progressForStage(prepared.toolUse, "resolving tool", 0, nil)
	progressUpdates := []types.ToolProgress{initialProgress}
	o.emitProgress(req, initialProgress)
	extraMessages := []types.Message{}

	if prepared.failure != nil {
		return o.failPreparedToolUse(prepared, progressUpdates, extraMessages, req)
	}

	state := o.newRuntimeStateFromPrepared(prepared, req, toolCtx)
	state = state.withValidatedInput(prepared.validatedInput)
	state = state.withBackfilledInput(prepared.backfilledInput)
	if rawBytes, err := json.Marshal(prepared.validatedInput); err == nil {
		state.callInput.Raw = string(rawBytes)
	} else {
		state.callInput.Raw = "{}"
	}
	state.callInput.Parsed = cloneToolInput(prepared.validatedInput)
	state.trace.FinalInput = cloneToolInput(prepared.validatedInput)

	return o.executePreparedToolPipeline(ctx, prepared, req, state, progressUpdates, extraMessages, false)
}

func (o *Orchestrator) executePreparedToolPipeline(
	ctx context.Context,
	prepared preparedToolUse,
	req ExecuteRequest,
	state toolRuntimeState,
	progressUpdates []types.ToolProgress,
	extraMessages []types.Message,
	observableInputModified bool,
) toolExecutionOutcome {
	toolUse := prepared.toolUse
	index := prepared.index
	t := prepared.tool

	// OTel span per tool call. Span name follows the convention "tool <name>".
	spanCtx, span := otel.Tracer("seshat").Start(ctx, "tool "+toolUse.Name,
		oteltrace.WithAttributes(
			attribute.String("tool.name", toolUse.Name),
			attribute.String("tool.use_id", toolUse.ID),
		),
	)
	defer span.End()
	ctx = spanCtx

	progPreHook := progressForStage(toolUse, "running pre-hooks", 20, state.trace.Metadata)
	progressUpdates = append(progressUpdates, progPreHook)
	o.emitProgress(req, progPreHook)

	currentInput := cloneToolInput(prepared.backfilledInput)
	preHookResult := runtimehooks.ExecutePre(ctx, o.hooks.Pre(), runtimehooks.ToolHookInput{
		ToolName:  toolUse.Name,
		ToolUseID: toolUse.ID,
		Input:     cloneToolInput(currentInput),
		ToolCtx:   state.toolCtx,
	})

	// Add hook metadata to the tracking state so subsequent progress updates
	// carry it for the TUI to render.
	if preHookResult.Metadata != nil {
		for k, v := range preHookResult.Metadata {
			state.trace.Metadata[k] = v
		}
	}

	if ctx.Err() != nil {
		return o.cancelledOutcome(toolUse, index, progressUpdates, state, extraMessages, req)
	}
	if preHookResult.Stop != nil {
		return o.hookStopOutcome(toolUse, index, progressUpdates, state, preHookResult.Stop, extraMessages, req)
	}
	if preHookResult.UpdatedInput != nil && !mapsEqual(currentInput, preHookResult.UpdatedInput) {
		currentInput = preHookResult.UpdatedInput
		observableInputModified = true
	}
	extraMessages = append(extraMessages, preHookResult.ExtraMessages...)

	progSafety := progressForStage(toolUse, "running safety checks", 25, state.trace.Metadata)
	progressUpdates = append(progressUpdates, progSafety)
	o.emitProgress(req, progSafety)

	if o.safetyChecker != nil {
		safetyResult := o.safetyChecker.CheckSafety(toolUse.Name, currentInput)
		if safetyResult.IsDangerous {
			err := fmt.Errorf("safety check blocked: %s", safetyResult.Reason)
			return o.failedOutcome(toolUse, index, progressUpdates, ErrorStagePermission, types.PermissionResult{
				Behavior: types.PermissionBehaviorDeny,
				Reason:   fmt.Sprintf("dangerous pattern detected: %s", safetyResult.Reason),
				DecisionReason: &types.PermissionDecisionReason{
					Type:   types.PermissionDecisionReasonSafetyCheck,
					Source: safetyResult.CheckType,
					Reason: safetyResult.Reason,
				},
			}, state.trace, err, extraMessages, req)
		}
	}

	progPerm := progressForStage(toolUse, "checking permissions", 33, state.trace.Metadata)
	progressUpdates = append(progressUpdates, progPerm)
	o.emitProgress(req, progPerm)

	permResult, failure := o.resolveToolPermissions(ctx, state, currentInput, req)
	if failure != nil {
		state = state.withPermissionResults(permResult.LocalPermission, permResult.GlobalPermission, permResult.FinalInput)
		return o.failedOutcome(toolUse, index, progressUpdates, failure.stage, failure.permissionResult, state.trace, failure.err, extraMessages, req)
	}
	state = state.withPermissionResults(permResult.LocalPermission, permResult.GlobalPermission, permResult.FinalInput)
	if !observableInputModified && mapsEqual(prepared.backfilledInput, permResult.FinalInput) {
		if rawBytes, err := json.Marshal(prepared.validatedInput); err == nil {
			state.callInput.Raw = string(rawBytes)
		} else {
			state.callInput.Raw = "{}"
		}
		state.callInput.Parsed = cloneToolInput(prepared.validatedInput)
		state.trace.FinalInput = cloneToolInput(prepared.validatedInput)
	}

	if req.DenialTracking != nil && permResult.GlobalPermission.Behavior == types.PermissionBehaviorAllow {
		req.DenialTracking.RecordSuccess()
	}

	progCall := progressForStage(toolUse, "calling tool", 66, state.trace.Metadata)
	progressUpdates = append(progressUpdates, progCall)
	o.emitProgress(req, progCall)

	callResult := o.callToolSafe(ctx, state, req.PermissionCheck)
	if callResult.IsError() {
		span.SetStatus(codes.Error, callResult.GetContent())
	}

	progPostHook := progressForStage(toolUse, "running post-hooks", 90, state.trace.Metadata)
	progressUpdates = append(progressUpdates, progPostHook)
	o.emitProgress(req, progPostHook)

	extraMessages = append(extraMessages, runtimehooks.ExecutePost(ctx, o.hooks.Post(), runtimehooks.ToolHookInput{
		ToolName:  toolUse.Name,
		ToolUseID: toolUse.ID,
		Input:     cloneToolInput(state.trace.FinalInput),
		ToolCtx:   state.toolCtx,
	})...)

	callResult = o.formatAndTruncateResult(t, callResult)
	if browserProgress := browserProgressForResult(toolUse, callResult); browserProgress != nil {
		for k, v := range state.trace.Metadata {
			browserProgress.Metadata[k] = v
		}
		progressUpdates = append(progressUpdates, *browserProgress)
		o.emitProgress(req, *browserProgress)
	}

	progComp := completeProgress(toolUse, callResult, state.trace.Metadata)
	progressUpdates = append(progressUpdates, progComp)
	o.emitProgress(req, progComp)

	return toolExecutionOutcome{
		ToolUse:  toolUse,
		Index:    index,
		Result:   callResult,
		Messages: extraMessages,
		Progress: progressUpdates,
		Trace:    cloneTrace(state.trace),
	}
}

func (o *Orchestrator) callToolSafe(
	ctx context.Context,
	state toolRuntimeState,
	permissionCheck types.CanUseToolFn,
) (callRes tool.CallResult) {
	defer func() {
		if r := recover(); r != nil {
			callRes = tool.NewErrorResult(fmt.Errorf("tool panic: %v", r))
		}
	}()
	// The tool may start sub-agents or background tasks: tell them which session
	// they belong to, so they are recorded as its children.
	ctx = types.WithCallingSessionID(ctx, state.callInput.SessionID)
	started := time.Now()
	defer func() { stampCallTiming(&callRes, started, time.Now()) }()
	result, err := state.tool.Call(ctx, state.callInput, permissionCheck)
	if err != nil {
		return tool.NewErrorResult(err)
	}
	return result
}

// stampCallTiming records when a call started and, when the tool did not say,
// how long it took. A host uses them to tell what a call did from what it only
// read: a pull request GitHub says was created inside that window was opened
// by this call, one created before it was only looked at. The start goes in
// Additional, which every result path (the session history, the host stream)
// already carries.
func stampCallTiming(result *tool.CallResult, started, ended time.Time) {
	if result.Metadata == nil {
		result.Metadata = &tool.ResultMetadata{}
	}
	if result.Metadata.Additional == nil {
		result.Metadata.Additional = map[string]any{}
	}
	if _, set := result.Metadata.Additional[ExecutionStartedAtKey]; !set {
		result.Metadata.Additional[ExecutionStartedAtKey] = started.UTC().Format(time.RFC3339Nano)
	}
	if result.Metadata.ExecutionDuration <= 0 {
		result.Metadata.ExecutionDuration = ended.Sub(started).Milliseconds()
	}
}

// ExecutionStartedAtKey is the metadata key of the start of a tool call (RFC 3339, UTC).
const ExecutionStartedAtKey = "execution_started_at"

func (o *Orchestrator) formatAndTruncateResult(t tool.Tool, result tool.CallResult) tool.CallResult {
	if result.Content == "" && result.Data != nil {
		result.Content = t.FormatResult(result.Data)
	}

	maxSize := t.Definition().MaxResultSize
	if maxSize > 0 && len(result.Content) > maxSize {
		original := result.Content
		truncated := truncateUTF8Safe(original, maxSize)
		result.Content = truncated + "\n\n... [truncated: original " +
			fmt.Sprintf("%d", len(original)) + " chars exceeded limit of " +
			fmt.Sprintf("%d", maxSize) + "]"

		// Preserve whatever the tool already put in Metadata (e.g. bash's
		// ExecutionDuration/exit_code/sandboxed flag in Additional) - this
		// used to allocate a fresh ResultMetadata here, silently discarding
		// all of it the moment a result got truncated.
		if result.Metadata == nil {
			result.Metadata = &tool.ResultMetadata{}
		}
		result.Metadata.ContentReplacement = &types.ContentReplacementState{
			OriginalSize:    int64(len(original)),
			ReplacedSize:    int64(len(result.Content)),
			ReplacementType: types.ContentReplacementTypeTruncated,
			Preview:         truncateUTF8Safe(original, min(maxSize, 200)),
		}
	}

	return result
}

// truncateUTF8Safe returns the longest prefix of s that is at most maxBytes
// bytes AND valid UTF-8 - a plain s[:maxBytes] byte slice can land inside a
// multi-byte rune (French/Arabic/Chinese/Japanese content, emoji, etc.),
// producing a string with a mangled character at the cut point.
func truncateUTF8Safe(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
