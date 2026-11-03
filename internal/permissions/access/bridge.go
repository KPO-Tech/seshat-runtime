package access

import (
	"context"
	"fmt"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ToolPermissionOptions carries runtime-specific fields for the shared
// permission pipeline request.
type ToolPermissionOptions struct {
	ToolInput              map[string]any
	ToolUseID              string
	SessionID              types.SessionID
	TurnID                 types.TurnID
	PermissionMode         types.PermissionMode
	WorkingDirectory       string
	IsToolRunningInSandbox bool
	Stage                  types.ToolPermissionStage
	Intent                 types.ToolPermissionIntent
	Metadata               map[string]any
}

// BuildToolPermissionRequest converts a normalized sandbox request into the
// runtime permission request expected by the engine.
func BuildToolPermissionRequest(req PermissionRequest, opts ToolPermissionOptions) (types.ToolPermissionRequest, error) {
	if err := req.Validate(); err != nil {
		return types.ToolPermissionRequest{}, err
	}

	metadata := req.MetadataMap()
	for key, value := range opts.Metadata {
		metadata[key] = value
	}

	stage := opts.Stage
	if stage == "" {
		stage = types.ToolPermissionStageGlobal
	}
	intent := opts.Intent
	if intent == "" {
		intent = types.ToolPermissionIntentCheck
	}

	return types.ToolPermissionRequest{
		ToolName:               req.ToolName,
		Description:            req.DescriptionText(),
		ToolInput:              cloneMap(opts.ToolInput),
		ToolUseID:              strings.TrimSpace(opts.ToolUseID),
		SessionID:              opts.SessionID,
		TurnID:                 opts.TurnID,
		PermissionMode:         opts.PermissionMode,
		WorkingDirectory:       strings.TrimSpace(opts.WorkingDirectory),
		IsToolRunningInSandbox: opts.IsToolRunningInSandbox,
		Stage:                  stage,
		Intent:                 intent,
		Metadata:               metadata,
	}, nil
}

// ResolveToolPermission runs the normalized request through the active
// permission resolver.
func ResolveToolPermission(
	ctx context.Context,
	checker types.CanUseToolFn,
	req PermissionRequest,
	opts ToolPermissionOptions,
) (types.PermissionResult, error) {
	if checker == nil {
		return types.PermissionResult{}, fmt.Errorf("permission checker is required")
	}

	toolReq, err := BuildToolPermissionRequest(req, opts)
	if err != nil {
		return types.PermissionResult{}, err
	}

	return checker(ctx, toolReq), nil
}

func cloneMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
