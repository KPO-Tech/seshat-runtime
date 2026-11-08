# Seshat, from Python

A Python client for the [Seshat](../README.md) agent runtime, over gRPC.

It is a client, not a port. The engine — providers, tools, permissions, skills,
MCP servers, memory, sessions, workflows — stays in Go, where it is the same code
the terminal interface runs. This package sends its calls over the wire, so a
Python program gets exactly what the runtime offers, at the speed of the Go
process: `grpcio` and compiled protobuf do the serialising, and nothing is
reimplemented or run as a subprocess.

```bash
pip install ./python          # from a checkout of the runtime
```

Requires Python 3.9+ and a running server (`go run ./cmd/grpc`, or `make
build-grpc && ./bin/seshat-grpc`, which listens on `127.0.0.1:50051`).

## First call

```python
import seshat

with seshat.connect() as client:            # SESHAT_GRPC_TARGET, or 127.0.0.1:50051
    print(client.info().model)              # anthropic:claude-sonnet-5-5
    print([model.id for model in client.models()])
    print([skill.name for skill in client.skills()])
```

## A conversation

```python
with client.create_session() as session:
    print(session.id)                       # s1
    result = session.ask("summarise this repository")
    print(result.content)
    print(result.usage.total_tokens)
```

The conversation lives on the server: closing the handle keeps it, and
`client.session("s1")` or `client.get_session("s1")` picks it up again — from the
stored transcript if the server restarted.

`ask()` runs a whole turn and returns a `TurnResult` (`content`, `messages`,
`usage`, `stop_reason`, `turn_number`…). `submit()` is the same turn as one
unary call, when you do not need the events.

### Configuring the agent

A session is described completely at creation — the prompt, the tool set, the
environment, the permission rule and the limits — so a worker can build the agent
it needs without touching the server's configuration:

| Argument | Effect |
|---|---|
| `model` | `"provider:model"`; empty uses the server's default |
| `api_key` | a credential for this session only |
| `system_prompt_template` | replaces the default system prompt |
| `append_system_prompt` | adds to the system prompt instead |
| `tools` | restricts the agent to these tool names (`["read_file", "bash"]`) |
| `working_dir` | the folder its file and shell tools work in |
| `permission_mode` | `onRequest` (default here), `bypass`, `auto`, `acceptEdits`, `never`, `granular` |
| `max_tokens` | maximum output tokens of a turn |
| `max_iterations`, `max_turns` | how hard and how long the agent may work |
| `turn_token_budget` | tokens one turn may spend |
| `max_consecutive_denials` | refusals after which it stops asking and ends |
| `auto_compact`, `enable_tool_search` | `True`/`False` to decide, `None` (default) to keep the server's answer |

```python
with client.create_session(
    system_prompt_template="You review Go pull requests. Read the diff, then answer.",
    tools=["read_file", "grep", "bash"],
    working_dir="/srv/repo",
    permission_mode="onRequest",
    max_turns=12,
    turn_token_budget=200_000,
    auto_compact=True,
) as session:
    ...
```

Everything after that lives on the session: `set_permission_mode`,
`set_working_directory`, `set_system_prompt`, `set_plan_mode`, `set_tools`.

## Streaming, and approvals

A tool the model wants to run is approved by *you*, while the turn runs. The
server pauses the tool, sends a `permission_request`, and waits for this client's
answer on the same stream:

```python
def approve(request):
    print(request.tool_name, request.tool_input)   # delete_branch {'branch': 'stale'}
    return "allow"          # or "allow_session", or "deny"

with client.create_session(permission_mode="onRequest") as session:
    for event in session.stream("delete the stale branches", on_permission=approve):
        if event.kind == "chunk":
            print(event.text, end="")
        elif event.kind == "tool_progress":
            progress = event.tool_progress
            print(f"\n[{progress.tool_name}] {progress.stage}: {progress.message}")
        elif event.kind == "turn_completed":
            print(f"\n\n{event.result.usage.output_tokens} output tokens")
```

Decisions: `allow`, `allow_session` (for the rest of the session), `deny`. With
no handler every request is denied and the reason says so — a client never
silently allows a tool.

A tool may also ask a question (`prompt_request`); `on_prompt` returns the
answer, `None` dismisses the question:

```python
for event in session.stream("merge the branch", on_prompt=lambda request: "main"):
    ...
```

`ask()` and `submit()` take the same `on_permission` / `on_prompt` arguments.

Every event is a `SessionEvent` with a `kind` (`chunk`, `tool_progress`,
`permission_request`, `prompt_request`, `turn_completed`, `turn_failed`, …), and
the matching field: `event.chunk.text`, `event.tool_progress.tool_name`,
`event.turn_completed`, `event.error`. The runtime's own vocabulary is passed
through as `event.type` (`tool.started`, `message.assistant`, `plan.ready`…).

## Sessions, memory, tools

