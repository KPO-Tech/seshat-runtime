package access

import (
	"fmt"
	"github.com/KPO-Tech/seshat-runtime/internal/sandbox"
	"strings"
)

const (
	MetadataRequestKey = "sandbox_request"
	MetadataPreviewKey = "sandbox_preview"
)

// ApprovalScope controls how long an approval grant should remain valid.
type ApprovalScope string

const (
	ApprovalScopeToolCall ApprovalScope = "tool_call"
	ApprovalScopeTurn     ApprovalScope = "turn"
	ApprovalScopeSession  ApprovalScope = "session"
)

// PermissionRequest is the normalized request emitted by tools and runtimes
// when they need a sandbox/approval decision.
type PermissionRequest struct {
	ToolName       string
	Description    string
	Environment    sandbox.EnvironmentKind
	Access         AccessKind
	Command        string
	Paths          []string
	NetworkTargets []string
	Justification  string
	Scope          ApprovalScope
	Metadata       map[string]any
}

// Validate ensures a permission request is structurally usable by the approval pipeline.
func (r PermissionRequest) Validate() error {
	if strings.TrimSpace(r.ToolName) == "" {
		return fmt.Errorf("tool name is required")
	}
	if r.Access == "" {
		return fmt.Errorf("access kind is required")
	}
	if r.Scope == "" {
		return fmt.Errorf("approval scope is required")
	}
	if strings.TrimSpace(r.Command) == "" && len(r.Paths) == 0 && len(r.NetworkTargets) == 0 {
		return fmt.Errorf("permission request must describe at least one command, path, or network target")
	}
	return nil
}

// DescriptionText returns a stable approval-friendly description.
func (r PermissionRequest) DescriptionText() string {
	if desc := strings.TrimSpace(r.Description); desc != "" {
		return desc
	}
	if command := strings.TrimSpace(r.Command); command != "" {
		return fmt.Sprintf("Execute command: %s", command)
	}
	if len(r.Paths) == 1 {
		return fmt.Sprintf("Access path: %s", r.Paths[0])
	}
	if len(r.Paths) > 1 {
		return fmt.Sprintf("Access %d paths", len(r.Paths))
	}
	if len(r.NetworkTargets) == 1 {
		return fmt.Sprintf("Access network target: %s", r.NetworkTargets[0])
	}
	if len(r.NetworkTargets) > 1 {
		return fmt.Sprintf("Access %d network targets", len(r.NetworkTargets))
	}
	return fmt.Sprintf("Permission request for tool %s", r.ToolName)
}

// MetadataMap returns stable metadata for the shared permission pipeline.
func (r PermissionRequest) MetadataMap() map[string]any {
	metadata := make(map[string]any, len(r.Metadata)+2)
	for k, v := range r.Metadata {
		metadata[k] = v
	}
	metadata[MetadataRequestKey] = r
	metadata[MetadataPreviewKey] = BuildPreview(r)
	return metadata
}

// PermissionDecision is the normalized response returned by the sandbox/approval layer.
type PermissionDecision struct {
	Decision      Decision
	Reason        string
	Scope         ApprovalScope
	ApprovedPaths []string
	Metadata      map[string]any
}

// IsAllowed reports whether the request was approved.
func (d PermissionDecision) IsAllowed() bool {
	return d.Decision == DecisionAllow
}
