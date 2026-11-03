package requestpermissions

import (
	"context"
	"fmt"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions/access"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ToolName is the canonical name of this tool.
const ToolName = "request_permissions"

// Description is the model-facing description of this tool.
const Description = `Requests additional permissions before performing an operation that exceeds the current authorization.

Use this tool when you need to:
- Read or write to paths outside the current workspace
- Access network resources that are not yet permitted
- Perform file system operations (create, delete, move) that require explicit user consent
- Execute commands that would normally require approval

## When to use

Call this tool BEFORE the operation that needs the permission, not after a failure.
Be specific: list the exact paths or network targets needed.
Provide a clear reason that explains WHY the permission is necessary.

## Scope

- ` + "`turn`" + ` (default): permission is valid for the remainder of this turn only.
- ` + "`session`" + `: permission persists for the entire session.

## Handling denial

If the user denies the request (` + "`granted: false`" + `), you must find a safe alternative or inform the user that the task cannot be completed without the requested permissions. Do NOT retry the same request.

## Examples

Good reason: "Need to read ~/.ssh/config to verify the SSH key used by the deployment script."
Bad reason: "Need more permissions."

Good reason: "Writing build artifacts to /tmp/seshat-build/ to avoid polluting the workspace."
Bad reason: "Write access needed."
`

// SearchHint is used by tool_search for ranking.
const SearchHint = "request additional permissions for file system or network access"

// PermissionGrantScope controls how long a granted permission remains valid.
type PermissionGrantScope string

const (
	GrantScopeTurn    PermissionGrantScope = "turn"
	GrantScopeSession PermissionGrantScope = "session"
)

// FilesystemPermissions describes the filesystem access being requested.
type FilesystemPermissions struct {
	Paths  []string `json:"paths"`
	Access []string `json:"access"`
}

// NetworkPermissions describes the network access being requested.
type NetworkPermissions struct {
	Targets []string `json:"targets,omitempty"`
	Enabled bool     `json:"enabled,omitempty"`
}

// RequestedPermissions is the top-level permissions payload.
type RequestedPermissions struct {
	Filesystem *FilesystemPermissions `json:"filesystem,omitempty"`
	Network    *NetworkPermissions    `json:"network,omitempty"`
}

// Input is the parsed input to request_permissions.
type Input struct {
	Reason      string               `json:"reason"`
	Permissions RequestedPermissions `json:"permissions"`
	Scope       PermissionGrantScope `json:"scope"`
}

// GrantedResponse is returned when the user approves the request.
type GrantedResponse struct {
	Granted     bool                 `json:"granted"`
	Scope       PermissionGrantScope `json:"scope"`
	Permissions RequestedPermissions `json:"permissions"`
}

// DeniedResponse is returned when the user denies the request.
type DeniedResponse struct {
	Granted bool   `json:"granted"`
	Reason  string `json:"reason"`
}

// Tool implements the request_permissions tool.
type Tool struct{}

// NewTool creates a new request_permissions tool.
func NewTool() *Tool { return &Tool{} }

// Definition returns the tool definition.
func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		DisplayName: "RequestPermissions",
		SearchHint:  SearchHint,
		Description: Description,
		Category:    "permissions",
		InputSchema: schema.FromMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Why these permissions are needed. Be specific and concise.",
				},
				"permissions": map[string]any{
					"type":        "object",
					"description": "The permissions being requested. Must include at least one of 'filesystem' or 'network'.",
					"properties": map[string]any{
						"filesystem": map[string]any{
							"type":        "object",
							"description": "Filesystem permission request.",
							"properties": map[string]any{
								"paths": map[string]any{
									"type":        "array",
									"description": "Absolute paths that need to be accessed.",
									"items":       map[string]any{"type": "string"},
								},
								"access": map[string]any{
									"type":        "array",
									"description": "Access kinds needed: read, write, create, delete, search.",
									"items": map[string]any{
										"type": "string",
										"enum": []string{"read", "write", "create", "delete", "search"},
									},
								},
							},
							"required": []string{"paths", "access"},
						},
						"network": map[string]any{
							"type":        "object",
							"description": "Network permission request.",
							"properties": map[string]any{
								"targets": map[string]any{
									"type":        "array",
									"description": "Hostnames or URLs that need to be accessed.",
									"items":       map[string]any{"type": "string"},
								},
								"enabled": map[string]any{
									"type":        "boolean",
									"description": "Request general network access.",
								},
							},
						},
					},
				},
				"scope": map[string]any{
					"type":        "string",
					"description": "How long the permission should remain valid: 'turn' (default) or 'session'.",
					"enum":        []string{"turn", "session"},
					"default":     "turn",
				},
			},
			"required": []string{"reason", "permissions"},
		}),
		IsReadOnly:         false,
		IsConcurrencySafe:  false,
		RequiresPermission: true,
	}
}

