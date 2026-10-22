# Tool System

## Tool contract

Every tool implements the `Tool` interface in `internal/tools/contract/interface.go`:

```go
type Tool interface {
    Definition() Definition
    Call(ctx context.Context, input CallInput, permCheck types.CanUseToolFn) (CallResult, error)
    Description(ctx context.Context) (string, error)
    ValidateInput(ctx context.Context, input map[string]any) (map[string]any, error)
    BackfillInput(ctx context.Context, input map[string]any) map[string]any
    CheckPermissions(ctx context.Context, input map[string]any, toolCtx ToolUseContext) types.PermissionResult
    IsConcurrencySafe(input map[string]any) bool
    IsReadOnly(input map[string]any) bool
    IsEnabled() bool
    FormatResult(data any) string
}
```

### Optional capabilities

```go
// PreparePermissionMatcher lets the tool supply a content-specific matcher
// that the permission engine uses to evaluate allow/deny rules.
type PermissionMatcherTool interface {
    PreparePermissionMatcher(ctx context.Context, input map[string]any) (matchFn, error)
}

// ExecutesInPlanMode marks tools that run even in plan mode.
// By default, tools are blocked in plan mode.
type PlanModeExecutableTool interface {
    ExecutesInPlanMode(input map[string]any) bool
}

// RequiresUserInteraction marks tools that need a terminal attached.
type RequiresUserInteractionTool interface {
    RequiresUserInteraction() bool
}
```

---

## Execution model

The orchestrator (`internal/execution/orchestrator.go`) partitions concurrent and sequential tool uses based on `IsConcurrencySafe`:

- **Concurrent batch**: tools that return `IsConcurrencySafe = true` run in parallel (max 10 goroutines by default). Context updates (tool results injected into the transcript) are applied in invocation order after all finish.
- **Serial batch**: tools run one at a time. Context updates are applied immediately after each call.

A turn's tool uses may contain multiple concurrent batches interleaved with serial ones.

---

## Built-in tools reference

Tools marked **stub** are registered but `IsEnabled()` returns `false` — they appear here so contributors know where to implement them. See their package-level doc comments for implementation notes.

---

### File tools (`internal/tools/files/`)

| Tool | Description |
|---|---|
| `file_read` | Read file content — text, image (PNG/JPG), or PDF. Supports line-range limits. A PDF is read page by page with a `--- page N ---` marker before each page; a read stops at a size limit (120,000 characters, or 20 pages sent to the document reader) and says which page to continue from with `pages`. The result names the pages that hold images or have no readable text. Native pages come back as markdown: headings, fenced code with its indentation, lists, paragraphs, formulas as `$...$` / `$$...$$` in LaTeX-like notation, and tables as markdown tables (merged cells resolved; tables with no lines too, and tables that are pictures, including scanned ones, when the native reader has its layout models and OCR). DOCX, PPTX and XLSX files are converted to markdown natively, with their headings, nested lists, tables (merged cells resolved), charts as data tables, footnotes, picture descriptions and speaker notes; a long one is read in pieces of about 120,000 characters, the result saying which `offset` to continue from, and a deck that is mostly pictures gives the little text it has; an HTML file is shown as source. An encrypted PDF is decrypted (AES-256 included); one that needs a password says so, and `password` takes the one the user gave. |
| `file_write` | Create or fully overwrite a file. |
| `file_edit` | Replace an exact string in a file (string-targeted, not line-based). |
| `file_patch` | Apply a unified diff patch to a file. |
| `glob` | List files matching a glob pattern relative to the working directory. |
| `grep` | Search file content with a regular expression. |
| `fs_create_directory` | Create a directory (and parents) if it does not exist. |
| `fs_get_metadata` | Stat a path — size, permissions, mod time, type. |
| `fs_list_directory` | List directory contents (non-recursive by default). |
| `fs_remove` | Delete a file or empty directory. |
| `read_url` | Fetch a URL and return its content as Markdown or plain text. |

---

### Document reader tools (`internal/tools/files/documentreader/`)

Require a document reader/renderer to be configured on `ClientConfig` (`DocumentReaderURL`/`DocumentConverter` for the first two, `DocumentPageRenderer` for the third) — each registers unconditionally but returns a "not configured" result when its dependency is nil.

