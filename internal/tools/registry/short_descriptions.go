package registry

import (
	"strings"
	"unicode/utf8"
)

// maxShortDescription is the longest a one-line description is allowed to be, in characters.
const maxShortDescription = 100

// ShortDescription is the one-line description of a tool for a list where the tool itself is not loaded: the names of the
// deferred tools in the system prompt. It is the tool's own ShortDescription when it sets one, else the line written for it in
// builtinShortDescriptions, else the first sentence of its description (which is what an MCP tool gets).
func ShortDescription(def Definition) string {
	if s := strings.TrimSpace(def.ShortDescription); s != "" {
		return s
	}
	if s, ok := builtinShortDescriptions[def.Name]; ok {
		return s
	}
	return firstSentence(def.Description, maxShortDescription)
}

// HasBuiltinShortDescription reports whether a line was written for the tool.
func HasBuiltinShortDescription(name string) bool {
	_, ok := builtinShortDescriptions[name]
	return ok
}

// firstSentence cuts a description at the end of its first sentence or line, and at max characters.
func firstSentence(text string, max int) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexAny(text, "\n"); i >= 0 {
		text = text[:i]
	}
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i+1]
	}
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

// builtinShortDescriptions holds one line for each tool that an agent may have to find with tool_search. Keep each to what
// the tool is for, in a few words: the full description is loaded when the agent searches for it.
var builtinShortDescriptions = map[string]string{
	// Agents and skills.
	"close_agent":        "Stop a sub-agent and remove it from the active ones.",
	"list_agents":        "List the active sub-agents with their status.",
	"resume_agent":       "Reopen a finished sub-agent, with its history, and give it a new task.",
	"send_agent_message": "Send a message to a running sub-agent for its next turn.",
	"seshat_list_skills": "List every skill available (builtin, user, project, MCP, installed).",
	"seshat_read_skill":  "Read the full content of a skill by name, with its bundled files.",
	"skill":              "Run a skill (specialised instructions) by name.",
	// Browser.
	"browser_close_page":     "Close a browser tab.",
	"browser_extract":        "Read the current page as clean text with its headings.",
	"browser_list_downloads": "List the files the browser downloaded.",
	"browser_list_pages":     "List the open browser tabs.",
	"browser_search_content": "Search the pages already captured in this browser session.",
	"browser_select_page":    "Switch to another open browser tab.",
	"browser_wait":           "Wait for a page to settle or for some text to appear.",
	// Files and search.
	"apply_patch":          "Add, update, delete or rename several files in one atomic patch.",
	"convert_document":     "Convert a local document (scan, PDF, Office, audio) to Markdown, with OCR.",
	"create_directory":     "Create a folder and its parents (mkdir -p).",
	"get_file_metadata":    "Size, type, permissions and dates of a file, without reading it.",
	"list_directory":       "List the entries of a folder with their type and size.",
	"read_document_url":    "Fetch a document at a URL (PDF, Office, HTML, arXiv) and convert it to Markdown.",
	"render_document_page": "Render one page of a document as an image to look at it.",
	"search_start":         "Run a slow content search in the background instead of blocking.",
	// Version control and code.
	"git_branch": "List, create, switch or delete git branches.",
	"git_commit": "Stage files and make a git commit.",
	"git_diff":   "Show changes between commits, the working tree or the index.",
	"git_log":    "Show the commit history.",
	"git_status": "Show the working tree status: branch, staged and changed files.",
	"lsp":        "Code intelligence from a language server: definitions, references, symbols.",
	"repo_map":   "A compact ranked map of the repository: packages, types, signatures.",
	// Scheduling.
	"delete_job":   "Delete a scheduled job for good.",
	"list_jobs":    "List the scheduled jobs with their status and next run.",
	"pause_job":    "Stop a scheduled job from triggering, without deleting it.",
	"resume_job":   "Re-enable a paused scheduled job.",
	"run_job_now":  "Run a scheduled job once, right now.",
	"schedule_job": "Create a job that runs an agent on a schedule, or once.",
	"update_job":   "Change the settings of a scheduled job.",
	// Goals, tasks and processes.
	"create_goal": "Set a goal that lasts across the turns of this session.",
	"get_goal":    "Show the session's goal and how much of its budget is used.",
	"monitor":     "Run a command in the background and receive its output lines as they come.",
	"task_stop":   "Stop a tracked task or a running background task.",
	"update_goal": "Mark the goal complete, blocked or paused, or change its text.",
	// Calculations.
	"calculator":     "Arithmetic, advanced functions and expression evaluation.",
	"financial_calc": "Interest, loan payments, ROI, present and future value.",
	"statistics":     "Statistics on a list of numbers (mean, median, deviation, correlation).",
	"unit_convert":   "Convert between units (length, mass, temperature...).",
	// Memory graph.
	"memory_add_observations": "Add facts to existing entities in long-term memory.",
	"memory_create_entities":  "Create entities, with first facts, in long-term memory.",
	"memory_open_nodes":       "Get specific memory entities by exact name, with their relations.",
	"memory_search_nodes":     "Search long-term memory by name, type or content.",
	// Media.
	"generate_image":   "Generate an image from a text prompt.",
	"present_artifact": "Show a finished image, audio, video or document to the person in the conversation.",
	"speech_to_text":   "Transcribe audio to text.",
	"text_to_speech":   "Turn text into spoken audio.",
	// Configuration and MCP resources.
	"get_config":         "Show the effective security policy: approvals, protected paths, limits.",
	"mcp_list_resources": "List the resources of the connected MCP servers.",
	"mcp_read_resource":  "Read an MCP resource by its URI.",
}
