package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
)

// ─── schedule_job ─────────────────────────────────────────────────────────────

// NewScheduleJobTool returns the schedule_job tool.
func NewScheduleJobTool(cfg Config) (tool.Tool, error) {
	c := newDaemonClient(cfg)

	return tool.NewBuilder(ToolNameScheduleJob).
		WithDisplayName("ScheduleJob").
		WithSearchHint("schedule a recurring or one-time job cron timer automation").
		WithCategory("automation").
		WithDescription(ToolDescScheduleJob).
		WithInputSchema(schema.JSONSchema{
			Type: "object",
			Properties: map[string]schema.JSONSchema{
				"name": {
					Type:        "string",
					Description: "Short name for the job (e.g. 'Daily standup summary')",
				},
				"description": {
					Type:        "string",
					Description: "Optional longer description of what the job does",
				},
				"trigger_type": {
					Type:        "string",
					Enum:        []string{"cron", "interval", "once"},
					Description: "How the job is triggered: 'cron', 'interval', or 'once'",
				},
				"cron": {
					Type:        "string",
					Description: "5-field cron expression (required when trigger_type=cron)",
				},
				"interval": {
					Type:        "string",
					Description: "Go duration string (required when trigger_type=interval, e.g. '24h', '30m')",
				},
				"run_at": {
					Type:        "string",
					Description: "RFC3339 datetime (required when trigger_type=once, e.g. '2026-07-01T09:00:00Z')",
				},
				"task": {
					Type:        "string",
					Description: "The prompt/task the agent will execute on each run",
				},
				"agent_slug": {
					Type:        "string",
					Description: "Slug of a named agent definition in seshat-ai (e.g. 'accounting-agent'). When set, the agent's model, tools, and system prompt are resolved from the definition; inline fields override them.",
				},
				"agent_model": {
					Type:        "string",
					Description: "Model to use for the agent (optional, overrides agent definition if agent_slug is set)",
				},
				"agent_tools": {
					Type:        "array",
					Description: "List of tool names the agent may use (overrides agent definition if agent_slug is set)",
					Items:       &schema.JSONSchema{Type: "string"},
				},
				"agent_max_turns": {
					Type:        "integer",
					Description: "Maximum agent turns per run (optional, overrides agent definition if agent_slug is set)",
				},
				"agent_system_prompt": {
					Type:        "string",
					Description: "Custom system prompt for the agent (optional, overrides agent definition if agent_slug is set)",
				},
			},
			Required: []string{"name", "trigger_type", "task"},
		}).
		WithHandler(func(ctx context.Context, input tool.CallInput, _ tool.ToolUseContext) (tool.CallResult, error) {
			if !cfg.available() {
				return tool.NewErrorResult(errors.New(errNotConfigured)), nil
			}
			p := input.Parsed
			if p == nil {
				p = map[string]any{}
			}

			name, _ := p["name"].(string)
			triggerType, _ := p["trigger_type"].(string)
			task, _ := p["task"].(string)

			body := map[string]any{
				"name":        name,
				"description": strVal(p, "description"),
				"task":        task,
				"trigger": buildTrigger(triggerType,
					strVal(p, "cron"),
					strVal(p, "interval"),
					strVal(p, "run_at"),
				),
				"agent": buildAgent(p),
			}

			userID := userIDFromCtx(ctx)
			data, status, err := c.do(ctx, http.MethodPost, "/jobs", body, userID)
			if err != nil {
				return tool.NewErrorResult(err), nil
			}
			var resp map[string]any
			if err := parseResponse(data, status, &resp); err != nil {
				return tool.NewErrorResult(err), nil
			}
			return tool.CallResult{Data: resp}, nil
		}).
		Build()
}

// ─── list_jobs ────────────────────────────────────────────────────────────────