```python
for info in client.sessions(limit=20):
    print(info.id, info.title, info.total_tokens)

found = client.search_sessions("migration")   # infos.ids, .sessions
client.delete_session("s1")

memory = client.memory()                     # or client.memory(user_id="alice")
memory.upsert_entities([{"name": "Seshat", "entity_type": "project",
                         "observations": ["an agent runtime"]}])
print(memory.context("what is Seshat?"))     # the Markdown the runtime injects

session.set_permission_mode("bypass")        # or auto, onRequest, never
session.set_working_directory("/srv/repo")
session.interrupt()
session.compact()
print(session.tools())
```

MCP servers are managed the same way: `client.mcp_servers()`,
`client.connect_mcp("files", type="http", url=...)`, `client.reconnect_mcp(name)`.
A `stdio` server is a command line on the server's host and is refused unless the
server runs with `SESHAT_GRPC_ALLOW_STDIO_MCP=true`; `http`, `sse` and `ws` are not.

Workflows: `client.validate_workflow(yaml_text)`, `client.draft_workflow(...)`,
`client.run_workflow(yaml_text)`.

## asyncio

The same contract on `grpc.aio`, for a program that already has an event loop:

```python
import asyncio
import seshat.aio

async def main():
    async with seshat.aio.connect() as client:
        session = await client.create_session()
        result = await session.ask("hello")
        print(result.content)

        async for event in session.stream("again", on_permission=lambda request: "allow"):
            print(event.kind)

asyncio.run(main())
```

Handlers are called in the loop, so they may be `async def`-free coroutine
functions only if you await them yourself — keep them plain functions that
return the decision.

## Errors

Every failure is one of `seshat.errors.SeshatError` and its subclasses, mapped
from the gRPC status: `InvalidArgument`, `NotFound`, `PermissionDenied`,
`Unauthenticated`, `FailedPrecondition`, `AlreadyExists`, `Aborted`,
`ResourceExhausted`, `Unavailable`, `DeadlineExceeded`, `Cancelled`, `Internal`.
Each carries `.code` and the server's `.details`:

```python
from seshat import errors

try:
    client.get_session("nope")
except errors.NotFound as failure:
    print(failure.code, failure.details)
```

A failed turn arrives as a `turn_failed` event, and `ask()` raises it.

## Connecting elsewhere

```python
seshat.connect("build-box:50051", token="...")        # SESHAT_GRPC_AUTH_TOKEN
seshat.connect("build-box:50051", tls="/etc/ssl/ca.pem",
               private_key="client.key", certificate_chain="client.pem")
seshat.connect("build-box:50051", timeout=60)         # default unary timeout
```

| Variable | Used for |
|---|---|
| `SESHAT_GRPC_TARGET` | `host:port` to connect to |
| `SESHAT_GRPC_HOST`, `SESHAT_GRPC_PORT` | the same, when the target is not set |
| `SESHAT_GRPC_AUTH_TOKEN` | the bearer token of the server |
| `SESHAT_GRPC_TIMEOUT` | the default timeout of a unary call, in seconds |

Anything the server itself needs — provider keys, `SESHAT_MODEL`, the database —
is its environment, on the machine that runs it, never this process. See
[`docs/transports.md`](../docs/transports.md).

## What is here

| Module | Contents |
|---|---|
| `seshat` | `connect`, `Client`, `Session`, `Memory`, the value types, `errors` |
| `seshat.events` | `SessionEvent` and its payloads |
| `seshat.types` | `TurnResult`, `SessionInfo`, `ToolInfo`, `Skill`, `MemoryGraph`… |
| `seshat.errors` | the exception hierarchy |
| `seshat.aio` | `AsyncClient`, `AsyncSession` |

The gRPC contract itself is `pkg/grpc/proto/seshat.proto`; the stubs under
`seshat/_proto` are generated from it with `make proto`.

## What cannot cross the wire

The engine is in Go, so a Python program gets its capabilities but not the ability
to inject code into it. In particular:

- **Tools are the runtime's tools.** You choose them, restrict them, watch them and
  approve them; you cannot hand the server a Python function to run as a tool. For
  your own capability, write an MCP server (any language) and connect it with
  `client.connect_mcp(...)`, or call it yourself between turns.
- **Go callbacks are not transferable**: hooks, custom document converters, custom
  title generators, credential resolvers. Equivalent behaviour comes from the
  server's own configuration, or through MCP.
- **The environment is the server's**: a process's environment variables, provider
  keys, sandbox backend and storage are set where the server runs. A session
  chooses its working directory, its MCP servers (with their own `env`), its model
  and its credential — not the environment of the host process.
- **Sub-agent definitions and scheduled jobs** are the host's configuration; a
  client opens sessions and runs them, it does not add definitions to the runtime.

## Tests

```bash
cd python
python -m pytest -q
```

They run against a scripted in-process server (`tests/fake_server.py`) that
speaks the same contract, so no provider key, engine or network is needed — and
the permission and prompt round trips are exercised for real.
