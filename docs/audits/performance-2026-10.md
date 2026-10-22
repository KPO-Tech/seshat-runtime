# Performance audit, October 2026

What a turn costs in the desktop application, measured on a real conversation (z.ai glm-5.1, Windows, a hard disk), where the time goes, what was changed and what is left. Every figure below was measured; the harness is `pkg/sdk/perf_audit_test.go` (set `AUDIT=1`) and the backend's own `[perf]` log lines.

## What one turn is made of

For a one word message ("reply ok") with a warm backend:

| Step | Time | Where |
|---|---|---|
| Backend before the model call (client from the cache, session load, context build) | 10 to 130 ms | `[perf] stream_prompt`: `client_build_ms`, `session_load_ms`, `time_to_first_trace_ms` |
| Same, the first turn after a start or a model change (new client) | 0.3 to 2 s | `sdk.NewClient`: `registry_ms` 0.3 to 1.9 s (a cold start of the process: 0.9 s of it is the Docker check), `mcp_ms` 0.1 to 0.2 s per stdio server |
| Time to the first chunk of the answer | 4 to 9 s | the provider (z.ai glm-5.1); with no tool definitions in the request it was 3.8 to 5.0 s, with them 4.2 to 7.4 s |
| A tool call in Execute mode, permission check | was 34 s, now 7 to 10 s | the auto-mode classifier (two model calls) |
| A tool that is deferred and used | one extra model round trip (5 to 10 s) | `tool_search`, then the call |

The backend is not the bottleneck: its own work before the model call is under a tenth of a second once the client is cached. The time is in the provider, in the number of model round trips a task needs, and in what each request carries.

## What each request carries

A turn with one short message sends about 20,250 tokens before the model reads a word of it:

- system prompt: 13.7 KB, about 3.4k tokens (a "runtime context" block of 4.3 KB, the rest is stable);
- 36 tool definitions: 67 KB, about 16.8k tokens. The heaviest: `bash` 1.5k, `agent` 1.4k, `grep` 1.05k, `read_file` 1.0k, `ask_user_question` 1.0k, `task_update` 0.9k, `enter_plan_mode` 0.7k, `task_create` 0.7k, `web_fetch` 0.7k, `request_permissions` 0.7k, `glob` 0.55k, `web_search` 0.55k.

Removing all of them cut the time to the first chunk by about 1.5 s on z.ai (about 0.1 s per 1,000 tokens). On a provider that is quick the share is larger, and on a small local model the prompt is the whole problem: a model with an 8k or 16k window cannot take a 20k request at all.

## Changed

1. **The auto-mode classifier no longer lets the model think** (1.11.7). Both stages passed no switch to turn off the provider's hidden reasoning, and the request was always addressed to Anthropic, so the switch never reached the adapter. Classification of one tool call: 34 s to 10 s, the turn 74 s to 33 s.
2. **A small MCP server is loaded with every request** (this change). An MCP tool was deferred, so each use started with a `tool_search` round trip. A server whose definitions fit in 3,000 bytes (about 750 tokens) is now loaded with the request, and the host's allowlist of tools no longer hides it. A larger server stays deferred. One tool call against a one tool server: the turn 33 s to 21 s, and no search.
3. **The `skill` tool is loaded when there are skills to offer** (this change). It was deferred, so each use of a skill started with a `tool_search` round trip, and the model did not know which skills existed. Its description is the index of the skills, already capped at 8,000 characters (about 2k tokens). With no skill it stays deferred. A skill turn: 28.8 s to 12.1 s.

## Left, by value

1. **A compact tool profile for small context windows.** The 20k token request does not fit a local model with an 8k to 16k window. For those, load about seven tools (read, write, edit, glob, grep, bash, tool_search) and keep the rest deferred; the host picks the profile from the window of the model. Roughly 5k tokens of definitions instead of 16.8k.
2. **Trim the twelve heaviest tool descriptions** by a third (examples and repeated guidance), about 3 to 4k tokens on every request. Needs a before and after check on tool use quality, so it is not done blindly.
3. **Fewer model round trips.** Each one costs 5 to 10 s on this provider. Candidates: telling the model in the system prompt which deferred tool matches the message (the short description catalog already lists them), loading the tools a conversation used last turn without a new search, and answering the permission question without a model for the calls the person already allowed in the same conversation.
4. **Remember the person's approval of an MCP server's tools** for the conversation or for good, so the classifier is not asked again for a server they chose to install.
5. **Client build on the first turn** (0.3 to 2 s): the registry (`registry_ms`) rebuilds every built-in tool for each new client; most of it can be shared between clients, and the MCP servers are spawned again for each new client.
6. **Reasoning models.** On "reply ok" glm-5.1 produced no thinking chunks, so switching reasoning off changed nothing there; on harder turns it may. A per-conversation "think less" setting is the lever a person can use.

## Not a problem

The `session_load_ms` of 1.3 to 2.1 s seen in earlier logs happened while tests and builds were using the same hard disk; with the machine idle it is 1 to 12 ms. `resolveBrowser` is 100 ms on the first call and about 1 ms after. The skills of external folders are read on each turn but add nothing measurable (`skills_ms=0`).
