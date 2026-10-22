package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	registry "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// streamedBlock is a tool call whose pieces are still arriving.
type streamedBlock struct {
	toolUse   types.ToolUseContent
	inputJSON strings.Builder
}

type streamingToolCoordinator struct {
	executor    *execution.StreamingExecutor
	tools       map[string]registry.Tool
	toolIndex   int
	currentTool *types.ToolUseContent
	inputJSON   strings.Builder
	submitted   map[int]types.ToolUseContent
	// indexed holds the tool calls of a provider that interleaves them (see types.APIResponseChunk.BlockIndex), by the index the
	// provider gave each. They are all submitted when the message stops, in index order, which is the order of the tool calls in the
	// message that is built from the same stream.
	indexed map[int]*streamedBlock
}

func newStreamingToolCoordinator(
	ctx context.Context,
	loop *Loop,
	req RunRequest,
	state *MutableState,
	transcript []types.Message,
) *streamingToolCoordinator {
	if loop == nil || loop.orchestrator == nil || loop.permissionIntegrator == nil || len(req.Tools) == 0 {
		return nil
	}

	execReq := loop.buildExecuteRequest(ctx, nil, req, state, transcript)
	return &streamingToolCoordinator{
		executor:  execution.NewStreamingExecutor(ctx, loop.orchestrator, execReq, req.Tools, execReq.PermissionCheck, tool.ToolUseContext{}),
		tools:     req.Tools,
		submitted: make(map[int]types.ToolUseContent),
	}
}

func (c *streamingToolCoordinator) observe(chunk types.APIResponseChunk) {
	switch chunk.Type {
	case types.APIChunkTypeContentBlockStart:
		c.startBlock(chunk)
	case types.APIChunkTypeContentBlockDelta:
		c.applyDelta(chunk)
	case types.APIChunkTypeContentBlockStop:
		c.finishCurrentTool()
	case types.APIChunkTypeMessageStop:
		c.finishCurrentTool()
		c.finishIndexedTools()
	}
}

func (c *streamingToolCoordinator) startBlock(chunk types.APIResponseChunk) {
	toolUse, isTool := chunk.ContentBlock.(types.ToolUseContent)
	if isTool && chunk.BlockIndex != nil {
		if c.indexed == nil {
			c.indexed = make(map[int]*streamedBlock)
		}
		if _, exists := c.indexed[*chunk.BlockIndex]; !exists {
			c.indexed[*chunk.BlockIndex] = &streamedBlock{toolUse: toolUse}
		}
		return
	}
	// A block that opens while another is still open closes it: a provider that does not send a stop between two tool calls must not
	// lose the first.
	c.finishCurrentTool()
	c.currentTool = nil
	c.inputJSON.Reset()
	if !isTool {
		return
	}
	cloned := toolUse
	c.currentTool = &cloned
}

func (c *streamingToolCoordinator) applyDelta(chunk types.APIResponseChunk) {
	if chunk.DeltaType != "input_json_delta" {
		return
	}
	if chunk.BlockIndex != nil {
		if block := c.indexed[*chunk.BlockIndex]; block != nil {
			block.inputJSON.WriteString(chunk.PartialJSON)
		}
		return
	}
	if c.currentTool == nil {
		return
	}
	c.inputJSON.WriteString(chunk.PartialJSON)
	if input, ok := parseStreamingToolInput(c.inputJSON.String()); ok {
		c.currentTool.Input = input
	}
}

// finishIndexedTools submits the tool calls that were streamed with indexes, in index order.
func (c *streamingToolCoordinator) finishIndexedTools() {
	if len(c.indexed) == 0 {
		return
	}
	order := make([]int, 0, len(c.indexed))
	for index := range c.indexed {
		order = append(order, index)
	}
	sort.Ints(order)
	for _, blockIndex := range order {
		block := c.indexed[blockIndex]
		toolUse := block.toolUse
		if block.inputJSON.Len() > 0 {
			if input, ok := parseStreamingToolInput(block.inputJSON.String()); ok {
				toolUse.Input = input
			}
		}
		c.submit(toolUse)
	}
	c.indexed = nil
}

func (c *streamingToolCoordinator) finishCurrentTool() {
	if c.currentTool == nil {
		return
	}

	toolUse := *c.currentTool
	c.currentTool = nil

	if c.inputJSON.Len() > 0 {
		if input, ok := parseStreamingToolInput(c.inputJSON.String()); ok {
			toolUse.Input = input
		}
	}
	c.inputJSON.Reset()
	c.submit(toolUse)
}

// submit gives a finished tool call its place in the order of the message and starts it when it may run before the stream ends.
func (c *streamingToolCoordinator) submit(toolUse types.ToolUseContent) {
	index := c.toolIndex
	c.toolIndex++
	if !c.canStreamExecute(toolUse) {
		return
	}
	c.submitted[index] = toolUse
	c.executor.SubmitToolUse(toolUse, index)
}