// NewListJobsTool returns the list_jobs tool.
func NewListJobsTool(cfg Config) (tool.Tool, error) {
	c := newDaemonClient(cfg)

	return tool.NewBuilder(ToolNameListJobs).
		WithDisplayName("ListJobs").
		WithSearchHint("list scheduled jobs automation").
		WithCategory("automation").
		WithDescription(ToolDescListJobs).
		ReadOnly().
		ConcurrencySafe().
		WithHandler(func(ctx context.Context, input tool.CallInput, _ tool.ToolUseContext) (tool.CallResult, error) {
			if !cfg.available() {
				return tool.NewErrorResult(errors.New(errNotConfigured)), nil
			}
			userID := userIDFromCtx(ctx)
			data, status, err := c.do(ctx, http.MethodGet, "/jobs", nil, userID)
			if err != nil {
				return tool.NewErrorResult(err), nil
			}
			var resp map[string]any
			if err := parseResponse(data, status, &resp); err != nil {
				return tool.NewErrorResult(err), nil
			}
			return tool.CallResult{Data: resp}, nil
		}).
		WithFormatResult(func(data any) string {
			if data == nil {
				return "No jobs found."
			}
			if m, ok := data.(map[string]any); ok {
				if jobs, ok := m["jobs"].([]any); ok {
					if len(jobs) == 0 {
						return "No automation jobs configured."
					}
					b, _ := json.MarshalIndent(jobs, "", "  ")
					return fmt.Sprintf("Found %d job(s):\n%s", len(jobs), string(b))
				}
			}
			return fmt.Sprintf("%v", data)
		}).
		Build()
}

// ─── update_job ───────────────────────────────────────────────────────────────

// NewUpdateJobTool returns the update_job tool.
func NewUpdateJobTool(cfg Config) (tool.Tool, error) {
	c := newDaemonClient(cfg)

	return tool.NewBuilder(ToolNameUpdateJob).
		WithDisplayName("UpdateJob").
		WithSearchHint("change the schedule or settings of a scheduled job").
		WithCategory("automation").
		WithDescription(ToolDescUpdateJob).
		WithInputSchema(schema.JSONSchema{
			Type: "object",
			Properties: map[string]schema.JSONSchema{
				"job_id":              {Type: "string", Description: "ID of the job to update"},
				"name":                {Type: "string", Description: "New name for the job"},
				"description":         {Type: "string", Description: "New description"},
				"task":                {Type: "string", Description: "New task prompt"},
				"trigger_type":        {Type: "string", Enum: []string{"cron", "interval", "once"}},
				"cron":                {Type: "string", Description: "New cron expression (when trigger_type=cron)"},
				"interval":            {Type: "string", Description: "New interval duration (when trigger_type=interval)"},
				"run_at":              {Type: "string", Description: "New run datetime (when trigger_type=once)"},
				"agent_slug":          {Type: "string", Description: "Named agent slug (resolves from seshat-ai agent definitions)"},
				"agent_model":         {Type: "string", Description: "New agent model"},
				"agent_tools":         {Type: "array", Items: &schema.JSONSchema{Type: "string"}},
				"agent_max_turns":     {Type: "integer"},
				"agent_system_prompt": {Type: "string"},
			},
			Required: []string{"job_id"},
		}).
		WithHandler(func(ctx context.Context, input tool.CallInput, _ tool.ToolUseContext) (tool.CallResult, error) {
			if !cfg.available() {
				return tool.NewErrorResult(errors.New(errNotConfigured)), nil
			}
			p := input.Parsed
			if p == nil {
				p = map[string]any{}
			}

			jobID, _ := p["job_id"].(string)
			if jobID == "" {
				return tool.NewErrorResult(fmt.Errorf("job_id is required")), nil
			}

			body := map[string]any{}
			if v, ok := p["name"].(string); ok && v != "" {
				body["name"] = v
			}
			if v, ok := p["description"].(string); ok {
				body["description"] = v
			}
			if v, ok := p["task"].(string); ok && v != "" {
				body["task"] = v
			}
			if tt, _ := p["trigger_type"].(string); tt != "" {
				body["trigger"] = buildTrigger(tt, strVal(p, "cron"), strVal(p, "interval"), strVal(p, "run_at"))
			}
			if agentBody := buildAgent(p); len(agentBody) > 0 {
				body["agent"] = agentBody
			}

			userID := userIDFromCtx(ctx)
			data, status, err := c.do(ctx, http.MethodPut, "/jobs/"+jobID, body, userID)
			if err != nil {
				return tool.NewErrorResult(err), nil
			}
			var resp map[string]any
			if err := parseResponse(data, status, &resp); err != nil {
				return tool.NewErrorResult(err), nil
			}
			return tool.CallResult{Data: resp}, nil
		}).
		Build()
}

