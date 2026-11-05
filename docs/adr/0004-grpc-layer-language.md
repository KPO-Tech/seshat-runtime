# ADR-0004 — The gRPC layer stays in Go, not Python

- **Status:** accepted
- **Date:** 2026-10-28
- **Deciders:** maintainer
- **Supersedes:** nothing. **Related:** [ADR-0001](0001-go-rust-boundary.md)

## Context

A handoff question: the gRPC server in `cmd/grpc` is written in Go, and Python
might be a better home for it. The premise is that maintaining it in Go costs
more than it is worth. That premise deserves numbers rather than an opinion, so
here is what the surface actually is.

**What `cmd/grpc` contains.** 1 848 lines in total: 904 of server code, 108 of
transport security (bind address, bearer token, TLS, stdio-MCP refusal), and 836
of tests. One service, `seshat.SeshatService`, and nine RPCs: `Query`,
`QueryStream`, `ListSkills`, `GetSkillDetails`, `ListMCP`, `ConnectMCP`,
`DisconnectMCP`, `GetModels`, `HealthCheck`.

**What it is.** An adapter. It imports `pkg/sdk`, `pkg/config`, `pkg/mcp`,
`pkg/skills`, `internal/providers` and `internal/storage`, and its whole body
translates protobuf messages to SDK calls and back. It holds no session, no
provider, no tool loop and no prompt: it borrows all of them. The `.proto` says
the same thing from the other side — permission requests, questions to the user,
plans and sessions do not cross this contract.

**Who calls it.** Nothing in the product family. `SeshatOS` and `SeshatCloud`
carry `google.golang.org/grpc` in `go.mod`/`go.sum` only as a transitive
dependency of the engine: no source file in either repository imports it, and
neither has a `.proto`. `seshat-ai` has no Python gRPC at all — no `import grpc`,
no `grpc.server`, no proto — while it does publish a documentation page that
promises the server, with its own warning that the surface is experimental and
partial.

That last combination is the important one, and it is not a language problem.
The surface is documented and unused, and its contract excludes the things a
real client would need first (approvals, questions, sessions).

## Decision

**The gRPC layer stays in Go.**

### Why not Python

1. **A Python server would still need this engine.** The adapter without the
   engine is nine empty handlers. To serve the same behaviour, Python either
   reimplements the engine — a separate decision, refused in ADR-0001 for
   reasons that do not change with the target language — or proxies to the Go
   one, which means a Python gRPC server in front of a Go gRPC server: two hops,
   two schemas, and one more failure mode, to save 900 lines.
2. **The SDK is Go.** `pkg/sdk` is the supported way to embed the runtime; the
   gRPC server is the network face of that same object. A Python face would
   speak to an SDK it cannot call.
3. **It would be a third language and a second codegen pipeline.** ADR-0001
   added Rust for a specific reason: confinement needs primitives Go cannot
   express safely. Python would bring `grpcio` and a protobuf toolchain to a
   repository whose stated property is that `go build` works everywhere with no
   extra toolchain. No confinement, no performance and no safety argument is
   available here.
4. **The cost was never the language.** 904 lines of adapter are not where the
   maintenance effort goes; the surface's own history shows it — the last
   functional change is 2026-10-06. Porting it would move a small, stable file to
   a new stack and leave the actual problem, that nobody calls it, untouched.

## Consequences

**Positive**

- One language for the engine and its transports: a change to the SDK and the
  handler that exposes it land in the same commit, checked by the same `Gate`.
- No new toolchain for contributors or CI.
- The only Python in the workspace stays where it already is, in `seshat-ai`,
  which is a different product with its own deploy.

**Negative, accepted**

- The "call it from any language" promise stays backed by Go code, so a
  contributor who wants to change the API needs to read Go.

## What this ADR does not settle

The language is decided; the surface is not. Two questions stay open, and they
are the ones worth arguing about later:

- **Is `cmd/grpc` kept at all?** It has no consumer and a partial contract.
  Either it is completed (approvals, questions, plans, sessions — the four
  things its own doc lists as missing) or it is deleted and the promise in
  `seshat-ai`'s documentation is withdrawn. "Keep it as it is" is the answer that
  costs the most for the least.
- **Where does per-user auth live?** The server authenticates with one shared
  token, deliberately. Anything with users, tenancy or quotas belongs to
  `SeshatCloud` (Go), in front of this server, not in a different language
  inside it.

A decision on either should be an ADR of its own; this one only records that
Python is not the answer to either.

## When to revisit

- If a Python-native component (RAG, document pipelines, OCR) needs to expose
  the runtime, it gets its own service in `seshat-ai` and calls this server as a
  client. That is a new service, not a port of this one.
- If `cmd/grpc` is still without a consumer at the phase-1 gate of the Rust
  rewrite, the question to ask is deletion, not translation.