func (c *streamingToolCoordinator) canStreamExecute(toolUse types.ToolUseContent) bool {
	if c == nil || c.executor == nil || toolUse.Name == "" || toolUse.ID == "" {
		return false
	}
	t, ok := c.tools[toolUse.Name]
	if !ok || t == nil {
		return false
	}
	input := toolUse.Input
	return t.IsConcurrencySafe(input) && t.IsReadOnly(input)
}

// discard cancels any in-flight pre-streamed tool executions and waits for
// goroutines to finish. Safe to call on a nil coordinator.
func (c *streamingToolCoordinator) discard() {
	if c == nil || c.executor == nil {
		return
	}
	c.executor.Discard()
}

func (c *streamingToolCoordinator) complete(ctx context.Context) (map[int]execution.StreamingExecutionResult, error) {
	if c == nil || c.executor == nil || len(c.submitted) == 0 {
		return nil, nil
	}

	// Wait replaces the old 5 ms polling loop: it blocks on the WaitGroup and
	// calls Discard (writing canceled outcomes) if ctx is canceled.
	if err := c.executor.Wait(ctx); err != nil {
		return nil, err
	}

	results := make(map[int]execution.StreamingExecutionResult, len(c.submitted))
	for _, result := range c.executor.GetAllCompletedResults() {
		if _, ok := c.submitted[result.Index]; ok {
			results[result.Index] = result
		}
	}
	return results, nil
}

func (l *Loop) buildExecuteRequest(ctx context.Context, toolUses []types.ToolUseContent, req RunRequest, state *MutableState, transcript []types.Message) execution.ExecuteRequest {
	var permCtx *types.PermissionContext
	var permMode types.PermissionMode
	if state != nil {
		permCtx = state.PermissionContext
		permMode = state.PermissionMode
	} else {
		permCtx = req.PermissionContext
		permMode = req.PermissionMode
	}
	var permissionResolver types.PermissionResolver
	// No prompt function means this context cannot show an interactive ask
	// at all - the classifier (auto mode) and the PowerShell/request_permissions
	// fast paths use this to deny instead of asking into the void, same as the
	// no-prompt-fn safety net at resolution time already does for every other
	// mode. Default true (avoid prompts) when there is no integrator to ask.
	shouldAvoidPrompts := true
	if l.permissionIntegrator != nil {
		permissionResolver = l.permissionIntegrator.ResolverWithContext(req.SessionID, req.TurnID, permCtx, transcript)
		shouldAvoidPrompts = !l.permissionIntegrator.HasPromptFn(ctx)
	}
	permissionCheck := req.PermissionCheck
	if permissionCheck == nil && permissionResolver != nil {
		permissionCheck = types.CanUseToolFunc(permissionResolver)
	}
	return execution.ExecuteRequest{
		ToolUses:                     toolUses,
		Tools:                        req.Tools,
		PermissionCheck:              permissionCheck,
		PermissionResolver:           permissionResolver,
		PermissionContext:            clonePermissionContext(permCtx),
		SessionID:                    req.SessionID,
		TurnID:                       req.TurnID,
		WorkingDirectory:             req.WorkingDirectory,
		PermissionMode:               permMode,
		ProgressCallback:             req.ProgressCallback,
		DenialTracking:               req.DenialTracking,
		Transcript:                   transcript,
		ShouldAvoidPermissionPrompts: shouldAvoidPrompts,
	}
}

func (l *Loop) executeToolsForResponse(
	ctx context.Context,
	toolUses []types.ToolUseContent,
	req RunRequest,
	state *MutableState,
	transcript []types.Message,
) (execution.ExecuteResult, error) {
	coordinator := l.activeStreamingTools
	l.activeStreamingTools = nil
	if coordinator == nil {
		return l.executeTools(ctx, toolUses, req, state, transcript)
	}

	streamed, err := coordinator.complete(ctx)
	if err != nil {
		return execution.ExecuteResult{}, err
	}
	if len(streamed) == 0 {
		return l.executeTools(ctx, toolUses, req, state, transcript)
	}

	remainingToolUses := make([]types.ToolUseContent, 0, len(toolUses))
	remainingIndexes := make([]int, 0, len(toolUses))
	for idx, toolUse := range toolUses {
		if _, ok := streamed[idx]; ok {
			continue
		}
		remainingToolUses = append(remainingToolUses, toolUse)
		remainingIndexes = append(remainingIndexes, idx)
	}

	remainingResult, err := l.executeTools(ctx, remainingToolUses, req, state, transcript)
	if err != nil {
		return execution.ExecuteResult{}, err
	}

	return mergeToolExecutionResults(toolUses, req.TurnID, streamed, remainingIndexes, remainingResult), nil
}