| Tool | Description |
|---|---|
| `convert_document` | Convert a local file to markdown via the configured document reader — OCR, layout analysis, table structure, audio transcription. Prefer `read_file` first; reach for this to force reconversion or for files `read_file` couldn't handle natively. |
| `read_document_url` | Fetch a document at a URL (PDF, DOCX, PPTX, XLSX, HTML, arXiv) and convert it to markdown. Can optionally save the result to a workspace path. |
| `render_document_page` | Render one page of a local PDF to a PNG and attach it to the conversation as a real image (delivered via a follow-up message, not inlined as text) for visual inspection of diagrams, charts, tables, or scanned content that markdown extraction would miss. Renders up to 8 MiB per page; larger renders are reported instead of attached. |

---

### Bash tool (`internal/tools/bash/`)

| Tool | Description |
|---|---|
| `bash` | Execute a shell command. Captures stdout/stderr, supports background jobs and timeouts. Includes a safety scanner for dangerous patterns. On Windows it runs commands in Git Bash (never the WSL launcher), or in PowerShell when Git Bash is missing; `SESHAT_BASH_SHELL` names another shell. |
| `bash_write_stdin` | Write data to a running background job's stdin. |
| `bash_job_output` | Read buffered stdout/stderr from a background job. |
| `bash_job_kill` | Send SIGTERM/SIGKILL to a background job. |
| `monitor` | Start a shell command and stream its stdout line-by-line as notifications. Designed for log tailing, build watching, and long-running processes. |

**Environment of the commands.** A command the model runs gets the environment of the process **without its secrets** (`internal/envfilter`): the variables whose name says secret (`*_API_KEY`, `*_TOKEN`, `*_SECRET`, `*_PASSWORD`, `*_DSN`, `DATABASE_URL`, ...), the families of the providers Seshat talks to (`ANTHROPIC_*`, `OPENAI_*`, `AWS_SECRET*`, `SESHAT_DB_*`, `SESHAT_S3_*`, ...) and the ones whose value is a credential (a URL with a password, a private key, the usual token prefixes). `PATH`, `HOME`, the locale, the proxies and the build settings stay. Otherwise a command that prints its environment (`env`, `curl -d "$(env)"`) hands the provider keys to whoever a prompt injection names. This applies to the `bash` tool (foreground, background, `monitor`), to the local sandbox executor and to the shell of the terminal UI; the Docker sandbox never forwarded the environment. Two settings: `SESHAT_BASH_ENV_ALLOW=NAME,PREFIX_*` keeps variables that look like secrets (for a command that needs `GITHUB_TOKEN`, say), `SESHAT_BASH_INHERIT_ENV=true` restores the old behaviour. Variables given by the call itself (`env` of a tool call, `RunRequest.Env`) always pass.

---

### Notebook tools (`internal/tools/notebook/`)

Interact with Jupyter notebooks and live kernels. The kernel sub-tools require a running Jupyter server — set `JUPYTER_SERVER_URL` and `JUPYTER_TOKEN`.

| Tool | Description |
|---|---|
| `notebook_create` | Create a new `.ipynb` file with optional initial cells. Fails if file already exists. |
| `notebook_read` | Read notebook cells, outputs, and kernel metadata. Supports index-based filtering. |
| `notebook_write` | Create or fully overwrite a notebook (for programmatic generation). |
| `notebook_edit` | Edit cells in place — replace/insert/delete. Supports single-op or batch `ops[]` array. |
| `notebook_execute` | Execute cells via a live Jupyter kernel and write outputs back to the notebook file. |
| `notebook_run` | Run arbitrary code in a kernel without creating/modifying a notebook file. |
| `notebook_kernel` | Manage Jupyter kernels — list/start/restart/interrupt/stop. |

---

### Web tools (`internal/tools/web/`)

| Tool | Description |
|---|---|
| `web_fetch` | HTTP fetch with Markdown conversion, response caching, and Docling PDF support. |
| `web_search` | Web search. Backends: DuckDuckGo (default), Exa, Jina, Tavily, SearXNG. |

---

### Context tools

| Tool | Description |
|---|---|
| `repo_map` | Build a compact structural map of the current repository. V1 is Go-first: it enumerates tracked/unignored files, parses Go packages and signatures, ranks important files, and renders within a token budget. |
| `workflow_draft` | Validate and render a static workflow DAG draft as YAML or JSON. Supports `agent`, `verifier`, `critic`, and `router` node roles for reusable parallel-agent workflow structure. |

