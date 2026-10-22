package builtin

// GeneralAgentToolNames returns the tools a general-purpose agent is offered:
// the ones almost every session needs. Everything else stays registered, so a
// specialised agent (data analysis, browsing, automation...) or a sub-agent can
// still use it; it just does not cost the general agent's context.
//
// The agent tools (agent, spawn_agent) are registered by the SDK once the
// engine exists, which is why they appear here without being in this package.
func GeneralAgentToolNames() []string {
	return []string{
		// Files and code.
		"read_file", "write_file", "edit_file", "glob", "grep", "remove_file",
		// Shell and background jobs.
		"bash", "job_output", "job_kill", "write_stdin",
		// Web.
		"web_search", "web_fetch",
		// Dialogue, plan and permissions.
		"ask_user_question", "enter_plan_mode", "exit_plan_mode", "submit_plan", "request_permissions",
		// Showing the person a file that was made.
		"present_artifact",
		// Visible progress.
		"task_create", "task_update", "task_list", "task_get",
		// Sub-agents.
		"agent", "spawn_agent", "wait_agent",
		// Extensions.
		"skill", "mcp", "tool_search",
	}
}
