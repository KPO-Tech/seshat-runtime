package sdk

import "github.com/KPO-Tech/seshat-runtime/internal/tools/builtin"

// EnterpriseAgentTools returns the tools a general agent in a company setting (the desktop application) has loaded at every
// request: the common core, the git worktree tools, get_context_remaining and a small set of browser tools. Use it as
// ClientConfig.MainAgentTools together with EnterpriseAgentDeferredTools and EnableToolSearch, which cuts what each request
// carries for tool definitions by about 45%, from about 35.5k to 19.4k tokens (see TestEnterpriseAgentProfileCutsTheToolDefinitions).
func EnterpriseAgentTools() []string {
	return builtin.EnterpriseAgentToolNames()
}

// EnterpriseAgentDeferredTools returns the tools that general agent does not have loaded but can find with tool_search and
// then use: the rest of the browser tools, git, scheduling, calculators, media and more. Use it as
// ClientConfig.MainAgentDeferredTools. A tool in neither list is not offered to the main agent at all. MCP tools are
// deferred by themselves and found the same way.
func EnterpriseAgentDeferredTools() []string {
	return builtin.EnterpriseAgentDeferredToolNames()
}