---

### Browser tools (`internal/tools/web/browser/`)

Browser automation via Playwright (go-rod). Available when a browser instance is configured.

| Tool | Description |
|---|---|
| `browser_open` | Open a URL in a new or existing tab |
| `browser_navigate` | Navigate the active tab to a URL |
| `browser_snapshot` | Capture a DOM/accessibility snapshot |
| `browser_extract` | Extract text content from the current page |
| `browser_screenshot` | Take a screenshot (returns image artifact) |
| `browser_click` | Click an element by selector or coordinates |
| `browser_type` | Type text into an input |
| `browser_press` | Press a key (Enter, Tab, Escape, …) |
| `browser_scroll` | Scroll the page |
| `browser_wait` | Wait for a condition |
| `browser_select_page` | Switch to a tab by index |
| `browser_close_page` | Close a tab |
| `browser_list_pages` | List all open tabs |
| `browser_get_network` | List captured network requests |
| `browser_list_downloads` | List completed downloads |
| `browser_search_content` | Search page text |
| `browser_set_network_policy` | Allow/deny requests by domain |
| `browser_get_network_policy` | Read the current network policy |

---

### Math tools (`internal/tools/math/`)

| Tool | Description |
|---|---|
| `calculator` | Evaluate arithmetic and algebraic expressions. Supports variables, functions, and unit-aware evaluation. |
| `unit_convert` | Convert values between units (length, mass, temperature, speed, …). |
| `statistics` | Descriptive statistics — mean, median, std dev, percentiles, correlation. |
| `financial` | Financial calculations — compound interest, NPV, IRR, amortisation. |

---

### Social tools (`internal/tools/social/`)

Tools for community and developer platforms. Read-only tools require no credentials.

| Tool | Status | Description |
|---|---|---|
| `hn_search` | ✅ live | Full-text search of Hacker News stories and comments via Algolia. |
| `hn_stories` | ✅ live | Fetch HN feed — top/new/best/ask/show/job stories. |
| `hn_item` | ✅ live | Get a story with its full comment thread. |
| `devto_feed` | ✅ live | Browse dev.to articles by tag, popularity, or date. |
| `devto_article` | ✅ live | Fetch a single dev.to article by ID or URL. |
| `devto_publish` | ✅ live | Publish or update a dev.to article (requires `DEV_TO_API_KEY`). |
| `twitter_search` | stub | Search tweets (requires `TWITTER_BEARER_TOKEN`). |
| `twitter_tweet` | stub | Post a tweet (requires OAuth 1.0a). |

---

### Research tools (`internal/tools/research/`)

Tools for market studies, product comparisons and the analysis of customer reviews. Each reads one official API and
returns readable text with the source, link, author and date of every item. A tool whose keys are missing is not
offered to the model. On a shared server the keys are given to each run (`ClientConfig.ResearchKeys`) and the process
environment is never read; on a single-user host the environment variables below are used.

| Tool | Needs | Description |
|---|---|---|
| `appstore_search` | nothing | Find apps in the Apple App Store: id, developer, rating, number of ratings. |
| `appstore_reviews` | nothing | Customer reviews of an app, per country. Apple's public feed serves at most 50 per sort and country (usually the first page), sometimes none for one sort, so both sorts are read and merged. |
| `reddit_search` | `REDDIT_CLIENT_ID`, `REDDIT_CLIENT_SECRET` | Search public Reddit posts, across Reddit or in one subreddit. |
| `reddit_posts` | same | List posts of a subreddit (hot, new, top, rising). |
| `reddit_post` | same | One post with its comments, indented by reply depth. |
| `youtube_search` | `YOUTUBE_API_KEY` | Search videos, with views, likes and comment counts (100 quota units per call). |
| `youtube_comments` | `YOUTUBE_API_KEY` | Comments under a video, with the first replies. |
| `places_search` | `GOOGLE_PLACES_API_KEY` | Find businesses and places on Google Maps (billed by Google). |
| `places_reviews` | `GOOGLE_PLACES_API_KEY` | A place's rating and its reviews (Google returns at most 5 per place). |
| `trustpilot_company` | `TRUSTPILOT_API_KEY` | A company's TrustScore and review count, found by its domain. |
| `trustpilot_reviews` | `TRUSTPILOT_API_KEY` | A company's public reviews, filtered by stars, language and order. |

