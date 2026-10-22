package execution

import (
	"fmt"
	"os"
	"path/filepath"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	"github.com/KPO-Tech/seshat-runtime/internal/workspace"
)

func (o *Orchestrator) newToolContext(req ExecuteRequest) tool.ToolUseContext {
	permissionMode := types.PermissionModeOnRequest
	if req.PermissionContext != nil && req.PermissionContext.Mode != "" {
		permissionMode = req.PermissionContext.Mode
	}
	executionMode := ""
	if req.PermissionContext != nil && req.PermissionContext.ExecutionMode != "" {
		executionMode = req.PermissionContext.ExecutionMode
	}
	workingDir := resolveWorkingDirectory(req.WorkingDirectory)
	toolCtx := tool.NewToolUseContext(req.SessionID, req.TurnID, "", permissionMode)
	toolCtx.WorkingDirectory = workingDir
	if ws, err := workspace.New(workingDir); err == nil {
		toolCtx.Workspace = ws
	}
	toolCtx.ExecutionMode = executionMode
	if req.PermissionContext != nil {
		toolCtx.PrePlanMode = req.PermissionContext.PrePlanMode
		toolCtx.IsBypassPermissionsModeAvailable = req.PermissionContext.IsBypassPermissionsModeAvailable
		toolCtx.IsAutoModeAvailable = req.PermissionContext.IsAutoModeAvailable
	}

	if permissionMode == types.PermissionModeBypass {
		toolCtx.IsBypassPermissionsModeAvailable = true
	}

	return toolCtx
}

func resolveWorkingDirectory(configured string) string {
	if configured != "" {
		if abs, err := filepath.Abs(filepath.Clean(configured)); err == nil {
			return abs
		}
		return filepath.Clean(configured)
	}
	workingDir, err := os.Getwd()
	if err != nil || workingDir == "" {
		return "."
	}
	if abs, err := filepath.Abs(filepath.Clean(workingDir)); err == nil {
		return abs
	}
	return workingDir
}

func (o *Orchestrator) applyContextModifier(current tool.ToolUseContext, result tool.CallResult) tool.ToolUseContext {
	if result.ContextModifier == nil || result.IsError() {
		return current
	}
	return result.ContextModifier(current)
}

func (o *Orchestrator) buildToolResultMessages(toolUse types.ToolUseContent, result tool.CallResult, turnID types.TurnID) []types.Message {
	messages := make([]types.Message, 0, 1+len(result.NewMessages))
	metadata := toolResultMetadata(toolUse, result, nil)
	toolResultMessage := types.UserMessage(
		fmt.Sprintf("msg-%s-result", toolUse.ID),
		"",
	)
	toolResultMessage.Content = []types.ContentBlock{types.ToolResultContent{
		ToolUseID: toolUse.ID,
		Content:   o.resultContent(result),
		IsError:   result.IsError(),
		Metadata:  &metadata,
	}}
	toolResultMessage.Metadata = &types.MessageMetadata{
		TurnID: turnID.String(),
	}
	messages = append(messages, toolResultMessage)
	messages = append(messages, result.NewMessages...)
	return messages
}

func (o *Orchestrator) resultContent(result tool.CallResult) string {
	if result.GetContent() != "" {
		return result.GetContent()
	}
	return fmt.Sprintf("%v", result.GetData())
}

func normalizeExecuteRequest(req ExecuteRequest) ExecuteRequest {
	req.WorkingDirectory = resolveWorkingDirectory(req.WorkingDirectory)
	if req.PermissionContext == nil {
		req.PermissionContext = &types.PermissionContext{
			Mode:                             req.PermissionMode,
			IsBypassPermissionsModeAvailable: req.PermissionMode == types.PermissionModeBypass,
			IsAutoModeAvailable:              req.PermissionMode == types.PermissionModeAuto,
		}
	}
	// The request owns its own copy: tools run side by side and share the caller's context, which they would otherwise all write to.
	permissionContext := *req.PermissionContext
	req.PermissionContext = &permissionContext
	if req.PermissionContext.Mode == "" {
		req.PermissionContext.Mode = req.PermissionMode
	}
	if req.PermissionContext.Mode == "" {
		req.PermissionContext.Mode = types.PermissionModeOnRequest
	}
	if req.PermissionContext.Mode == types.PermissionModeBypass {
		req.PermissionContext.IsBypassPermissionsModeAvailable = true
	}
	req.PermissionContext.NormalizeLegacyPlanMode()
	req.PermissionMode = req.PermissionContext.Mode
	return req
}