// RequiresUserInteraction ensures this tool is never auto-approved in bypass mode.
func (t *Tool) RequiresUserInteraction() bool { return true }

// IsEnabled reports that this tool is always active.
func (t *Tool) IsEnabled() bool { return true }

// IsConcurrencySafe reports that permission requests must run serially.
func (t *Tool) IsConcurrencySafe(_ map[string]any) bool { return false }

// IsReadOnly reports that permission requests have side-effects (they change the session grant state).
func (t *Tool) IsReadOnly(_ map[string]any) bool { return false }

// Description returns the tool description.
func (t *Tool) Description(_ context.Context) (string, error) { return Description, nil }

// FormatResult serialises the tool output.
func (t *Tool) FormatResult(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", data)
}

// BackfillInput returns input unchanged.
func (t *Tool) BackfillInput(_ context.Context, input map[string]any) map[string]any { return input }

// ValidateInput validates the tool input.
func (t *Tool) ValidateInput(_ context.Context, input map[string]any) (map[string]any, error) {
	parsed, err := parseInput(input)
	if err != nil {
		return nil, err
	}
	if err := parsed.validate(); err != nil {
		return nil, err
	}
	return input, nil
}

// CheckPermissions always delegates to the global pipeline — the approval
// happens inside Call via ResolveToolPermission.
func (t *Tool) CheckPermissions(_ context.Context, input map[string]any, _ tool.ToolUseContext) types.PermissionResult {
	return types.Passthrough(input)
}

// Call executes the permission request through the approval pipeline.
// A user denial returns a structured JSON response (not an error) so the model
// can handle it gracefully and propose a safe alternative.
func (t *Tool) Call(
	ctx context.Context,
	input tool.CallInput,
	permissionCheck types.CanUseToolFn,
) (tool.CallResult, error) {
	parsed, err := parseInput(input.Parsed)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("invalid input: %w", err)), nil
	}
	if err := parsed.validate(); err != nil {
		return tool.NewErrorResult(fmt.Errorf("invalid input: %w", err)), nil
	}

	toolCtx := input.ToolContextValue()

	req := access.PermissionRequest{
		ToolName:       ToolName,
		Description:    buildDescription(parsed),
		Environment:    sandbox.EnvironmentLocal,
		Access:         access.AccessEscalate,
		Paths:          collectPaths(parsed),
		NetworkTargets: collectNetworkTargets(parsed),
		Justification:  parsed.Reason,
		Scope:          mapScope(parsed.Scope),
		Metadata: map[string]any{
			"requested_permissions": parsed.Permissions,
			"grant_scope":           string(parsed.Scope),
		},
	}

	opts := access.ToolPermissionOptions{
		ToolUseID:              toolCtx.ToolUseID,
		SessionID:              toolCtx.SessionID,
		TurnID:                 toolCtx.TurnID,
		PermissionMode:         toolCtx.PermissionMode,
		WorkingDirectory:       strings.TrimSpace(toolCtx.WorkingDirectory),
		IsToolRunningInSandbox: toolCtx.EnableSandbox,
	}

	result, err := access.ResolveToolPermission(ctx, permissionCheck, req, opts)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("permission resolution failed: %w", err)), nil
	}

	if result.Behavior == types.PermissionBehaviorAllow {
		resp := GrantedResponse{
			Granted:     true,
			Scope:       parsed.Scope,
			Permissions: parsed.Permissions,
		}
		callResult := tool.NewJSONResult(resp)
		callResult.Content = formatGranted(parsed)
		return callResult, nil
	}

	reason := result.Reason
	if reason == "" {
		reason = "User denied the permission request."
	}
	resp := DeniedResponse{
		Granted: false,
		Reason:  reason,
	}
	callResult := tool.NewJSONResult(resp)
	callResult.Content = fmt.Sprintf("Permission request denied: %s", reason)
	return callResult, nil
}