---

### Notification tools (`internal/tools/notifications/`)

Direct-messaging channels — one-way delivery to a person or system. All require credentials; all registered as stubs until implemented.

| Tool | Status | Env vars | Description |
|---|---|---|---|
| `slack_send` | stub | `SLACK_WEBHOOK_URL` or `SLACK_BOT_TOKEN` | Send to a Slack channel via Incoming Webhook or Bot API. Supports Block Kit. |
| `discord_send` | stub | `DISCORD_WEBHOOK_URL` or `DISCORD_BOT_TOKEN` | Send to a Discord channel via webhook or Bot API. Supports embeds. |
| `telegram_send` | stub | `TELEGRAM_BOT_TOKEN` | Send via Telegram Bot API. HTML and MarkdownV2 parse modes. |
| `email_send` | stub | `SMTP_HOST`, `SMTP_USER`, `SMTP_PASSWORD` | Send email via SMTP (net/smtp, no extra deps). Supports HTML + plain multipart. |
| `whatsapp_send` | stub | `WHATSAPP_PHONE_NUMBER_ID`, `WHATSAPP_TOKEN` | Send via WhatsApp Business Cloud API. Supports text and approved templates. |

---

### VCS tools (`internal/tools/git/`)

Structured git operations — return typed JSON, not raw text. Requires the `git` binary on PATH. All registered as stubs until implemented via `os/exec`.

| Tool | Status | Description |
|---|---|---|
| `git_status` | stub | Working tree status — staged, unstaged, untracked, ahead/behind. |
| `git_log` | stub | Commit history with filters (branch, since, author, grep). |
| `git_diff` | stub | Diff between refs or working tree. Returns structured file list + patches. |
| `git_commit` | stub | Stage files and create a commit. `RequiresPermission: true`. |
| `git_branch` | stub | List, create, switch, or delete branches. |

---

### Multimedia tools (`internal/tools/multimedia/`)

Always registered; `IsEnabled()` returns `false` when no provider is configured.

| Tool | Env var | Description |
|---|---|---|
| `present_artifact` | none | Show a file the agent made (image, audio, video, PDF, document, spreadsheet, slides, web page) in the conversation. The file must be in the working folder. |
| `generate_image` | `OPENAI_API_KEY` / Gemini key | Generate an image from a text prompt. Returns base64 or URL. |
| `text_to_speech` | provider key | Synthesise speech audio from text. Returns base64-encoded audio. |
| `speech_to_text` | provider key | Transcribe audio to text. Input: base64-encoded audio. |

