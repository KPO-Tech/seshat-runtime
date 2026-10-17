package builtin

// EnterpriseAgentToolNames returns the tools loaded at every request for a general agent in a company setting (the
// desktop application): the common core of GeneralAgentToolNames, the git worktree tools (they change the working
// folder of the session, which bash cannot) and a small set of browser tools to act on a page directly.
//
// The browser tools not listed here, and many other tools, are in EnterpriseAgentDeferredToolNames: the agent finds
// them with tool_search when it needs them, so they cost nothing until then. A tool in neither list (publishing,
// outgoing messages, notebooks, market research...) is not offered to this agent at all and belongs to specialised
// agents. The classification and its reasons are in docs/reflexions/journal-engineering-2026/
// inventaire-outils-et-agents-2026-10-17.md of the seshat-ai repository.
func EnterpriseAgentToolNames() []string {
	names := append([]string(nil), GeneralAgentToolNames()...)
	names = append(names,
		// Git worktrees.
		"enter_worktree", "exit_worktree",
		// How much of the context window is left.
		"get_context_remaining",
	)
	return append(names, BrowserCoreToolNames()...)
}

// BrowserCoreToolNames returns the browser tools for acting on a page: open, navigate, read the page, click, type,
// press a key, scroll, capture. About 1,100 tokens together.
func BrowserCoreToolNames() []string {
	return []string{
		"browser_open", "browser_navigate", "browser_snapshot", "browser_click",
		"browser_type", "browser_press", "browser_scroll", "browser_screenshot",
	}
}

// EnterpriseAgentDeferredToolNames returns the tools the enterprise agent can find with tool_search and then use. Use
// it as ClientConfig.MainAgentDeferredTools next to EnterpriseAgentToolNames as MainAgentTools.
func EnterpriseAgentDeferredToolNames() []string {
	return []string{
		// Agents and skills.
		"close_agent", "list_agents", "resume_agent", "send_agent_message", "seshat_list_skills", "seshat_read_skill",
		// Browser, the rest.
		"browser_close_page", "browser_extract", "browser_list_downloads", "browser_list_pages", "browser_search_content", "browser_select_page", "browser_wait",
		// Files and search.
		"apply_patch", "convert_document", "create_directory", "get_file_metadata", "list_directory", "read_document_url", "render_document_page", "search_start",
		// Version control and code.
		"git_branch", "git_commit", "git_diff", "git_log", "git_status", "lsp", "repo_map",
		// Scheduling.
		"delete_job", "list_jobs", "pause_job", "resume_job", "run_job_now", "schedule_job", "update_job",
		// Goals, tasks and processes.
		"create_goal", "get_goal", "monitor", "task_stop", "update_goal",
		// Calculations.
		"calculator", "financial_calc", "statistics", "unit_convert",
		// Memory graph.
		"memory_add_observations", "memory_create_entities", "memory_open_nodes", "memory_search_nodes",
		// Media.
		"generate_image", "speech_to_text", "text_to_speech",
		// Configuration and MCP resources.
		"get_config", "mcp_list_resources", "mcp_read_resource",
	}
}