// parseInput converts raw map input to a typed Input.
func parseInput(parsed map[string]any) (*Input, error) {
	reason, _ := parsed["reason"].(string)

	var perms RequestedPermissions
	rawPerms, ok := parsed["permissions"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("permissions is required and must be an object")
	}

	if rawFS, ok := rawPerms["filesystem"].(map[string]any); ok {
		fs := &FilesystemPermissions{}
		if rawPaths, ok := rawFS["paths"].([]any); ok {
			for _, p := range rawPaths {
				if s, ok := p.(string); ok {
					fs.Paths = append(fs.Paths, s)
				}
			}
		}
		if rawAccess, ok := rawFS["access"].([]any); ok {
			for _, a := range rawAccess {
				if s, ok := a.(string); ok {
					fs.Access = append(fs.Access, s)
				}
			}
		}
		perms.Filesystem = fs
	}

	if rawNet, ok := rawPerms["network"].(map[string]any); ok {
		net := &NetworkPermissions{}
		if rawTargets, ok := rawNet["targets"].([]any); ok {
			for _, target := range rawTargets {
				if s, ok := target.(string); ok {
					net.Targets = append(net.Targets, s)
				}
			}
		}
		if enabled, ok := rawNet["enabled"].(bool); ok {
			net.Enabled = enabled
		}
		perms.Network = net
	}

	scope := GrantScopeTurn
	if rawScope, ok := parsed["scope"].(string); ok && rawScope == "session" {
		scope = GrantScopeSession
	}

	return &Input{
		Reason:      reason,
		Permissions: perms,
		Scope:       scope,
	}, nil
}

// validate returns an error if the input is not usable.
func (in *Input) validate() error {
	if strings.TrimSpace(in.Reason) == "" {
		return fmt.Errorf("reason is required")
	}
	if in.Permissions.Filesystem == nil && in.Permissions.Network == nil {
		return fmt.Errorf("at least one of 'filesystem' or 'network' permissions must be specified")
	}
	if fs := in.Permissions.Filesystem; fs != nil {
		if len(fs.Paths) == 0 {
			return fmt.Errorf("filesystem.paths must not be empty")
		}
		if len(fs.Access) == 0 {
			return fmt.Errorf("filesystem.access must not be empty")
		}
	}
	if net := in.Permissions.Network; net != nil {
		if len(net.Targets) == 0 && !net.Enabled {
			return fmt.Errorf("network must specify targets or set enabled to true")
		}
	}
	return nil
}

// buildDescription returns a human-readable approval prompt description.
func buildDescription(in *Input) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Permission escalation request — reason: %s", in.Reason))

	if fs := in.Permissions.Filesystem; fs != nil {
		b.WriteString(fmt.Sprintf("\nFilesystem: %s on %s",
			strings.Join(fs.Access, ", "),
			strings.Join(fs.Paths, ", ")))
	}
	if net := in.Permissions.Network; net != nil {
		if len(net.Targets) > 0 {
			b.WriteString(fmt.Sprintf("\nNetwork: access to %s", strings.Join(net.Targets, ", ")))
		} else if net.Enabled {
			b.WriteString("\nNetwork: general network access")
		}
	}

	scopeLabel := "this turn"
	if in.Scope == GrantScopeSession {
		scopeLabel = "this session"
	}
	b.WriteString(fmt.Sprintf("\nScope: %s", scopeLabel))
	return b.String()
}

// formatGranted returns a human-readable granted message.
func formatGranted(in *Input) string {
	scopeLabel := "this turn"
	if in.Scope == GrantScopeSession {
		scopeLabel = "this session"
	}
	return fmt.Sprintf("Permission granted for %s. You may now proceed with the requested operation.", scopeLabel)
}

// collectPaths extracts all filesystem paths from the request.
func collectPaths(in *Input) []string {
	if in.Permissions.Filesystem == nil {
		return nil
	}
	return append([]string(nil), in.Permissions.Filesystem.Paths...)
}

// collectNetworkTargets extracts all network targets from the request.
func collectNetworkTargets(in *Input) []string {
	if in.Permissions.Network == nil {
		return nil
	}
	return append([]string(nil), in.Permissions.Network.Targets...)
}

// mapScope converts a PermissionGrantScope to a sandbox ApprovalScope.
func mapScope(scope PermissionGrantScope) access.ApprovalScope {
	if scope == GrantScopeSession {
		return access.ApprovalScopeSession
	}
	return access.ApprovalScopeTurn
}