Speech is configured like a chat provider: `text_to_speech` and `speech_to_text` in the configuration name a `provider`, an optional `model`, and optionally a `base_url` and `api_key` (otherwise the provider's entry in `providers`, then its environment variable, is used). The audio catalog (`internal/audio/providers/catalog.go`, also `audioproviders.Profiles()` in the SDK) lists what each provider offers; the first model is the default:

| Provider | Speech to text | Text to speech | Key |
|---|---|---|---|
| `openai` | `whisper-1` (segments and timings), `gpt-transcribe`, `gpt-4o-transcribe`, `gpt-4o-mini-transcribe` | `gpt-4o-mini-tts`, `tts-1`, `tts-1-hd` | `OPENAI_API_KEY` |
| `groq` | `whisper-large-v3-turbo`, `whisper-large-v3` | — | `GROQ_API_KEY` |
| `mistral` | `voxtral-mini-latest` | — | `MISTRAL_API_KEY` |
| `elevenlabs` | `scribe_v2` (word timings) | `eleven_multilingual_v2`, `eleven_v4`, `eleven_v4_turbo`, `eleven_v3`, `eleven_flash_v2_5`, `eleven_flash_v2` | `ELEVENLABS_API_KEY` |

Any other ID works when `base_url` points at an OpenAI-compatible server (`/audio/transcriptions`, `/audio/speech`), for example a local whisper.cpp server, and then needs no key. ElevenLabs has its own API and its own clients (`internal/audio/providers/elevenlabs.go`): `voice` is an ElevenLabs `voice_id` (without one, the first voice of the account is used), `format` takes `mp3`, `wav`, `opus`, `pcm` or a full ElevenLabs `output_format` such as `mp3_44100_192`. A provider with yet another API (Deepgram) needs its own client in the same folder. The transcription request asks for `verbose_json` from Whisper models and `json` from the others, which reject `verbose_json` and so return text without segments.

Image generation is configured the same way (`image_generation`: `provider`, `model`, `base_url`, `api_key`), from the catalog in `internal/image/providers/catalog.go`; the first model is the default:

| Provider | Models | Key |
|---|---|---|
| `openai` | `gpt-image-2.5-flare` (fast), `gpt-image-2.5-sunburst` (precise edits), `gpt-image-2`, `gpt-image-1`, `gpt-image-1-mini`; `dall-e-3` still works when named | `OPENAI_API_KEY` |
| `gemini` | `gemini-nano-banana-2.1`, `gemini-3.1-flash-image`, `gemini-3-pro-image`, `gemini-3.1-flash-lite-image`; an `imagen-*` model uses the Imagen call | `GOOGLE_API_KEY` |
| `openrouter` | `google/gemini-nano-banana-2.1`, `openai/gpt-image-2.5-flare`, `openai/gpt-image-2.5-sunburst`, `black-forest-labs/flux-3-image`, `bytedance-seed/seedream-5-0-pro`, `recraft/recraft-v4.1`, `x-ai/grok-imagine-image-2.0`, `qwen/qwen-image-3` (the full list: `GET https://openrouter.ai/api/v1/images/models`) | `OPENROUTER_API_KEY` |

Any other ID works as an OpenAI-compatible image server when `base_url` is given, without a key. The GPT Image models take quality `low`, `medium`, `high` or `auto` (the 2.5 ones also `xhigh` and `max`) and no `response_format`; DALL-E takes `standard` or `hd`.

---

### Agent tools (`internal/tools/agents/`)

| Tool | Description |
|---|---|
| `spawn_agent` | Launch a sub-agent with isolated context, tool set, and system prompt. Returns an agent ID. |
| `wait_agent` | Block until a sub-agent completes; returns its final output. |
| `resume_agent` | Resume a previously paused sub-agent with a new message. |
| `list_agents` | List all active sub-agents and their current status. |
| `send_agent_message` | Send a message to a running sub-agent. |
| `close_agent` | Terminate a sub-agent and release its resources. |

---

### Goal tools (`internal/tools/special/goal/`)

| Tool | Description |
|---|---|
| `create_goal` | Create or replace the active session goal with an objective and optional token budget. When SQLite session storage is enabled, the goal is persisted with the session and successful turns record token usage against it. |
| `get_goal` | Retrieve the active session goal, status, token usage, elapsed time, and remaining budget. |
| `update_goal` | Update the active goal's status (`active`, `paused`, `blocked`, `complete`) or objective. |

---

### Memory tools (`internal/tools/special/memory/`)

Available when long-term memory is configured.

| Tool | Description |
|---|---|
| `memory_create_entities` | Create named entities in the knowledge graph. |
| `memory_add_observations` | Add observations to existing entities. |
| `memory_search_nodes` | Semantic search across the knowledge graph. |
| `memory_open_nodes` | Retrieve specific nodes by name. |

---

### RAG tools (`internal/tools/special/rag/`)

| Tool | Description |
|---|---|
| `rag_search` | Semantic search across indexed documents. |
| `rag_ingest` | Ingest a file or directory into the vector store. |

---

### System tools (`internal/tools/system/`)

| Tool | Description |
|---|---|
| `mcp_*` | Exposes MCP server tools. Each connected MCP server contributes one or more tools prefixed with the server name. |
| `seshat_list_skills` | List available skills with name and description. |
| `seshat_read_skill` | Read the full content of a skill file. |
| `seshat_validate_skill` | Validate a skill file for structural correctness. |
| `skill_run` | Invoke a skill by name with optional arguments. |

---

### Task tools (`internal/tools/task/`)

| Tool | Description |
|---|---|
| `task_create` | Create a session-scoped tracked task for execution progress. Pass `tasks` to create a whole list in one call. |
| `task_list` | List tracked session tasks and, when requested, background runtime tasks. |
| `task_get` | Fetch details for a tracked session task, or for a background task or agent. With `wait_seconds` (up to 600) it waits for a background agent to finish and returns its final answer, so there is no need to sleep in a shell; a task that is still running when the time is up comes back with `timedOut`. |
| `task_update` | Update tracked task status, details, and dependencies. Pass `updates` to change several tasks in one call (finish one, start the next). |
| `task_stop` | Stop tracking a session task. |
| `task_output` | Get the output of a background runtime task. |

---

### Special tools (`internal/tools/special/`)

| Tool | Description |
|---|---|
| `ask_user` | Prompt the user for input during a session. Supports timeout. |
| `lsp` | Language Server Protocol — go-to-definition, hover, diagnostics, rename. |
| `worktree_enter` / `worktree_exit` | Enter/exit a git worktree (isolated branch checkout). |
| `tool_search` | Search available tools by keyword, including deferred ones. |
| `plan_mode_enter` / `plan_mode_exit` | Switch to plan mode (no execution, only planning). |
| `plan_submit` | Submit a plan for user review and approval. |
| `request_permissions` | Request additional permissions at runtime. |
| `fim` | Fill-in-the-middle code completion at the cursor position. |

---

## Writing a custom tool

```go
type MyTool struct{}

func (t *MyTool) Definition() tool.Definition {
    return tool.Definition{
        Name:        "my_tool",
        Description: "Does something useful.",
        InputSchema: schema.FromMap(map[string]any{
            "type": "object",
            "properties": map[string]any{
                "input": map[string]any{"type": "string"},
            },
            "required": []string{"input"},
        }),
        IsReadOnly:        true,
        IsConcurrencySafe: true,
    }
}

func (t *MyTool) Call(ctx context.Context, input tool.CallInput, _ types.CanUseToolFn) (tool.CallResult, error) {
    value, _ := input.Parsed["input"].(string)
    return tool.NewTextResult("result: " + value), nil
}

// ... implement remaining interface methods
```

Register in `internal/tools/builtin/builtin.go`:

```go
tools := []tool.Tool{
    // ...
    &MyTool{},
}
```

See [`CONTRIBUTING.md`](../CONTRIBUTING.md) for the full tool-addition checklist.

---

## Static Workflow DAGs

`pkg/workflow` runs a minimal static DAG that can be loaded from YAML or JSON.
Each node has an `id`, a `prompt`, optional `needs` dependencies, and an
optional `kind`: `agent`, `verifier`, `critic`, or `router`. Nodes whose
dependencies are complete run in parallel up to `--max-parallel`; each
downstream prompt receives the text outputs from its dependencies.

`verifier`, `critic`, and `router` nodes add role-specific prompt guidance.
Router nodes also declare allowed `routes`, which are rendered into the prompt;
V1 records the routing decision as model output while execution remains a static
DAG.

Headless hosts should prefer the SDK entry point, `Client.RunWorkflow`. The CLI
command is a thin wrapper around that SDK method.
Agents can use `workflow_draft` to produce a validated YAML/JSON workflow
definition before asking to save it or running it later.

```yaml
name: investigation
nodes:
  - id: collect
    prompt: Find the relevant facts.
  - id: verify
    kind: verifier
    prompt: Check the facts for contradictions.
    needs: [collect]
  - id: critique
    kind: critic
    prompt: Identify gaps, risks, and missing evidence.
    needs: [collect]
  - id: route
    kind: router
    prompt: Decide whether the final answer is ready or more research is needed.
    needs: [verify, critique]
    routes: [final]
  - id: final
    prompt: Write the final answer.
    needs: [route]
```

Run it with:

```sh
seshat workflow run workflow.yaml --max-parallel 4
```

---

## Companion Profile

`pkg/companion` stores a lightweight collaboration profile. Headless hosts can
set `ClientConfig.Companion`; the SDK appends the generated `<companion>` block
after the normal system prompt so it guides tone and presence without replacing
Seshat's core instructions.

Local CLI users can manage the same profile with:

```sh
seshat companion init --name Nora --style "calm, precise" --traits "warm,proactive"
seshat companion show
seshat companion enable
seshat companion disable
```

---

## Tool registry and deferred tools

The `Registry` (`internal/tools/registry/`) holds all available tools. Tools can be:

- **Eager**: available from session start (included in every tool list sent to the LLM)
- **Deferred**: not included in the initial tool list; revealed by `tool_search` during a session

Deferred tools are useful for large tool sets — they keep the initial context small while still making every tool discoverable.
