package access

import "strings"

// ToolAccessPreview is the normalized human-facing summary of what a tool is asking to do.
type ToolAccessPreview struct {
	ToolName       string   `json:"tool_name"`
	Environment    string   `json:"environment,omitempty"`
	Access         string   `json:"access,omitempty"`
	Command        string   `json:"command,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	NetworkTargets []string `json:"network_targets,omitempty"`
	Justification  string   `json:"justification,omitempty"`
}

// BuildPreview converts a permission request into a stable preview payload.
func BuildPreview(req PermissionRequest) ToolAccessPreview {
	return ToolAccessPreview{
		ToolName:       req.ToolName,
		Environment:    string(req.Environment),
		Access:         string(req.Access),
		Command:        strings.TrimSpace(req.Command),
		Paths:          append([]string(nil), req.Paths...),
		NetworkTargets: append([]string(nil), req.NetworkTargets...),
		Justification:  strings.TrimSpace(req.Justification),
	}
}