func mergeToolExecutionResults(
	toolUses []types.ToolUseContent,
	turnID types.TurnID,
	streamed map[int]execution.StreamingExecutionResult,
	remainingIndexes []int,
	remaining execution.ExecuteResult,
) execution.ExecuteResult {
	total := len(toolUses)
	results := make([]tool.CallResult, total)
	traces := make([]execution.ToolExecutionTrace, total)
	progress := append([]types.ToolProgress(nil), remaining.ProgressUpdates...)
	errors := append([]execution.ExecutionError(nil), remaining.Errors...)
	messageBatches := make([][]types.Message, total)

	for idx, result := range streamed {
		results[idx] = result.Result
		traces[idx] = result.Trace
		progress = append(progress, result.Progress...)
		messageBatches[idx] = buildToolResultMessages(result.ToolUse, result.Result, turnID, result.Messages)
		if result.Error != nil {
			errors = append(errors, execution.ExecutionError{
				ToolUseID: result.ToolUse.ID,
				ToolName:  result.ToolUse.Name,
				Error:     result.Error,
				Stage:     result.ErrorStage,
			})
		}
	}

	remainingMessageBatches := splitToolMessageBatches(remaining.Messages)
	for localIdx, globalIdx := range remainingIndexes {
		if localIdx < len(remaining.Results) {
			results[globalIdx] = remaining.Results[localIdx]
		}
		if localIdx < len(remaining.Traces) {
			traces[globalIdx] = remaining.Traces[localIdx]
		}
		if localIdx < len(remainingMessageBatches) {
			messageBatches[globalIdx] = remainingMessageBatches[localIdx]
		}
	}

	messages := make([]types.Message, 0, len(remaining.Messages))
	for _, batch := range messageBatches {
		messages = append(messages, batch...)
	}

	finalToolContext := remaining.FinalToolContext
	if len(remaining.Results) == 0 {
		for idx := 0; idx < total; idx++ {
			if result, ok := streamed[idx]; ok {
				finalToolContext = result.FinalToolContext
			}
		}
	}

	return execution.ExecuteResult{
		Results:          results,
		Messages:         messages,
		Traces:           traces,
		Errors:           errors,
		TotalDuration:    remaining.TotalDuration,
		ProgressUpdates:  progress,
		FinalToolContext: finalToolContext,
	}
}

func splitToolMessageBatches(messages []types.Message) [][]types.Message {
	if len(messages) == 0 {
		return nil
	}

	batches := make([][]types.Message, 0)
	current := make([]types.Message, 0)
	for _, message := range messages {
		if isToolResultMessage(message) && len(current) > 0 {
			batches = append(batches, current)
			current = make([]types.Message, 0)
		}
		current = append(current, message)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func isToolResultMessage(message types.Message) bool {
	for _, block := range message.Content {
		if _, ok := block.(types.ToolResultContent); ok {
			return true
		}
	}
	return false
}

func buildToolResultMessages(toolUse types.ToolUseContent, result tool.CallResult, turnID types.TurnID, extraMessages []types.Message) []types.Message {
	metadata := map[string]any{"tool_name": toolUse.Name}
	if content := result.GetContent(); content != "" {
		metadata["content"] = content
	}
	if result.Metadata != nil {
		if result.Metadata.ExecutionDuration > 0 {
			metadata["execution_duration_ms"] = result.Metadata.ExecutionDuration
		}
		if result.Metadata.ContentReplacement != nil {
			metadata["content_replacement"] = result.Metadata.ContentReplacement
		}
		for key, value := range result.Metadata.Additional {
			metadata[key] = value
		}
	}
	content := resultContent(result)
	if sanitized, detected, reason := sanitizeToolResult(content); detected {
		content = sanitized
		metadata["security_injection_detected"] = true
		metadata["security_injection_reason"] = reason
	}

	toolResultMessage := types.UserMessage(fmt.Sprintf("msg-%s-result", toolUse.ID), "")
	toolResultMessage.Content = []types.ContentBlock{types.ToolResultContent{
		ToolUseID: toolUse.ID,
		Content:   content,
		IsError:   result.IsError(),
		Metadata:  &metadata,
	}}
	toolResultMessage.Metadata = &types.MessageMetadata{TurnID: turnID.String()}

	messages := make([]types.Message, 0, 1+len(result.NewMessages)+len(extraMessages))
	messages = append(messages, toolResultMessage)
	messages = append(messages, result.NewMessages...)
	messages = append(messages, extraMessages...)
	return messages
}

func resultContent(result tool.CallResult) string {
	if result.GetContent() != "" {
		return result.GetContent()
	}
	// Defense in depth for a CallResult built without NewJSONResult/
	// NewTextResult (which both now always populate Content) - JSON is
	// still parseable by the model and any frontend renderer, unlike Go's
	// %v-formatted struct/map dump this used to fall back to.
	if encoded, err := json.Marshal(result.GetData()); err == nil {
		return string(encoded)
	}
	return fmt.Sprintf("%v", result.GetData())
}

func parseStreamingToolInput(raw string) (map[string]any, bool) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, true
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, false
	}
	return input, true
}
