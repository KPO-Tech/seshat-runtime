<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/lockup-on-dark.svg">
    <img src="docs/brand/lockup-on-light.svg" alt="Seshat Runtime" width="420">
  </picture>
</p>


<p align="center">
  <b>Open-source Go agent runtime for governed, multi-provider, self-hostable execution</b><br>
  <i>Built for serious agent work today, with a larger open-source vision behind it.</i>
</p>

<p align="center">
  <a href="https://seshat-ai.com/en"><b>🌐 Website</b></a> ·
  <a href="https://seshat-ai.com/en/docs"><b>📚 Documentation</b></a> ·
  <a href="https://seshat-ai.com/en/docs/getting-started/installation"><b>⬇️ Installation</b></a> ·
  <a href="https://github.com/KPO-Tech/seshat-runtime/discussions"><b>💬 Discussions</b></a> ·
  <a href="https://github.com/KPO-Tech/seshat-runtime/issues"><b>🐛 Issues</b></a> ·
  <a href="https://github.com/KPO-Tech/SeshatOS"><b>🖥️ SeshatOS</b></a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Status-Active%20Development-orange?style=for-the-badge">
  <img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=for-the-badge&logo=go">
  <img src="https://img.shields.io/badge/License-Apache%202.0-brightgreen?style=for-the-badge">
  <img src="https://img.shields.io/badge/Providers-15-4F6EF7?style=for-the-badge">
  <img src="https://img.shields.io/badge/Tools-60%2B-A855F7?style=for-the-badge">
</p>

<h1 align="center">Seshat Runtime</h1>

<p align="center">
  <img src="docs/images/seshat-demo.gif" alt="Seshat delegates a research task to five sub-agents working in parallel, then shows them in the terminal interface" width="860">
</p>

<p align="center">
  <i>One request, five sub-agents working in parallel. Replayed from a real session of the terminal interface (the pace is reconstructed).</i>
</p>

---

## What Seshat Is Today

Seshat is an open-source Go agent runtime for people who need more than a chat shell and less vendor lock-in than a closed assistant.

Its current wedge is concrete:

- governed multi-turn agent execution
- tools, files, sandboxed bash, web, browser, MCP, RAG, and skills
- multi-provider routing with self-hosted and local-first deployment options
- persistent sessions, permissions, recovery, and audit-friendly behavior
- CLI/TUI, gRPC, and Go SDK surfaces on the same runtime core

If you want the class of tool people reach for in products like Claude Code, but open, written in Go, multi-provider, and deployable under your own rules, this is the project.

## Why This Exists

Most of the market still splits into two weak extremes:

- chat products that answer well but do not own serious execution
- low-level frameworks that are flexible but too fragmented, too provider-shaped, or too awkward to ship as a real runtime

Seshat is the missing middle layer: an execution runtime that can actually run agents, call tools, hold sessions, enforce permissions, recover from failures, and stay usable as infrastructure.

## The Broader Open-Source Direction

The project still keeps a broader ambition, and that matters for the community.

Seshat is not only about making one strong solo agent. The longer-term direction is organized agentic work: explicit roles, mission memory, structured coordination, durable teams, and an open foundation that others can extend through skills, MCP servers, SDK integrations, and domain-specific workflows.

That larger vision belongs in the docs and in open-source discussion because contributors need the map, not only the current feature list.

- Product and runtime direction: https://seshat-ai.com/en/docs/learn/vision
- Runtime concepts and architecture: https://seshat-ai.com/en/docs/concepts/what-is-seshat
- Technical bets behind the project: https://seshat-ai.com/en/docs/learn/technical-hypotheses
- Why the runtime is written in Go: https://seshat-ai.com/en/docs/learn/why-go

## Runtime First, Platform Above It

`Seshat` is the runtime layer.

Use this repository if you want to:

- run agents locally or self-host them under your own control
- embed the runtime inside a Go application or internal system
- extend providers, tools, permissions, MCP integration, or runtime behavior