// ─── delete_job ───────────────────────────────────────────────────────────────

// NewDeleteJobTool returns the delete_job tool.
func NewDeleteJobTool(cfg Config) (tool.Tool, error) {
	c := newDaemonClient(cfg)

	return tool.NewBuilder(ToolNameDeleteJob).
		WithDisplayName("DeleteJob").
		WithSearchHint("delete a scheduled job").
		WithCategory("automation").
		WithDescription(ToolDescDeleteJob).
		Destructive().
		WithInputSchema(schema.JSONSchema{
			Type:       "object",
			Properties: map[string]schema.JSONSchema{"job_id": {Type: "string", Description: "ID of the job to delete"}},
			Required:   []string{"job_id"},
		}).
		WithHandler(func(ctx context.Context, input tool.CallInput, _ tool.ToolUseContext) (tool.CallResult, error) {
			if !cfg.available() {
				return tool.NewErrorResult(errors.New(errNotConfigured)), nil
			}
			p := input.Parsed
			if p == nil {
				p = map[string]any{}
			}
			jobID, _ := p["job_id"].(string)
			if jobID == "" {
				return tool.NewErrorResult(fmt.Errorf("job_id is required")), nil
			}
			userID := userIDFromCtx(ctx)
			data, status, err := c.do(ctx, http.MethodDelete, "/jobs/"+jobID, nil, userID)
			if err != nil {
				return tool.NewErrorResult(err), nil
			}
			if err := parseResponse(data, status, nil); err != nil {
				return tool.NewErrorResult(err), nil
			}
			return tool.NewTextResult(fmt.Sprintf("Job %s deleted successfully.", jobID)), nil
		}).
		Build()
}

// ─── pause_job ────────────────────────────────────────────────────────────────

// NewPauseJobTool returns the pause_job tool.
func NewPauseJobTool(cfg Config) (tool.Tool, error) {
	c := newDaemonClient(cfg)

	return tool.NewBuilder(ToolNamePauseJob).
		WithDisplayName("PauseJob").
		WithSearchHint("pause a scheduled job").
		WithCategory("automation").
		WithDescription(ToolDescPauseJob).
		WithInputSchema(schema.JSONSchema{
			Type:       "object",
			Properties: map[string]schema.JSONSchema{"job_id": {Type: "string", Description: "ID of the job to pause"}},
			Required:   []string{"job_id"},
		}).
		WithHandler(func(ctx context.Context, input tool.CallInput, _ tool.ToolUseContext) (tool.CallResult, error) {
			if !cfg.available() {
				return tool.NewErrorResult(errors.New(errNotConfigured)), nil
			}
			p := input.Parsed
			if p == nil {
				p = map[string]any{}
			}
			jobID, _ := p["job_id"].(string)
			if jobID == "" {
				return tool.NewErrorResult(fmt.Errorf("job_id is required")), nil
			}
			userID := userIDFromCtx(ctx)
			data, status, err := c.do(ctx, http.MethodPost, "/jobs/"+jobID+"/pause", nil, userID)
			if err != nil {
				return tool.NewErrorResult(err), nil
			}
			var resp map[string]any
			if err := parseResponse(data, status, &resp); err != nil {
				return tool.NewErrorResult(err), nil
			}
			return tool.NewTextResult(fmt.Sprintf("Job %s paused.", jobID)), nil
		}).
		Build()
}

// ─── resume_job ───────────────────────────────────────────────────────────────

