# Seshat — Architecture

## Table of Contents

1. [Overview](#1-overview)
2. [Layer Diagram](#2-layer-diagram)
3. [Query Loop — State Machine](#3-query-loop--state-machine)
4. [Prompt Assembly Pipeline](#4-prompt-assembly-pipeline)
5. [Tool Execution Pipeline](#5-tool-execution-pipeline)
6. [Permission Pipeline](#6-permission-pipeline)
7. [Multi-Provider Routing](#7-multi-provider-routing)
8. [Session Lifecycle](#8-session-lifecycle)
9. [Streaming Architecture](#9-streaming-architecture)
10. [Subsystem Reference Map](#10-subsystem-reference-map)

---

## 1. Overview

Seshat is a **headless AI coding runtime**. It connects an LLM provider to a set of tools (file system, bash, web, LSP, etc.) and orchestrates multi-turn conversations where the model can invoke tools, observe results, and continue reasoning.

The engine exposes two built-in entry points. Products built on top of the Go SDK add their own HTTP surfaces: [SeshatOS](https://github.com/KPO-Tech/SeshatOS) (local backend) and [SeshatCloud](https://github.com/KPO-Tech/SeshatCloud) (multi-tenant server).

```
              ┌─────────────┐                           ┌──────────────┐
              │  cmd/cli    │                           │  cmd/grpc    │
              │  Terminal   │                           │  gRPC server │
              │  chat/run   │                           │  port 50051  │
              └──────┬──────┘                           └──────┬───────┘
                    │                                         │
                    └─────────────────┬───────────────────────┘
                                        │
                                  ┌──────▼──────┐
                                  │   pkg/sdk   │
                                  │  Go SDK     │
                                  │  (public)   │
                                  └──────┬──────┘
                                        │
                                  ┌──────▼──────┐
                                  │   Engine    │  ← internal/engine
                                  │   Session   │
                                  └──────┬──────┘
                                        │
                            ┌────────────┼────────────┐
                            │            │            │
                    ┌──────▼───┐  ┌─────▼────┐   ┌───▼──────┐
                    │  Loop    │  │ Prompt   │   │ Providers│
                    │ (Loop.go)│  │ Builder  │   │ (LLM API)│
                    └──────┬───┘  └──────────┘   └──────────┘
                            │
                    ┌──────▼───────┐
                    │  Execution   │  ← internal/execution
                    │ Orchestrator │
                    └──────┬───────┘
                            │
                  ┌─────────┼──────────┐
                  │         │          │
              ┌───▼──┐ ┌───▼──┐ ┌──────▼────┐
              │Tools │ │Perms │ │EventQueue │
              │30+   │ │Engine│ │(streaming)│
              └──────┘ └──────┘ └───────────┘
```

---

## 2. Layer Diagram

The system is organized in four layers:

```
            ╔══════════════════════════════════════════════════════════════════╗
            ║  ENTRY POINTS (seshat)                                     ║
            ║  cmd/cli (terminal) · cmd/grpc (gRPC :50051)                     ║
            ║  + HTTP+SSE lives in SeshatOS / SeshatCloud, uses pkg/sdk         ║
            ╚══════════════════════════╤═══════════════════════════════════════╝
                                      │ uses
            ╔══════════════════════════▼═══════════════════════════════════════╗
            ║  PUBLIC SDK  (pkg/)                                              ║
            ║  sdk.Client · sdk.Session · mcp.Manager · skills.All()           ║
            ╚══════════════════════════╤═══════════════════════════════════════╝
                                      │ wraps
            ╔══════════════════════════▼═══════════════════════════════════════╗
            ║  CORE ENGINE  (internal/engine/)                                 ║
            ║                                                                  ║
            ║  Engine ──creates──▶ Session ──runs──▶ Loop                      ║
            ║                                         │                        ║
            ║  Config · state.go · session_memory.go  │                        ║
            ║  stop_hooks.go · token_budget.go        │                        ║
            ╚══════════════════════════╤══════════════╪════════════════════════╝
                                      │              │ calls
            ╔══════════════════════════▼══════════════▼═══════════════════════╗
            ║  SUBSYSTEMS  (internal/)                                        ║
            ║                                                                 ║
            ║  prompt/    ── assembles system prompt (sections + caching)     ║
            ║  providers/ ── sends API requests (retry + circuit breaker)     ║
            ║  execution/ ── orchestrates tool calls (parallel/serial)        ║
            ║  tools/     ── 30+ tool implementations                         ║
            ║  permissions/ ─ checks tool safety (rules + classifier)         ║
            ║  memory/    ── long-term memory (project/user/cross-session)    ║
            ║  modes/     ── execution modes (plan/execute/browse/pair)       ║
            ║  runtime/   ── compaction + state persistence                   ║
            ║  hooks/     ── lifecycle events                                 ║
            ║  monitoring/ ─ Prometheus metrics                               ║
            ║  db/        ── SQLite (sessions, users, orgs, credentials)      ║
            ║  auth/      ── OAuth + identity                                 ║
            ╚═════════════════════════════════════════════════════════════════╝
```

---

## 3. Query Loop — State Machine

The `Loop` (`internal/engine/loop.go`) is the core state machine. Each call to `Loop.Run()` executes one **turn**: a sequence of model call → tool execution → model call cycles.

```
                    ┌────────────────────────────────────┐
                    │           Loop.Run(req)            │
                    │                                    │
                    │  state = initializeState(req)      │
                    └─────────────┬──────────────────────┘
                                  │
                    ┌─────────────▼───────────────────────────────────────┐
                    │         for i < MaxIterations                       │
                    │                                                     │
                    │  ┌─────────────────────────────────────────────┐    │
                    │  │  1. maybeAutoCompact()                      │    │
                    │  │     compact if context window filling       │    │
                    │  └──────────────────┬──────────────────────────┘    │
                    │                     │                               │
                    │  ┌──────────────────▼──────────────────────────┐    │ 
                    │  │  2. callModel()  → APIResponse              │    │
                    │  │     streaming or non-streaming              │    │
                    │  │     retry on recoverable errors             │    │
                    │  │     fallback to next provider on failure    │    │
                    │  └──────────────────┬──────────────────────────┘    │
                    │                     │                               │
                    │         ┌───────────┴────────────┐                  │
                    │         │ has tool_use blocks?    │                 │
                    │         └────┬──────────┬─────────┘                 │
                    │            YES          NO                          │
                    │             │           │                           │
                    │  ┌──────────▼────┐  ┌──▼───────────────────────┐    │
                    │  │ 3. Execute    │  │ stopReason == end_turn?  │    │
                    │  │    tools      │  └──┬──────────────┬────────┘    │
                    │  │    (parallel/ │    YES             NO            │
                    │  │     serial)   │     │              │             │
                    │  └──────┬────────┘     │   ┌──────────▼──────────┐  │
                    │         │              │   │ shouldNudge         │  │
                    │  append tool results   │   │ Continuation?       │  │
                    │  continue loop ───────────▶│ inject nudge msg    │  │
                    │                         │  │ → continue          │  │
                    │              ┌───────── ▼──▼──────────────────┐  │  │
                    │              │ runStopHooks()                 │  │  │
                    │              │ hook.Continue? → inject + loop │  │  │
                    │              │ else → break                   │  │  │
                    │              └────────────────────────────────┘  │  │
                    └─────────────────────────────────────────────────────┘
                                  │
                    ┌─────────────▼──────────────────────┐
                    │         return RunResult           │
                    │  Messages, StopReason, ToolUses    │
                    │  Usage, RecoveryContext            │
                    └────────────────────────────────────┘
```

### Recovery paths

| Condition | Action |
|---|---|
| `ErrCodeAPIRateLimit` / `ErrCodeAPITimeout` | `tryRecovery()` — exponential backoff, up to `MaxOutputTokensRecoveryLimit` |
| `stop_reason == max_tokens` | Inject continuation message, loop continues |
| Model signals "next step" in text | `shouldNudgeContinuation()` → inject nudge, up to `ContinuationNudgeLimit` |
| All fallback models exhausted | Return error with `RecoveryContext` |

### Transition types

```go
Continue(reason)                           // loop continues normally
ContinueWithRecovery(reason, type)         // loop continues after recovery
Terminate(reason, stopReason)              // loop exits
```

---

## 4. Prompt Assembly Pipeline

The prompt is assembled in two phases each turn:

```
          Phase 1 — FetchSystemPromptParts()  (called once per config change)
          ─────────────────────────────────────────────────────────────────────

            Input: tools, model, stage, toolHints, projectInstructions, memory

            ┌─────────────────────────────────────────────────────────────┐
            │                   STABLE SECTIONS (cacheable)               │
            │                                                             │
            │  Priority  Name                Content                      │
            │  ─────────────────────────────────────────────              │
            │  900       identity            Role: headless AI runtime    │
            │  850       runtime_contract    Treat session as recoverable │
            │  820       working_rules       Read → plan → act order      │
            │  800       tool_use            Prefer tools over text       │
            │  790       output_discipline   Concise, concrete responses  │
            │  780       tool_catalog        Generated tool list          │
            │                                                             │
            │  ─ ─ ─ ─ ─ ─ CACHE BOUNDARY ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─       │
            │                                                             │
            │                   DYNAMIC SECTIONS (per-turn)               │
            │                                                             │
            │  780       runtime_guidance    CWD, session/turn IDs, mode  │
            │  775       project_instructions SESHAT.md / AGENTS.md        │
            │  770       stage_overlay       Stage-specific guidance      │
            │  750       runtime_context     Date, model, tool list       │
            │  730       runtime_memory      Long-term memory context     │
            └─────────────────────────────────────────────────────────────┘

          Phase 2 — BuildCanonicalPrompt()  (each turn, uses parts from phase 1)
          ─────────────────────────────────────────────────────────────────────

            Variables injected:
            • {{cwd}}                working directory
            • {{session_id}}         session identifier
            • {{turn_id}}            current turn identifier
            • {{model}}              active model name
            • {{date}}               ISO date
            • {{memory_context}}     formatted memory entries
            • {{project_instructions_block}}  SESHAT.md content (if present)

            Output: CacheSafePrompt
            • SystemPrompt          full assembled string for provider
            • SystemPromptBlocks    structured blocks with cache_control markers
            • CacheBreakpoint       index of first dynamic block
```

### Stage overlays

When `PromptStage` is set, an additional section is injected at priority 770:

| Stage | Guidance injected |
|---|---|
| `tool_call` | Guidance for invoking tools precisely |
| `tool_result` | Guidance for interpreting tool output |
| `continuation` | Guidance for resuming after interruption |
| `plan` | Plan-mode guidance (describe, do not execute) |

### Project instructions

The engine reads instruction files from the working directory in priority order:

```
1. SESHAT.md
2. AGENTS.md
3. .seshat/instructions.md
```

Content is capped at 32 KB with line-boundary truncation.

---

## 5. Tool Execution Pipeline

Every tool invocation passes through the Orchestrator (`internal/execution/`):

```
                Orchestrator.Execute(ctx, req)
                      │
                      ▼
                ┌───────────────────────────────────────────────────────────┐
                │  Partition tool uses by concurrency safety                │
                │                                                           │
                │  IsConcurrencySafe(input) == true  ──▶  concurrent batch  │
                │  IsConcurrencySafe(input) == false ──▶  serial batch      │  
                └───────────────────────────────────────────────────────────┘
                      │
                      ▼
                For each tool use:
                ┌─────────────────────────────────────────────┐
                │  1. Resolve tool from registry              │
                │  2. tool.IsEnabled()                        │
                │  3. tool.ValidateInput()                    │
                │  4. tool.BackfillInput()  (enrich for perms)│
                │  5. Pre-tool hooks                          │
                │  6. ─────── PERMISSION PIPELINE ──────────  │
                │  7. tool.Call()                             │
                │  8. Post-tool hooks                         │
                │  9. tool.FormatResult()                     │
                │  10. Content size limits                    │
                │  11. Context modifier (serial: live update, │
                │       concurrent: batch-ordered after all)  │
                └─────────────────────────────────────────────┘
                      │
                      ▼
                ExecuteResult
                • Results []CallResult
                • Traces  []ToolExecutionTrace
                • Messages (tool_result blocks for conversation)
                • TotalDuration
```

### Tool categories

```
internal/tools/
├── files/          read, edit, write, glob, grep, notebook_edit
├── bash/           shell execution with safety scanner
├── web/            fetch (with markdown conversion), search (DuckDuckGo, Exa, Tavily)
├── special/        ask_user, todo, lsp, research, worktree, wikipedia, tree, monitor
├── system/         mcp, plan (enter/exit), pair_programming, skills
├── task/           taskCreate, taskList, taskGet, taskUpdate, taskStop, taskOutput
└── agent/          sub-agent / skill runner
```

### Tool contract interface

```go
type Tool interface {
    Definition()   tool.Definition     // name, description, JSON schema
    Call(ctx, input, permCheck)        // execute
    ValidateInput(ctx, input)          // normalize + validate
    BackfillInput(ctx, input)          // enrich before permission check
    CheckPermissions(ctx, input, ctx)  // tool-owned permission decision
    IsConcurrencySafe(input) bool      // can run in parallel with other tools?
    IsReadOnly(input) bool             // does not modify state?
    IsEnabled() bool                   // registered and available?
    FormatResult(data) string          // serialize result for transcript
}

// Optional capabilities
type PermissionMatcherTool interface {
    PreparePermissionMatcher(ctx, input) matchFn
}
type PlanModeExecutableTool interface {
    ExecutesInPlanMode(input) bool     // exempt from plan-mode blocking
}
```

---

## 6. Permission Pipeline

Permissions are checked before `tool.Call()`. The pipeline has several layers:

```
                    Tool use arrives
                          │
                          ▼
                    ┌─────────────────────────────────────────────────┐
                    │  1. Deny rules (from filesystem/DB/API)         │
                    │     • Path patterns for file tools              │
                    │     • Command patterns for bash                 │
                    │     • Explicit deny entries                     │
                    └────────────────────┬────────────────────────────┘
                                        │ no explicit deny
                                        ▼
                    ┌─────────────────────────────────────────────────┐
                    │  2. tool.CheckPermissions()                     │
                    │     Tool-owned check (e.g. bash safety scanner) │
                    └────────────────────┬────────────────────────────┘
                                        │ allowed
                                        ▼
                    ┌─────────────────────────────────────────────────┐
                    │  3. Always-allow rules                          │
                    │     (read-only tools, explicitly safe patterns) │
                    └────────────────────┬────────────────────────────┘
                                        │ not always-allow
                                        ▼
                    ┌─────────────────────────────────────────────────┐
                    │  4. Permission mode gate                        │
                    │                                                 │
                    │  bypass ──▶ allow immediately                   │
                    │  never  ──▶ deny immediately                    │
                    │  auto   ──▶ classifier decision (ML)            │
                    │  onRequest ▶ ask user                           │
                    └────────────────────┬────────────────────────────┘
                                        │
                                        ▼
                                    allow / deny / ask
```

### Permission modes

| Mode | Behavior |
|---|---|
| `bypass` | Skip all checks — for trusted automation |
| `never` | Deny all non-trivially-safe tool uses |
| `auto` | ML classifier auto-approves based on transcript context |
| `acceptEdits` | Auto-approve safe file operations in working dir |
| `onRequest` | Ask user for explicit approval |
| `granular` | Fine-grained control per category |

### Denial tracking

When the classifier denies too often (threshold in `LoopConfig`), the engine gracefully degrades the mode: `auto → onRequest → deny`. The denial count is tracked per session in `DenialTrackingState`.

---

## 7. Multi-Provider Routing

The `providers.Client` supports 11 providers and a fallback chain:

```
                Loop.callModel()
                      │
                      ▼
                ┌─────────────────────────────────────────────────────┐
                │  primaryClient.CreateMessageStreamResultWithCallback│
                │  (Anthropic / OpenAI / Gemini / OpenAI-compatible…) │
                └──────────────────────┬──────────────────────────────┘
                                      │ error?
                                      ▼
                ┌─────────────────────────────────────────────────────┐
                │  isRecoverableError()?                              │
                │  • ErrCodeAPIRateLimit                              │
                │  • ErrCodeAPITimeout                                │
                │  • ClassifyHTTPError → Network / ServerOverload     │
                └──────────────────────┬──────────────────────────────┘
                                      │ yes
                                      ▼
                ┌─────────────────────────────────────────────────────┐
                │  tryFallbackModel()                                 │
                │  iterate providerConfig.Routing.FallbackModels      │
                └──────────────────────┬──────────────────────────────┘
                                      │ all models exhausted
                                      ▼
                ┌─────────────────────────────────────────────────────┐
                │  tryFallbackProvider()                              │
                │  iterate providerConfig.Routing.FallbackProviders   │
                │  NewFallbackClient() per provider                   │
                └──────────────────────┬──────────────────────────────┘
                                      │ all providers exhausted
                                      ▼
                                  return error
```

### Retry within a single provider

```
      RetryHTTP(ctx, config, sendFn, errorFn, circuitOpenFn)
          │
          ├── attempt 1 ── send ── OK? ──▶ return response
          │                 │
          │                 └── error? ── ClassifyHTTPError()
          │                               │
          │                 ┌─────────────┴───────────────────┐
          │                 │ ClientError / AuthError         │ no retry
          │                 │ RateLimit / Network / Server…   │ retry after backoff
          │                 └─────────────────────────────────┘
          │
          ├── attempt 2 ── calculateHTTPBackoff(attempt) ── send …
          │                  (exponential + ±25% jitter)
          └── attempt N ── max attempts reached ── return error
```

### Circuit breaker

```
      Closed ──[failures >= threshold]──▶ Open ──[timeout]──▶ Half-Open
        ▲                                                          │
        └──────────────[success]───────────────────────────────────┘
```

When the circuit is Open, requests fail fast without hitting the provider. After the timeout, one probe request is sent; on success the circuit closes again.

### Supported providers

| Provider | Auth | Notes |
|---|---|---|
| `anthropic` | API key | Direct Messages API, prompt caching |
| `openai` | API key | GPT-4o, o1, o3 |
| `gemini` | API key | Google Gemini |
| `ollama` | none | Local inference |
| `openrouter` | API key | Unified multi-provider |
| `zai` | API key | GLM / Z.ai |
| `minimax` | API key | MiniMax |

---

## 8. Session Lifecycle

```
          sdk.NewClient(config)
                │ initializes subsystems:
                │ engine, providers, tools, memory, monitoring, db
                ▼
          client.CreateSession(ctx)
                │ engine.NewSession()
                │ generates SessionID
                ▼
          session.SubmitMessage(ctx, userMessage)
                │
                │ 1. Build system prompt (FetchSystemPromptParts + BuildCanonicalPrompt)
                │ 2. Append user message to transcript
                │ 3. Loop.Run(RunRequest)
                │      │
                │      └── model calls + tool execution + streaming
                │
                │ 4. Persist session to store (SQLite or filesystem)
                │ 5. Update session metadata (turns, tokens, stop reason)
                ▼
          SessionResponse
          • Messages []types.Message
          • StopReason string
          • TurnNumber int
          • Usage *types.TokenUsage
          • ToolUses, ToolResults

          (repeat SubmitMessage for multi-turn)

          session.Close()
                │ closes EventQueue
                │ removes from active sessions map
```

### Session persistence

State is saved after each turn to the configured backend:

| Backend | Path | Notes |
|---|---|---|
| SQLite | `~/.seshat/sessions.db` | Default; supports `session_metadata`, `session_transcript_entries`, `session_checkpoints` |
| Filesystem | `workspaces/{id}/` | JSON files per session |
| Memory | — | Testing only |

### Compaction

When the transcript approaches the context window, `compact.Engine` replaces older messages with an LLM-generated summary:

```
  [msg1][msg2]…[msgN]  →  [SUMMARY: first N-k turns][msgN-k+1]…[msgN]
```

Configurable thresholds: `AutoCompactThreshold` (%), `CompactTargetPercentage` (%), `MaxSummaryTokens`.

---

## 9. Streaming Architecture

Streaming flows through two parallel channels:

```
  Provider streams chunks
          │
          ▼
  client.CreateMessageStreamResultWithCallback(ctx, req, onChunk)
          │
          ▼  onChunk called for each APIResponseChunk
          │
  buildChunkCallback(ResponseChunkCallback, EventQueue)
          │
          ├──▶  ResponseChunkCallback(chunk)  ← direct callback (sync)
          │
          └──▶  EventQueue.Emit(chunk)        ← buffered channel (non-blocking)
                    │
                    │  readers drain via:
                    ├──▶  for c := range queue.Recv() { … }  ← in-process consumer
                    │
                    └──▶  HTTP SSE handler                   ← POST /api/v1/query/stream
```

### EventQueue

```go
type EventQueue struct {
    ch       chan APIResponseChunk  // buffered, capacity 1000
    emitted  atomic.Int64
    overflow atomic.Int64
    closed   atomic.Bool
}
```

- `Emit(chunk)` — non-blocking, drops + increments overflow if full
- `EmitBlocking(ctx, chunk)` — blocks until room or ctx cancelled
- `Recv()` — read-only channel; ranging over it drains remaining items after Close
- `Close()` — idempotent; signals readers the stream is done

### SSE endpoint

`POST /api/v1/query/stream`:

```
Request:  { "prompt": "…", "session_id": "…" }

Stream:
  data: {"type":"chunk","chunk_type":"content_block_delta","delta":"hel","delta_type":"text_delta"}

  data: {"type":"chunk","chunk_type":"content_block_delta","delta":"lo","delta_type":"text_delta"}

  event: done
  data: {"session_id":"…","content":"hello","stop_reason":"end_turn","turn_number":1,…}

  (on error):
  event: error
  data: {"error":"provider unavailable"}
```

Headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`

---

## 10. Subsystem Reference Map

This table maps each capability to its primary location in the source tree.

| Capability | Package | Key file |
|---|---|---|
| Session lifecycle | `internal/engine` | `engine.go` |
| Query loop state machine | `internal/engine` | `loop.go` |
| Loop configuration | `internal/engine` | `config.go`, `loop.go:LoopConfig` |
| Session state | `internal/engine` | `state.go` |
| Recovery context | `internal/engine` | `loop.go:RecoveryContext` |
| Compaction hook | `internal/engine` | `loop.go:maybeAutoCompact` |
| Stop hooks | `internal/engine` | `stop_hooks.go` |
| Token budget | `internal/engine` | `token_budget.go` |
| Streaming tool coordination | `internal/engine` | `streaming_tools.go` |
| Memory integration | `internal/engine` | `session_memory.go` |
| Prompt sections | `internal/prompt` | `types.go` |
| Prompt assembler | `internal/prompt` | `assembler.go` |
| Prompt builder | `internal/prompt` | `builder.go` |
| Stage overlays | `internal/prompt` | `stages.go` |
| Prompt cache | `internal/prompt` | `cache.go` |
| Provider client | `internal/providers` | `client.go` |
| Retry (HTTP level) | `internal/providers/retry` | `retry.go` |
| Retry strategy (loop level) | `internal/providers/retry` | `config.go`, `retry.go` |
| Circuit breaker | `internal/providers` | `circuit_breaker.go` |
| Streaming transport | `internal/providers/transport` | `stream_helpers.go` |
| Tool orchestrator | `internal/execution` | `orchestrator.go` |
| Event queue | `internal/execution` | `event_queue.go` |
| Tool batching | `internal/execution` | `batch.go` |
| Tool contract | `internal/tools/contract` | `interface.go` |
| Tool registry | `internal/tools/registry` | `registry.go` |
| File tools | `internal/tools/files` | `read/`, `edit/`, `write/` |
| Bash tool | `internal/tools/bash` | `bash.go` |
| Web tools | `internal/tools/web` | `fetch/`, `search/` |
| Special tools | `internal/tools/special` | `lsp/`, `todo/`, `research/`, … |
| System tools | `internal/tools/system` | `mcp/`, `plan/`, `skills/` |
| Permission engine | `internal/permissions` | `engine.go` |
| Permission integrator | `internal/permissions` | `integration.go` |
| Auto-mode classifier | `internal/permissions/auto` | `classifier.go` |
| Denial tracking | `internal/permissions` | `denialTracking.go` |
| Memory service | `internal/memory` | `manager.go` |
| Hook registry | `internal/hooks` | `registry.go` |
| Hook executor | `internal/hooks` | `executor.go` |
| Execution modes | `internal/modes` | `execution.go` |
| Compaction engine | `internal/runtime/memory` | `engine.go` |
| State backend (SQLite) | `internal/runtime/state` | `sqlite_backend.go` |
| Database schema | `internal/db` | `schema.go` |
| Session store | `internal/db` | `session_store.go` |
| Identity store | `internal/db` | `identity.go` |
| Monitoring system | `internal/monitoring` | `system.go` |
| Metrics types | `internal/monitoring` | `metrics.go` |
| SDK client | `pkg/sdk` | `client.go` |
| SDK prompt config | `pkg/sdk` | `prompt_config.go` |
| MCP manager | `pkg/mcp` | `mcp.go` |
| Skills system | `pkg/skills` | `skills.go` |
| Config loader | `pkg/config` | `config.go` |
| Core types | `internal/types` | `api.go`, `errors.go`, `permissions.go` |
