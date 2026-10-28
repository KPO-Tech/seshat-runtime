# ADR-0001 — The Go/Rust boundary: a sandbox sidecar, not a second runtime

- **Status:** accepted
- **Date:** 2026-10-28
- **Deciders:** maintainer
- **Supersedes:** nothing. **Amends:** nothing.

## Context

Seshat is a Go agent runtime of roughly 314 000 lines: an agent loop, 104
registered tools, five model transports covering 15 providers, 13 SQLite tables,
a terminal interface, a gRPC server and an SDK.

Two things in it are weak for reasons the language makes structural rather than
accidental:

1. **Confinement is partial and optional.** `internal/sandbox` has Docker (needs
   a daemon) and Landlock (Linux only, and only for the `bash` tool). The default
   is `NoopExecutor`, which runs commands on the host. The bash tool logs a
   warning on platforms without a backend and runs anyway.
2. **A permission state is representable that should not exist.** The internal
   audit found that auto-mode could allow anything when no classifier was
   configured — an `if` that falls through to "yes".

Rust is a better substrate for exactly these two: an exhaustive decision enum
with no default branch makes "fell through to allow" a compile error, and
`Send`/`Sync` plus no GC suit a runtime that executes model-issued shell commands
concurrently.

It is **not** a better substrate for the rest. The agent loop is I/O-bound, the
providers are 15 sets of paid-for quirks, the tools are an ecosystem of Go
libraries, and the terminal interface is 71 000 lines that works.

A full rewrite is out of the question: measured, it is ~250 000 lines of Rust
before parity, and the parity argument is worse than the cost argument — a
partial clone is a worse product than an incomplete one with a solid core.

## Decision

**One codebase, one product, one owner for the agent loop.**

- Rust owns **execution confinement and command-policy evaluation** only.
- Go keeps everything else, including the permission *policy* — who approves,
  which mode is active, what the user is asked. Only the "is this command
  permitted, and with what access" question crosses into Rust.
- The boundary is **a process, spoken over stdio**, described in
  [`docs/sandboxd-protocol.md`](../sandboxd-protocol.md).
- Rust lives in `rust/` of this repository. It is **not** a separate repository
  and **not** a second agent.
- Go side entry point: `sandbox.EnvironmentSandboxed`, a fourth `Executor`
  backend behind the interface that already exists.

The rule this rests on, and the one to re-read in every future review:

> **One component owns the agent loop. Everything else is a servant, never a
> peer.**

If two components can both "run the agent", there are two products: two
backlogs, two behaviours for the same tool, two session formats, two permission
rules, and half a community each.

## Consequences

**Positive**

- The costly part of the problem is bought rather than written: `codex` already
  pays for OS-level sandboxing per platform, and is readable as a reference
  (Apache-2.0). Nothing is forked.
- `go build` keeps working everywhere: no cgo, no C toolchain, no bindgen.
- The confinement surface is small enough to audit in one sitting, because it
  cannot see a prompt, a model or a session.
- Enabling the backend is additive: a fourth case in `NewExecutor`. No tool, no
  contract, no provider changes.

**Negative, accepted**

- Two toolchains in CI and in a contributor's setup.
- A second binary to build, sign and ship per platform.
- A wire protocol to keep in sync by hand, with no schema compiler.
- Commitments that must be revisited if the sidecar grows: a phase gate measures
  whether it is used, whether it caught anything, and what it costs to maintain.
  If not, it is deleted. The gate is written down so it cannot be quietly
  skipped.

**Deliberately out of scope**

- Reimplementing the terminal interface, the providers, or the tools in Rust.
- Exposing the sidecar as an MCP server, or listening on a port: a sandbox the
  model can address is not a sandbox.
- A binding layer (`cgo`, napi, PyO3) to the sidecar's logic.
