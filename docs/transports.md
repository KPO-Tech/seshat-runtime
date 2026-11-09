# Transports & Setup

This document covers the gRPC server, proto codegen, and environment configuration for seshat.

> The HTTP REST/SSE API (`cmd/api`) is part of the products built on seshat — **[SeshatOS](https://github.com/KPO-Tech/SeshatOS)** (local backend) and **[SeshatCloud](https://github.com/KPO-Tech/SeshatCloud)** (multi-tenant server) — not seshat itself. See their documentation for that surface.

---

## Prerequisites

### Go

The module requires Go 1.25+. Check your version:

```bash
go version
```

### Proto codegen (only needed if modifying the .proto file)

`make proto` regenerates both the Go and the Python stubs, and uses the `protoc`
inside `grpcio-tools` when there is no system one (`pip install grpcio-tools`).
The commands below install a system `protoc` and the Go plugins.

```bash
# Check tools
protoc --version
which protoc-gen-go
which protoc-gen-go-grpc

# Install protoc (Linux)
mkdir -p "$HOME/.local"
curl -LO https://github.com/protocolbuffers/protobuf/releases/download/v34.1/protoc-34.1-linux-x86_64.zip
unzip -o protoc-34.1-linux-x86_64.zip -d "$HOME/.local"

# Install Go plugins
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

export PATH="$HOME/.local/bin:$(go env GOPATH)/bin:$PATH"
```

### grpcurl (for manual testing)

```bash
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
```

---

## Environment variables

### Provider credentials

```bash
ANTHROPIC_API_KEY=sk-ant-...
OPENAI_API_KEY=sk-...
GOOGLE_API_KEY=...
OLLAMA_API_KEY=...            # optional, only needed if Ollama requires auth
```

### Runtime configuration

```bash
SESHAT_MODEL=anthropic:claude-sonnet-5-5   # default model (provider:model)
SESHAT_API_KEY=...                          # alternative to provider-specific key
SESHAT_CWD=/path/to/working/directory      # agent working directory
SESHAT_DB_PATH=/tmp/seshat/seshat.sqlite     # SQLite database path
SESHAT_DEBUG=true                          # verbose logging
SESHAT_PROVIDER_BASE_URL=...               # custom provider base URL
WEB_SEARCH_PROVIDER=tavily                # web search provider
ENABLE_TOOL_SEARCH=true                   # hide deferred tools (MCP, skill) behind the tool_search tool; pkg/sdk.ClientConfig.EnableToolSearch overrides this when set to true
SESHAT_STREAM_IDLE_TIMEOUT=180            # seconds a streamed response may stay silent before the request is given up on and retried (default 180; 0 turns it off)
```

The config loader reads, in order: environment variables → `.env` in the current directory → `~/.seshat.yaml`.

---

## gRPC server

### Start

```bash
go run ./cmd/grpc   # 127.0.0.1:50051
# or
make build-grpc && ./bin/seshat-grpc
```

### Security and configuration

The server runs an agent for whoever can call it, with the provider keys of the host, and `ConnectMCP` can start a process. It is closed by default:

| Variable | Default | Effect |
|---|---|---|
| `SESHAT_GRPC_HOST` | `127.0.0.1` | Address to listen on. Anything but the loopback (`0.0.0.0`, a LAN address) needs the token below, or `SESHAT_GRPC_ALLOW_INSECURE_REMOTE=true`: the server refuses to start otherwise. In a container, set `SESHAT_GRPC_HOST=0.0.0.0` and a token. |
| `SESHAT_GRPC_PORT` | `50051` | Port. |
| `SESHAT_GRPC_AUTH_TOKEN` | empty | When set, every call but `HealthCheck` must carry the metadata `authorization: Bearer <token>`, or fail with `Unauthenticated`. |
| `SESHAT_GRPC_TLS_CERT`, `SESHAT_GRPC_TLS_KEY` | empty | Turn TLS on (both or neither). The token is a secret: over a network, send it only over TLS (the server warns when it is not). |
| `SESHAT_GRPC_ALLOW_INSECURE_REMOTE` | `false` | Accept a non-loopback address with no token, because the network in front of the server is the protection. |
| `SESHAT_GRPC_ALLOW_STDIO_MCP` | `false` | Let `ConnectMCP` start a stdio MCP server, which is a command line (`command`, `args`, `env`) run on the host. Refused with `PermissionDenied` otherwise; `http`, `sse` and `ws` servers are not affected. |
| `SESHAT_GRPC_INTERACTION_TIMEOUT` | `5m` | How long a turn waits for the answer to a `permission_request` or a `prompt_request` before that tool call fails with `DeadlineExceeded`. A duration (`90s`, `10m`); anything it cannot parse falls back to the default. |
| `SESHAT_GRPC_ENABLE_REFLECTION` | `false` | Server reflection. |
| `SESHAT_GRPC_MAX_CONCURRENT_RPCS`, `SESHAT_GRPC_KEEPALIVE_TIME` | `10`, `30s` | Limits. |

With a token, a client passes it as metadata: `grpcurl -H "authorization: Bearer $TOKEN" ...`, or in Go `metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)`.

### Services

Defined in `pkg/grpc/proto/seshat.proto`: four services that together cover the
runtime. The engine stays on the server; a client only sends these calls.

#### `seshat.SeshatService` — the server

| Method | Type | Description |
|---|---|---|
| `HealthCheck` | unary | Liveness, version, uptime. The only call that needs no token |
| `GetServerInfo` | unary | Version, model, working directory, enabled features, providers configured |
| `GetModels` | unary | Models of the provider registry (with `context_window`) and the providers with whether each has a credential |
| `ListSkills`, `GetSkillDetails` | unary | Skills of the runtime, optionally with their content |
| `ListMCP`, `ConnectMCP`, `DisconnectMCP`, `ReconnectMCP` | unary | MCP servers: list, start, stop, restart |
| `CreateSession` | unary | Open a conversation; the returned `Session` is attached to this server |
| `ListSessions`, `GetSession`, `SearchSessions`, `DeleteSession` | unary | Stored conversations |
| `Query`, `QueryStream` | unary, streaming | Legacy single-turn query on a throwaway session |

#### `seshat.SessionService` — one conversation

| Method | Type | Description |
|---|---|---|
| `SubmitMessage` | unary | Run a turn, return its `TurnResult` |
| `SubmitMessageStream` | server-streaming | Run a turn, stream every `SessionEvent`. Ends with `TurnCompleted` or `TurnFailed` |
| `Subscribe` | server-streaming | Follow the events of a turn without starting one |
| `GetMessages`, `GetHistory` | unary | What the model sees / the whole transcript, including compacted turns |
| `GetState` | unary | Modes, counters, tools, whether auto mode is available |
| `Interrupt`, `Compact`, `Close` | unary | Stop the running turn, compact now, end the session |
| `SetPermissionMode`, `SetWorkingDirectory`, `SetSystemPrompt`, `SetPlanMode`, `ApprovePlan` | unary | Change the session |
| `ListTools`, `SetTools` | unary | Tools of the session; `SetTools` restricts to a set |
| `RespondPermission`, `RespondPrompt` | unary | Answer a `permission_request` / `prompt_request` |

#### `seshat.MemoryService`

| Method | Description |
|---|---|
| `UpsertEntities`, `AddObservations` | Create entities (deduplicated by name) and append observations |
| `SearchMemory`, `OpenEntities` | Search names, types and observations / open exact names with their relations |
| `RetrieveContext` | The Markdown block the runtime injects for a query, capped in tokens |

#### `seshat.WorkflowService`

| Method | Description |
|---|---|
| `ValidateWorkflow` | Check a definition written as YAML or JSON |
| `DraftWorkflow` | Normalise a definition and render it back |
| `RunWorkflow` | Run it: each node is a turn of an agent, run when its needs have succeeded |

Memory and workflows take a `user_id`, blank for the shared user: authentication
is one token for the whole server (see above), not per-user, so put the server
behind your own gateway if callers must be told apart.

### Events

`SubmitMessageStream` and `Subscribe` send `SessionEvent`: `event_id`,
`session_id`, a `sequence` that grows by one per session (so a reconnecting
client can ask for what it missed), `turn_number`, `type` in the engine's own
vocabulary (`turn.started`, `response.chunk`, `tool.started`, `plan.ready`…),
and the payload field that matches it, e.g. `chunk` for `response.chunk` or
`turn_completed` for `turn.completed`. An older client must treat an unknown
payload or an unknown string as opaque text: the engine grows, the contract only
adds.

### Approvals, over the wire

The engine's `PromptFn` blocks inside the turn, so the server turns it into an
event and a call:

1. a tool needs a decision: the server sends `permission_request` with a
   `request_id`, `tool_name` and `tool_input`, then waits;
2. the client answers on any connection to the same server with
   `RespondPermission` (`allow`, `allow_session`, `deny`) or `RespondPrompt`
   (a value, or `cancelled`);
3. a request nobody answers within `SESHAT_GRPC_INTERACTION_TIMEOUT` fails that
   tool call with `DeadlineExceeded`; a request whose stream is cancelled is
   denied.

A unary `SubmitMessage` cannot answer: an interactive turn belongs on
`SubmitMessageStream`, or on another connection that holds the `request_id`.

> Note: server reflection is off by default (`SESHAT_GRPC_ENABLE_REFLECTION=true` turns it on). Otherwise provide the `.proto` file explicitly, as `grpcurl` calls below do.

> Note: `QueryRequest.stream` and `QueryRequest.temperature` are deprecated and ignored; `Query` and `QueryStream` are kept for clients written against the previous contract. Prefer `CreateSession` + `SubmitMessage` and `SubmitMessageStream`: one engine per conversation instead of one per call, and approvals that can be answered.

### Query (unary)

```bash
grpcurl \
  -plaintext \
  -import-path pkg/grpc/proto \
  -proto seshat.proto \
  -d '{"prompt":"hello","model":"anthropic:claude-sonnet-5-5"}' \
  localhost:50051 seshat.SeshatService/Query
```

### QueryStream (server-streaming)

The stream emits `QueryResponse` messages with an `item_type` field:

| `item_type` | Fields | Description |
|---|---|---|
| `chunk` | `content`, `chunk.type`, `chunk.delta_type`, `chunk.delta` | Text delta |
| `runtime_event` | `runtime_event.type`, `.session_id`, `.turn_number`, `.tool_name`, `.stop_reason`, … | Structured engine event |
| `final` | `conversation_id`, `content`, `token_usage`, `stopped` | Final result |

```bash
grpcurl \
  -plaintext \
  -import-path pkg/grpc/proto \
  -proto seshat.proto \
  -d '{"prompt":"hello","model":"anthropic:claude-sonnet-5-5"}' \
  localhost:50051 seshat.SeshatService/QueryStream
```

### A conversation

A conversation outlives the call that opened it, and a turn streams its events:

```bash
# open one; the id is in the answer
SESSION=$(grpcurl -plaintext -import-path pkg/grpc/proto -proto seshat.proto \
  -d '{"model":"anthropic:claude-sonnet-5-5"}' \
  localhost:50051 seshat.SeshatService/CreateSession | sed -n 's/.*"id": "\(s[^"]*\)".*/\1/p')

grpcurl -plaintext -import-path pkg/grpc/proto -proto seshat.proto \
  -d "{\"session_id\":\"$SESSION\",\"text\":\"hello\"}" \
  localhost:50051 seshat.SessionService/SubmitMessageStream

# what it cost, and what it holds
grpcurl -plaintext -import-path pkg/grpc/proto -proto seshat.proto \
  -d "{\"session_id\":\"$SESSION\"}" localhost:50051 seshat.SessionService/GetState
```

A turn that needs an approval sends `permission_request` in that stream; answer it
with `RespondPermission`, carrying the `request_id` it gave. A unary
`SubmitMessage` cannot do that, which is why the stream exists.

#### What `CreateSession` configures

A session is described completely when it is opened: a client does not have to
inherit the server's idea of an agent.

| Field | Effect |
|---|---|
| `model`, `api_key`, `max_tokens` | the model, a credential for this session only, the output ceiling of a turn |
| `system_prompt_template`, `append_system_prompt` | replace the system prompt, or add to it |
| `tools` | restrict the agent to these tool names; empty offers every enabled tool |
| `working_dir` | the folder the file and shell tools work in |
| `permission_mode` | `bypass`, `auto`, `acceptEdits`, `onRequest` (default), `never`, `granular` |
| `max_iterations`, `max_turns` | how many model calls a turn, and turns a session, may run |
| `turn_token_budget`, `max_consecutive_denials` | the budget of a turn, and the refusals after which it stops asking |
| `auto_compact`, `enable_tool_search` | `optional bool`: unset keeps the server's answer, so "off" can be said |

Every limit is optional in the same way: `0` (or unset) keeps the server's value,
and a negative number is refused with `InvalidArgument`. What cannot be sent is
the host's process environment, its Go callbacks (hooks, converters) and its
sub-agent definitions: those belong to the machine that runs the server. For your
own code, write an MCP server and connect it.

### Other methods

```bash
# Health check
grpcurl -plaintext -import-path pkg/grpc/proto -proto seshat.proto \
  -d '{}' localhost:50051 seshat.SeshatService/HealthCheck

# Available models
grpcurl -plaintext -import-path pkg/grpc/proto -proto seshat.proto \
  -d '{}' localhost:50051 seshat.SeshatService/GetModels

# List skills
grpcurl -plaintext -import-path pkg/grpc/proto -proto seshat.proto \
  -d '{}' localhost:50051 seshat.SeshatService/ListSkills
```

---

## Proto codegen

`pkg/grpc/proto/seshat.proto` is the single source of the contract: the Go
server and the [Python client](../python/README.md) are both generated from it,
so neither can drift.

```bash
make proto    # pkg/grpc/seshat (Go) and python/seshat/_proto (Python)
```

It uses `protoc` when it is on `PATH` and the one inside `grpcio-tools`
(`pip install grpcio-tools`) otherwise, with the Go plugins from
`protoc-gen-go` and `protoc-gen-go-grpc`. By hand, the same thing:

```bash
PROTO_INC=$(python -c 'import grpc_tools, os; print(os.path.join(os.path.dirname(grpc_tools.__file__), "_proto"))')
python -m grpc_tools.protoc -I pkg/grpc/proto -I "$PROTO_INC" \
  --go_out=. --go_opt=module=github.com/KPO-Tech/seshat-runtime \
  --go-grpc_out=. --go-grpc_opt=module=github.com/KPO-Tech/seshat-runtime \
  --python_out=python/seshat/_proto --pyi_out=python/seshat/_proto \
  --grpc_python_out=python/seshat/_proto \
  pkg/grpc/proto/seshat.proto
```

The generated `seshat_pb2_grpc.py` imports its own module absolutely, which breaks
once it is a package: `make proto` rewrites that line to a relative import.

Verify:

```bash
go build ./cmd/grpc ./pkg/grpc/...
go test ./cmd/grpc/... ./pkg/grpc/...
make test-python
```

---

## Python client

The `python/` folder is a client, installable with `pip install ./python` and
imported as `seshat`. It is a thin layer over this contract: `grpcio` and
compiled protobuf, no engine, no subprocess.

```bash
pip install ./python
make test-python
```

```python
import seshat

with seshat.connect() as client:
    print(client.info().model)
    with client.create_session() as session:
        for event in session.stream("hello", on_permission=lambda request: "allow"):
            if event.kind == "chunk":
                print(event.text, end="")
```

`seshat.aio` is the same surface on `grpc.aio`. See
[`python/README.md`](../python/README.md) for the whole API.

---

## Smoke tests

```bash
# gRPC
go test ./cmd/grpc/...
go test ./pkg/grpc/...

# Full suite
go test ./...
```