If you want a ready-to-use product built on this engine, see [SeshatOS](https://github.com/KPO-Tech/SeshatOS) (local-first desktop app and backend) or [SeshatCloud](https://github.com/KPO-Tech/SeshatCloud) (multi-tenant server and admin console for organizations).

---

## 🖥️ Terminal UI


`seshat chat` drops you into a full-featured terminal interface built for long-running agent sessions.

<table>
  <tr>
    <td align="center" width="50%">
      <img src="docs/captures/home.png" alt="Welcome screen" width="100%">
      <br><sub><b>Welcome</b> — up and running in 10 seconds</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/captures/settings.png" alt="Settings panel" width="100%">
      <br><sub><b>Settings</b> — every section, one keystroke away (<code>ctrl+p</code>)</sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/captures/providers.png" alt="Provider families" width="100%">
      <br><sub><b>Providers</b> — language models, image, speech and web search</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/captures/models.png" alt="Model selection" width="100%">
      <br><sub><b>Model selection</b> — switch models in 2 keystrokes (<code>ctrl+l</code>)</sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/captures/questions.png" alt="Agent asking clarifying questions" width="100%">
      <br><sub><b>Asks before it acts</b> — clarifying questions with options, then a plan</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/captures/web-research.png" alt="Agent fetching web pages" width="100%">
      <br><sub><b>Checks the source</b> — fetches pages and shows what it read</sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/captures/plan-review.png" alt="Plan review" width="100%">
      <br><sub><b>Plan review</b> — read, comment and approve the plan before work starts</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/captures/agent-working.png" alt="Agent working with sub-agent tasks" width="100%">
      <br><sub><b>Agent at work</b> — task list, sub-agents and streamed results</sub>
    </td>
  </tr>
</table>

> **Keyboard shortcuts:** `ctrl+p` settings · `ctrl+l` models · `ctrl+s` sessions · `ctrl+,` providers · `ctrl+n` new session · `ctrl+t` tasks · `ctrl+u` copy last response · `ctrl+y` toggle yolo · `ctrl+o` open editor · `ctrl+g` help · `ctrl+c` quit
>
> **New line in the prompt:** `ctrl+j` works in every terminal. `shift+Enter` and `ctrl+Enter` need a terminal that tells them apart from `Enter`; Windows Terminal and the VS Code terminal do not by default, so run `seshat terminal-setup` (the setup scripts offer it): it writes the key bindings into their settings, after backing the file up, and `--check` previews it. Terminals with a keyboard protocol (kitty, WezTerm, Ghostty, foot, recent Alacritty, iTerm2) need nothing.

> **Clipboard note (Linux):** selection copy works best when `wl-clipboard` (Wayland) or `xclip`/`xsel` (X11) is installed. Without a system clipboard backend, Seshat can request terminal clipboard access but cannot guarantee a real system copy.

> **Compaction note:** transcript compaction is automatic today. A dedicated manual compact action is planned for the TUI once the runtime exposes a real manual-compaction hook.

---

## 🌐 The Seshat Ecosystem

seshat is the **headless runtime**: pure Go, no UI, no users, no billing. It is the foundation everything else builds on.

### Products built on seshat

| | seshat (this repo) | [SeshatOS](https://github.com/KPO-Tech/SeshatOS) | [SeshatCloud](https://github.com/KPO-Tech/SeshatCloud) |
|---|---|---|---|
| **What it is** | Go runtime + SDK + CLI/TUI + gRPC | Local-first desktop app and backend | Multi-tenant server and admin console |
| **Stack** | Go | Go + TypeScript/React/Electron | Go + TypeScript/React (+ a Python service for document intelligence) |
| **License** | Apache-2.0 | Apache-2.0 + Commons Clause (source-available) | Source-available (draft) |
| **Who it's for** | Developers embedding agents in their own apps | Individuals and small teams who self-host | Organizations that need central administration |
| **Status** | Stable, in production use | Under construction (chat-first desktop rebuild) | Server side solid, not yet a packaged offer |

Both products consume `seshat` through `pkg/*` only. `seshat` knows nothing about them.

The commercial activity around Seshat (AI consulting and integration, plus the products above) is presented at [seshat-ai.com](https://seshat-ai.com).

### 🤝 Contribution split

| If you want to... | Contribute to... |
|---|---|
| Improve execution speed, reduce latency, optimize the agent loop | **seshat** (Go) |
| Add a new LLM provider or tool | **seshat** (Go) |
| Expose new capabilities in the SDK or gRPC API | **seshat** (Go) |
| Improve the desktop app or local backend | **SeshatOS** |
| Work on organizations, IAM, or scheduled automation | **SeshatCloud** |

The engine is intentionally kept minimal and fast. If you need something from the SDK that is not exposed yet, open an issue and we will prioritize it.

### 📦 Installation

The full guide is on the website: [Installation — seshat-ai.com](https://seshat-ai.com/en/docs/getting-started/installation). It covers the install script and what it does, its options, the Go toolchain, building from source and using Seshat as a library. The commands below are the short version.

| System | Command (copy, paste, run) | Needs |
|---|---|---|
| **Linux** (amd64, arm64) | `curl -fsSL https://raw.githubusercontent.com/KPO-Tech/seshat-runtime/main/scripts/install.sh \| bash` | `curl` or `wget` |
| **macOS** (Intel, Apple silicon) | the same command as Linux | `curl` |
| **Windows** (amd64, arm64) | `irm https://raw.githubusercontent.com/KPO-Tech/seshat-runtime/main/scripts/install.ps1 \| iex` in PowerShell | nothing, no administrator rights |

You do not need Go: the script downloads a ready-made binary. Afterwards the command is `seshat` on every system, and `seshat update` keeps it current (see *Updating* below).

**Linux and macOS — one command, fully configured:**

```bash
curl -fsSL https://raw.githubusercontent.com/KPO-Tech/seshat-runtime/main/scripts/install.sh | bash
```

Downloads the right binary for your platform, adds it to your PATH, and leaves the runtime directory (`~/.config/seshat-cli/`) ready. The DB and sessions are created on first run.

Options:
```bash
VERSION=v0.1.0 bash <(curl -fsSL ...)   # pin a specific version
```

**Windows (PowerShell) — one command, no administrator rights:**

```powershell
irm https://raw.githubusercontent.com/KPO-Tech/seshat-runtime/main/scripts/install.ps1 | iex
```

Downloads the Windows build (amd64 or arm64), checks it against `SHA256SUMS.txt`, installs `seshat.exe` into `%LOCALAPPDATA%\Programs\seshat`, adds that folder to your user PATH and installs ripgrep (winget, scoop or choco) when it is missing. Set `$env:SESHAT_VERSION = "v1.9.0"` before the command to pin a version, `$env:SESHAT_INSTALL_DIR` to choose the folder, `$env:SESHAT_SKIP_RIPGREP = "1"` to skip ripgrep.

**Updating.** The installed binary updates itself. When you start `seshat` in a terminal and a newer release exists, it says so and asks `Update now? [Y]es / [l]ater / [s]kip this version`. `seshat update` does the same on demand (`--check` only looks, `--yes` skips the question). The download is checked against the release's `SHA256SUMS.txt` before it replaces the binary, and the old binary stays in place if anything fails. The release is looked up at most once a day, with a 2-second limit; `SESHAT_NO_UPDATE_CHECK=1` turns the offer off. Development builds and non-interactive runs (pipes, CI) are never asked. Re-running the install script also updates.

**Session titles, offline (optional).** By default the chat provider names each session after the first message. `seshat title-model install` (the installers offer it) adds a small local model instead, about 500 MB once, CPU only: titles take under a second, cost nothing and keep the first message on your machine. `seshat title-model status|disable|enable|remove` manage it, and the provider still names the session whenever the local model is missing or fails.

Everything lives in `<runtime folder>/title-model/` (`~/.config/seshat-cli/` on Linux, `%APPDATA%\seshat-cli\` on Windows): the llama.cpp server, the model, a `state.json` and the server's `server.log`; `seshat title-model remove` deletes the folder. The server listens on the loopback address only, runs while seshat is open (about 600 MB of memory) and stops after ten idle minutes; if seshat is killed, the next run stops the server it left behind. `SESHAT_TITLE_MODEL=1` makes the installers install it without asking.

**Developers — Go toolchain:**

```bash
# Install the CLI binary
go install github.com/KPO-Tech/seshat-runtime/tui/cmd/cli@latest
```

**SDK — embed in your Go application:**

```bash
go get github.com/KPO-Tech/seshat-runtime@latest
```

---

## 🔀 Three Ways to Use It

### 1. 💻 CLI — `seshat`

An AI agent in your terminal. Multi-provider, local-first, skills-aware. Guide: [Using the CLI](https://seshat-ai.com/en/docs/guides/cli).

**Install**

```bash
# End users — full setup in one command:
curl -fsSL https://raw.githubusercontent.com/KPO-Tech/seshat-runtime/main/scripts/install.sh | bash

# Windows (PowerShell):
#   irm https://raw.githubusercontent.com/KPO-Tech/seshat-runtime/main/scripts/install.ps1 | iex

# Developers — binary only via Go toolchain:
go install github.com/KPO-Tech/seshat-runtime/tui/cmd/cli@latest
```

**Configure a provider**

```bash
seshat config --provider anthropic --api-key sk-ant-...
seshat config --model anthropic:claude-sonnet-5-5
seshat config --print
```

**Run**

```bash
seshat chat                                            # interactive TUI session
seshat chat --resume <session-id>                      # resume a specific session
seshat chat --continue                                 # resume the most recent session
seshat run "list all TODO comments in this codebase"   # one-shot task
seshat sessions list                                   # browse past sessions
seshat version                                         # print installed version
seshat help                                            # full command reference
```

Sessions are persisted locally in SQLite. Skills are loaded from `.seshat/skills/` in your project. The full tool set is available: file edits, sandboxed bash, web search, browser, MCP servers, sub-agents.

---

### 2. 🌐 gRPC Server

Run seshat as a gRPC service and generate clients for any language. Reference: [gRPC API](https://seshat-ai.com/en/docs/sdk/grpc-api).

```bash
# Development
ANTHROPIC_API_KEY=sk-ant-... go run ./cmd/grpc

# From build
ANTHROPIC_API_KEY=sk-ant-... ./bin/seshat-grpc
```

Server starts on `:50051`. The contract lives in `pkg/grpc/proto/seshat.proto`. Generate a client for Python, TypeScript, Java, Rust, or any gRPC-supported language:

```bash
# Python
python -m grpc_tools.protoc -I pkg/grpc/proto --python_out=. --grpc_python_out=. seshat.proto

# TypeScript
npx grpc-tools --js_out=. --grpc_out=. pkg/grpc/proto/seshat.proto
```

One runtime. Every language.

---

### 3. 📦 Go SDK

Embed the full runtime in your own Go application. Reference: [Go SDK](https://seshat-ai.com/en/docs/sdk/go-sdk), with the [events and hooks](https://seshat-ai.com/en/docs/sdk/events-and-hooks) and the [integration guide](https://seshat-ai.com/en/docs/sdk/integration).

```bash
go get github.com/KPO-Tech/seshat-runtime/pkg/sdk
```

```go
import "github.com/KPO-Tech/seshat-runtime/pkg/sdk"

client, err := sdk.NewClient(&sdk.ClientConfig{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    Model:  sdk.ModelIdentifier{Provider: "anthropic", Model: "claude-sonnet-5-5"},
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()

session, _ := client.CreateSession(ctx)
resp, _ := session.SubmitMessage(ctx, "Write a Go HTTP handler for /health")
fmt.Println(resp.Content)
```

---

## 📊 How Seshat Compares

| Feature | **seshat** | Claude Agent SDK | OpenAI Agents SDK | LangGraph | CrewAI |
|---|:---:|:---:|:---:|:---:|:---:|
| Language | **Go** | Python/TS | Python | Python | Python |
| Single binary (no deps) | ✅ | ❌ | ❌ | ❌ | ❌ |
| CLI included | ✅ | ❌ | ❌ | ❌ | ❌ |
| gRPC server (any language) | ✅ | ❌ | ❌ | ❌ | ❌ |
| Multi-provider | ✅ (15) | ❌ Claude only | ❌ OpenAI only | ✅ | ✅ |
| MCP client | ✅ | ✅ | ✅ | Partial | ❌ |
| Sandboxed bash (Landlock, srt, Docker) | ✅ | ✅ | ❌ | ❌ | ❌ |
| Skills system | ✅ | ❌ | ❌ | ❌ | ❌ |
| Built-in RAG | ✅ | ❌ | ❌ | ❌ | ❌ |
| Browser automation | ✅ | ✅ | ❌ | ❌ | ❌ |
| Session persistence | ✅ | ❌ | ❌ | ✅ | ❌ |
| OTel tracing | ✅ | ❌ | ❌ | ✅ | ❌ |
| Open-source license | Apache 2.0 | MIT | MIT | MIT | Apache 2.0 |

---

## ✨ Capabilities

| Capability | Details |
|---|---|
| 🌍 **Multi-provider** | 15 providers: Anthropic, OpenAI, Gemini, Mistral, DeepSeek, Ollama, OpenRouter, AWS Bedrock, GCP Vertex, Azure Foundry, Codex, MiniMax, Z.ai, OpenCode, Cloudflare Workers AI |
| 🛠️ **60+ built-in tools** | File read/write/patch, bash (Landlock, srt or Docker sandbox), web search, web fetch, browser (Playwright), grep/glob, LSP, sub-agents, RAG, tasks, memory, worktree, notebooks, image generation, TTS/STT |
| 🔌 **MCP client** | Universal MCP client: plug in any MCP server (GitHub, Postgres, Slack, Docker, Notion, ...) |
| ⚡ **Skills** | Markdown instruction files injected into the system prompt: encode your team's conventions and domain expertise |
| 🎯 **Execution modes** | `execute` (default), `plan` (review before act), `pair_programming` (collaborative) |
| 🔒 **Permission engine** | Per-tool deny rules, auto-mode LLM classifier, configurable per session (`auto` / `acceptEdits` / `onRequest` / `bypass` / `never`) |
| 💾 **Session persistence** | SQLite-backed multi-turn sessions, resumable across restarts |
| 📡 **Streaming** | Text chunks + structured runtime events (tool calls, plan events, permission requests, token usage) |
| 🧠 **Long-context compaction** | Automatic context compression when approaching the model's window (configurable threshold) |
| 📉 **Observability** | Prometheus metrics + OpenTelemetry tracing (OTLP gRPC export, no-op when endpoint not set) |

---

## 🗂️ Repository Structure

```
seshat/
├── cmd/
│   ├── cli/              ← seshat CLI entrypoint (TUI + one-shot commands)
│   └── grpc/             ← gRPC server entrypoint
├── pkg/                  ← public API (safe to import from outside)
│   ├── sdk/              ← Go SDK: Client, sessions, streaming, callbacks
│   ├── types/            ← shared types: Message, ToolUse, TokenUsage, ...
│   ├── agent/            ← agent definitions, built-in registry
│   ├── providers/        ← LLM provider abstraction, routing, fallback
│   ├── mcp/              ← MCP client: stdio, SSE, HTTP transports
│   ├── rag/              ← chunking, embedding, hybrid vector search
│   ├── skills/           ← skill loading, frontmatter parsing, injection
│   ├── memory/           ← in-session state, compaction strategies
│   ├── web/              ← web search, fetch, browser (Playwright)
│   ├── storage/          ← artifact store: S3, local filesystem
│   ├── vector/           ← vector DB abstraction
│   ├── contract/         ← Tool interface, CallResult, registry
│   ├── auth/             ← provider auth abstraction, OAuth device flow
│   ├── workspace/        ← sandbox path resolution, workspace layout
│   ├── monitoring/       ← Prometheus metrics, OTel spans
│   ├── grpc/             ← proto definitions and generated code
│   └── config/           ← app-level config from env
└── internal/             ← private implementation (do not import directly)
```

> SeshatOS, SeshatCloud and any third-party consumer must import `pkg/*` only, never `internal/*`.

---

## 🌐 Supported Providers

| Provider ID | Service | Auth |
|---|---|---|
| `anthropic` | Anthropic | `ANTHROPIC_API_KEY` |
| `openai` | OpenAI | `OPENAI_API_KEY` |
| `gemini` | Google Gemini | `GOOGLE_API_KEY` |
| `mistral` | Mistral AI | `MISTRAL_API_KEY` |
| `deepseek` | DeepSeek | `DEEPSEEK_API_KEY` |
| `ollama` | Ollama (local) | none |
| `openrouter` | OpenRouter | `OPENROUTER_API_KEY` |
| `bedrock` | AWS Bedrock | `AWS_ACCESS_KEY_ID` + region |
| `vertex` | GCP Vertex AI | `ANTHROPIC_VERTEX_PROJECT_ID` + region |
| `foundry` | Azure AI Foundry | `ANTHROPIC_FOUNDRY_API_KEY` |
| `codex` | ChatGPT Pro (OAuth) | device-code flow |
| `minimax` | MiniMax | `MINIMAX_API_KEY` |
| `z-ai` | Z.ai | `Z_AI_API_KEY` |
| `opencode` | OpenCode Zen | `OPENCODE_API_KEY` |
| `workers-ai` | Cloudflare Workers AI | `CLOUDFLARE_API_KEY` |

Full model listings and capabilities: [`docs/providers.md`](./docs/providers.md).

---

## 🚀 Quick Start

```bash
# 1. Install
curl -fsSL https://raw.githubusercontent.com/KPO-Tech/seshat-runtime/main/scripts/install.sh | bash

# Reload your shell (or open a new terminal) if prompted, then:

# 2. Configure your provider
seshat config --provider anthropic --api-key sk-ant-...
seshat config --model anthropic:claude-sonnet-5-5

# 3. Start chatting
seshat chat                          # new session (opens TUI)
seshat chat --continue               # resume last session
seshat chat --resume <session-id>    # resume a specific session
seshat run "list all TODO comments in this codebase"  # one-shot task
```

> **No API key?** Use Ollama for free local inference:
> `seshat config --provider ollama --model ollama:llama3.2`
> (requires [Ollama](https://ollama.com) running locally)

> **Developers building from source:** see the [Development](#️-development) section below.

The same steps are on seshat-ai.com: the [Quick start](https://seshat-ai.com/en/docs/getting-started/quick-start) (resuming a session, the shortcuts of the terminal interface), [Configuration](https://seshat-ai.com/en/docs/getting-started/configuration) (the command line, the environment variables, the providers) and [Your first agent](https://seshat-ai.com/en/docs/getting-started/first-agent) (running a task, adding a skill, calling the runtime from your own code).

---

## ⚡ Skills

Skills are Markdown files that encode expertise injected into the agent's system prompt at runtime.

```
.seshat/skills/
  go-conventions.md     # "always use context.Context as the first parameter..."
  git-workflow.md       # "never commit to main, always open a PR, squash before merge..."
  security-rules.md     # "never log secrets, validate all external input at boundaries..."
```

The official skill collection is [seshat-skills](https://github.com/KPO-Tech/seshat-skills), installable from any URL directly from the CLI.

---

## 🔌 MCP

Any MCP server is immediately usable — no additional development needed.

```go
client, _ := sdk.NewClient(&sdk.ClientConfig{
    MCPServers: []sdk.MCPServerConfig{
        {Name: "github",   Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}},
        {Name: "postgres", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-postgres", "postgresql://..."}},
        {Name: "slack",    Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-slack"}},
    },
})
```

---

## 🏗️ Architecture

<p align="center">
  <img src="docs/images/ideal_vision.png" alt="Seshat Architecture" width="800">
</p>

Full architecture diagrams (Mermaid): [`docs/vision/diagrams.md`](./docs/vision/diagrams.md).

---

## 📖 Documentation

| Doc | What it covers |
|---|---|
| [Vision & Roadmap](./docs/vision/README.md) | Project idea, design principles, Level 1->2->3 roadmap |
| [Architecture](./docs/architecture.md) | System design, layer diagrams, query loop state machine |
| [SDK Guide](./docs/sdk.md) | `ClientConfig`, sessions, streaming, callbacks, MCP |
| [Tools](./docs/tools.md) | Built-in tools reference, permission pipeline |
| [Providers](./docs/providers.md) | Multi-provider routing, retry, circuit breaker |
| [Prompt System](./docs/prompt-system.md) | Section assembly, stage overlays, cache control |
| [Skills](./docs/skills.md) | Skills system, loading order, injection |
| [Transports & Setup](./docs/transports.md) | gRPC setup, proto codegen, env vars |
| [Multi-Agent Teams](./docs/team.md) | Agent profiles, mailbox, dispatcher, TeamBus |
| [Memory and Compaction](./docs/memory.md) | Session memory, context compaction, memory tool |
| [MCP Client](./docs/mcp.md) | MCP protocol, transports, server configuration |
| [RAG System](./docs/rag.md) | Chunking, embeddings, hybrid search, document ingestion |
| [Planning Mode](./docs/planning.md) | Execution modes: execute, plan, pair-programming |

---

## 🛠️ Development

### First-time setup (build from source)

```bash
# Linux / macOS — installs all dependencies, builds, wires git hooks
git clone https://github.com/KPO-Tech/seshat-runtime
cd seshat-runtime
make setup

# Windows (PowerShell — make is not available by default on Windows)
powershell -ExecutionPolicy Bypass -File scripts\setup.ps1
```

`make setup` handles everything: Go version check, ripgrep, and the final build. Binaries land in `bin/`.

### Daily commands

```bash
make build           # build CLI and gRPC binaries to bin/
make test            # run all tests
make test-race       # run tests with race detector
make lint            # golangci-lint
make fmt             # gofmt
make hooks           # (re-)install git pre-commit hooks
make clean           # remove bin/
make clean-runtime   # erase runtime data (~/.config/seshat-cli)
make clean-all       # both
```

### Runtime data directory

Seshat stores sessions and config under a platform-appropriate directory:

| OS | Default path |
|---|---|
| Linux | `~/.config/seshat-cli/` (respects `$XDG_CONFIG_HOME`) |
| macOS | `~/.config/seshat-cli/` |
| Windows | `%APPDATA%\seshat-cli\` |

Override with `SESHAT_RUNTIME_ROOT=/your/path seshat chat`.

### Runtime dependencies

> **ripgrep** — the `glob` and `grep` tools require [`ripgrep`](https://github.com/BurntSushi/ripgrep) (`rg`). Included in `make setup`; install separately with `make install-deps`.

> **A document-reader service** (optional) — for scans and complex layouts, run seshat-intelligence (or any compatible service) yourself and set `DOCUMENT_READER_URL`. The built-in reader needs nothing installed.

### OS compatibility

> **Linux** — primary development and testing platform. Fully supported.
>
> **macOS** — code is written to support macOS and basic testing has been done, but **macOS support is not yet fully validated**. If you hit an issue, please [open a report](https://github.com/KPO-Tech/seshat-runtime/issues).
>
> **Windows** — released builds for amd64 and arm64, and an installer (`scripts/install.ps1`). The terminal interface is used on Windows Terminal; other Windows setups have had less testing. Reports are welcome. To build from source, use `scripts\setup.ps1`.

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the full contribution guide.

---

## 🔒 Security

To report a vulnerability, see [`SECURITY.md`](./SECURITY.md).

---

## 📄 License

[Apache 2.0](./LICENSE). The terminal UI (`internal/seshattui`) was built from Crush and stays subject to its FSL-1.1-MIT licence: see [`NOTICE`](./NOTICE) and [`docs/licensing.md`](./docs/licensing.md).
