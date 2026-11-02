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
| `hello` | `protocol_major`, `protocol_minor`, `client_version`, `profile`, `capability_sid`, `deny_read` | runtime → sidecar |
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

`profile` chooses the Windows identity ([ADR-0002](adr/0002-windows-execution-profiles.md)):
`strict` (empty means this) or `project`. It is created between `hello` and
`hello_ack`, so `level` describes what exists. `deny_read` names what `project` refuses
to read, through a deny ACE on `capability_sid`, which the client keeps. A named path
that exists and cannot be protected makes `level` `none`. A deny ACE outlives the
session on purpose; `seshat-sandboxd unprotect <capability-sid> <path>...` takes it back,
with no session needed, so it works after a crash. Under `project`, temporary files and
tool caches are redirected to `LocalLow\Seshat\sandbox`, and Git Bash runs from a private
copy kept there, because it cannot start from its install while another Git Bash runs.

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
   One variable is added, and the reason is measured rather than chosen: on
   Windows the block also carries `LOCALAPPDATA`, because `CreateProcessW`
   inside an AppContainer fails with error 203 without it — the kernel derives
   the container's per-package profile path from that variable. It is a
   directory name the identity cannot write to, never a credential, and a value
   the client sends wins over the synthesised one.
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

- **On Windows, filesystem and network confinement are enforced.** Every
  command starts with `CreateProcessW` inside an AppContainer whose package SID
  is granted the run's workdir and nothing else: a write outside it is refused
  by the system, `%TEMP%` is refused, and the identity carries no capability, so
  there is no network. `level` stays `degraded` because three limits were
  measured, not assumed: the leading directories of a workdir cannot be `stat`ed
  (`git init` fails), a toolchain installed under the user profile is not
  readable, and `%TEMP%` is not writable. A profile that cannot be created makes
  the session `none`, and the runtime refuses to execute.
- **Linux and macOS** report `degraded` with no filesystem or network
  confinement yet; macOS has no backend and will report `none` rather than
  pretend.
- **stdin is not part of the protocol.** A `RunRequest` carrying stdin is refused
  by the Go side rather than run without the input it expects.
- **macOS** has no backend; it will report `none` rather than pretend.
- **`violations` is still always empty, and that is now a measurement rather
  than a placeholder.** The refusal happens in the kernel, between the command
  and the file it asked for, so nothing in the sidecar's own process observes
  it: the command sees an error, and that error reaches the runtime as its
  stderr and exit code. What the operating system records instead is an audit
  event, and none of it is reachable without elevation on this machine —
  `auditpol /get /category:"Object Access"` fails with `A required privilege is
  not held by the client` (0x522), the Security log answers `Access is denied`,
  and the one channel that *is* readable, `AppModel-Runtime/Admin`, records
  containers being created and destroyed, not per-path refusals. Reporting a
  violation therefore needs an administrator to enable failure auditing and a
  SACL on the path, which is a machine setting a sandbox must not grant itself.
  Invariant 6 stays a promise the backends do not keep, and this document says
  so instead of guessing from stderr.
- **The Windows tree kill is a job object.** The child is created suspended,
  placed in a job whose only limit is `KILL_ON_JOB_CLOSE`, and resumed once it is
  a member, so nothing it starts can start before the assignment and nothing it
  starts can leave the job. `cancel` and a deadline call `TerminateJobObject`,
  and closing the job when a run ends ends whatever the command left running —
  the case `taskkill /T` never covered, because it only ran on a kill and always
  raced what the command was still starting. A test measures it: a detached
  grandchild proves it ran, and the file it would have written four seconds
  later never appears. `job_object` is claimed in `capabilities` for that
  reason; it means tree containment, not CPU or memory limits, which no backend
  applies yet.

## 6. Versioning

`sandboxdProtocolMajor` moves when an old peer would **misread** a frame;
`sandboxdProtocolMinor` moves when a message or field is added and an old peer
would merely reject it. A major mismatch ends the handshake with
`protocol_mismatch`. The compatibility comment lives here, in the repository —
not in a wiki that drifts.