// NewResumeJobTool returns the resume_job tool.
func NewResumeJobTool(cfg Config) (tool.Tool, error) {
	c := newDaemonClient(cfg)

	return tool.NewBuilder(ToolNameResumeJob).
		WithDisplayName("ResumeJob").
		WithSearchHint("resume a paused scheduled job").
		WithCategory("automation").
		WithDescription(ToolDescResumeJob).
		WithInputSchema(schema.JSONSchema{
			Type:       "object",
			Properties: map[string]schema.JSONSchema{"job_id": {Type: "string", Description: "ID of the job to resume"}},
			Required:   []string{"job_id"},
		}).
		WithHandler(func(ctx context.Context, input tool.CallInput, _ tool.ToolUseContext) (tool.CallResult, error) {
			if !cfg.available() {
				return tool.NewErrorResult(errors.New(errNotConfigured)), nil
			}
			p := input.Parsed
			if p == nil {
				p = map[string]any{}
			}
			jobID, _ := p["job_id"].(string)
			if jobID == "" {
				return tool.NewErrorResult(fmt.Errorf("job_id is required")), nil
			}
			userID := userIDFromCtx(ctx)
			data, status, err := c.do(ctx, http.MethodPost, "/jobs/"+jobID+"/resume", nil, userID)
			if err != nil {
				return tool.NewErrorResult(err), nil
			}
			var resp map[string]any
			if err := parseResponse(data, status, &resp); err != nil {
				return tool.NewErrorResult(err), nil
			}
			return tool.NewTextResult(fmt.Sprintf("Job %s resumed.", jobID)), nil
		}).
		Build()
}

// ─── run_job_now ──────────────────────────────────────────────────────────────

// NewRunJobNowTool returns the run_job_now tool.
func NewRunJobNowTool(cfg Config) (tool.Tool, error) {
	c := newDaemonClient(cfg)

	return tool.NewBuilder(ToolNameRunJobNow).
		WithDisplayName("RunJobNow").
		WithSearchHint("run a scheduled job immediately").
		WithCategory("automation").
		WithDescription(ToolDescRunJobNow).
		WithInputSchema(schema.JSONSchema{
			Type:       "object",
			Properties: map[string]schema.JSONSchema{"job_id": {Type: "string", Description: "ID of the job to run immediately"}},
			Required:   []string{"job_id"},
		}).
		WithHandler(func(ctx context.Context, input tool.CallInput, _ tool.ToolUseContext) (tool.CallResult, error) {
			if !cfg.available() {
				return tool.NewErrorResult(errors.New(errNotConfigured)), nil
			}
			p := input.Parsed
			if p == nil {
				p = map[string]any{}
			}
			jobID, _ := p["job_id"].(string)
			if jobID == "" {
				return tool.NewErrorResult(fmt.Errorf("job_id is required")), nil
			}
			userID := userIDFromCtx(ctx)
			data, status, err := c.do(ctx, http.MethodPost, "/jobs/"+jobID+"/run", nil, userID)
			if err != nil {
				return tool.NewErrorResult(err), nil
			}
			var resp map[string]any
			if err := parseResponse(data, status, &resp); err != nil {
				return tool.NewErrorResult(err), nil
			}
			return tool.CallResult{Data: resp}, nil
		}).
		Build()
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func buildTrigger(triggerType, cron, interval, runAt string) map[string]any {
	t := map[string]any{"type": triggerType}
	if cron != "" {
		t["cron"] = cron
	}
	if interval != "" {
		t["interval"] = interval
	}
	if runAt != "" {
		t["run_at"] = runAt
	}
	return t
}

func buildAgent(p map[string]any) map[string]any {
	agent := map[string]any{}
	if v, ok := p["agent_slug"].(string); ok && v != "" {
		agent["slug"] = v
	}
	if v, ok := p["agent_model"].(string); ok && v != "" {
		agent["model"] = v
	}
	if v, ok := p["agent_tools"].([]any); ok && len(v) > 0 {
		tools := make([]string, 0, len(v))
		for _, t := range v {
			if s, ok := t.(string); ok {
				tools = append(tools, s)
			}
		}
		agent["tools"] = tools
	}
	if v, ok := p["agent_max_turns"].(float64); ok && v > 0 {
		agent["max_turns"] = int(v)
	}
	if v, ok := p["agent_system_prompt"].(string); ok && v != "" {
		agent["system_prompt"] = v
	}
	return agent
}
