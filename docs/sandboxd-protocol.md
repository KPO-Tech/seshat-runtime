# `sandboxd` — the Go ↔ Rust boundary

`seshat-sandboxd` is the sidecar that owns execution confinement. The Go engine
owns everything else: the agent loop, prompts, providers, tools, sessions, policy
decisions and the terminal interface. This document is the normative description
of the contract between the two. It is an internal contract — no public API, no
SDK, no MCP surface — and it changes with `sandboxdProtocolMajor`.

Implementation: [`internal/sandbox/sandboxd.go`](../internal/sandbox/sandboxd.go)
and [`rust/sandboxd/`](../rust/sandboxd/).

## 1. Why a process, not a library

Three properties, in order of importance:

- **A crash does not cross.** A fault in the sandbox kills the sandbox, not the
  session that was running. Go's own execution path stays intact.
- **No C toolchain.** A `cdylib` would make `go build` depend on a native
  toolchain on every platform — the property `modernc.org/sqlite` was chosen to
  preserve. A process boundary needs neither cgo nor bindgen.
- **An audit surface that fits in a page.** The sidecar receives a command, a
  directory, limits and an environment. It never sees a prompt, a model, a
  token, a session or a conversation. What an attacker could reach through it is
  a command line.

## 2. Transport

**stdio only.** The runtime starts the binary, speaks over stdin/stdout, and
kills the process when the session ends. No TCP port, no Unix socket, no shared
daemon — therefore no authentication to design, nothing to discover, no port to
collide with, and a lifecycle that makes an orphan sidecar visible immediately
rather than after a reboot.

**Framing:** a 4-byte big-endian length followed by exactly that many bytes of
UTF-8 JSON. Lengths outside `1..=8 MiB` are refused before allocation. Each side
rejects unknown fields: a peer that sends a field this side does not know may
also mean something different by the ones it does.

**Why JSON and not protobuf.** This boundary carries shell output — arbitrary
bytes, ANSI escapes, invalid UTF-8 — which is why payloads are base64. It has to
be readable with a pipe and a hex dump when a sandbox refuses a command at 2 a.m.
The cost is a schema written twice (Go and Rust), paid for by this document and
by the shared shapes below. Protobuf would remove the duplication and add a
`protoc` toolchain to every contributor's machine and to CI; for ~10 messages on
a private boundary, that price is not justified.

## 3. Messages

One `Frame` per message: `{"type": "...", "<payload>": {...}}`, with exactly one
payload set.

| `type` | payload | direction |
|---|---|---|
| `hello` | `protocol_major`, `protocol_minor`, `client_version` | runtime → sidecar |
| `hello_ack` | `protocol_major`, `protocol_minor`, `server_version`, `level`, `capabilities`, `degraded_reasons` | sidecar → runtime |
| `run` | `run_id`, `command`, `shell`, `work_dir`, `env`, `allow_network`, `timeout_ms`, `max_output_bytes` | runtime → sidecar |
| `chunk` | `run_id`, `stream` (`stdout`/`stderr`), `data` (base64) | sidecar → runtime |
| `exit` | `run_id`, `exit_code`, `duration_ms`, `timed_out`, `truncated`, `stdout_bytes`, `stderr_bytes`, `violations` | sidecar → runtime |
| `cancel` | `run_id` | runtime → sidecar |
| `error` | `code`, `message` | either |

`level` is what the sidecar can **actually enforce on this host**, never what the
runtime asked for: `full` (everything), `degraded` (a subset, with every missing
capability named in `degraded_reasons`), `none` (nothing — the runtime must
refuse to run).

Arrays are always encoded, never `null`: `"shell":[]`, not `"shell":null`. An
empty list and an absent one must not be two different shapes.

## 4. Invariants

1. **Fails closed.** If the binary is missing, the handshake fails, the major
   version differs, or `level` is `none`, the runtime refuses to execute. There
   is no fallback to the host path: an operator who asked for a sandbox and got
   none sees an error, not a command that quietly ran unconfined.
2. **One run at a time, one `run_id` per session.** Interleaving commands would
   make a violation report ambiguous about which command caused it.
3. **No environment inheritance.** The sidecar clears its own environment and
   builds the child's from `env` alone. A variable the runtime did not name — an
   API key, a proxy credential, `AWS_*` — cannot reach a model-issued command.
4. **Truncation is announced.** `max_output_bytes` cuts the stream but `exit`
   carries the real byte counts and `truncated: true`, so the model can be told
   it is looking at part of the output.
5. **Killing kills the tree.** A timeout or a `cancel` terminates the command and
   everything it started; the run does not end while a grandchild still holds a
   pipe.
6. **Violations are reported.** When a backend refuses a write or a connection,
   `violations` says so. A sandbox that only says "no" turns every diagnosis into
   a guess.

## 5. Known gaps (phase 0)

- Filesystem and network confinement are **not implemented yet**; the sidecar
  reports `degraded` and names both. What it enforces today is the deadline, the
  output cap, the environment scrub and the tree kill.
- **stdin is not part of the protocol.** A `RunRequest` carrying stdin is refused
  by the Go side rather than run without the input it expects.
- **macOS** has no backend; it will report `none` rather than pretend.
- The Windows tree kill uses `taskkill /T`; a Job Object backend replaces it
  without changing this protocol.

## 6. Versioning

`sandboxdProtocolMajor` moves when an old peer would **misread** a frame;
`sandboxdProtocolMinor` moves when a message or field is added and an old peer
would merely reject it. A major mismatch ends the handshake with
`protocol_mismatch`. The compatibility comment lives here, in the repository —
not in a wiki that drifts.
