# Spike — can Windows refuse a write by path without elevation?

Phase 1, task 1.5 of [`plan-rewrite-go-rust.md`](plan-rewrite-go-rust.md) asks
for a Windows backend "Job Object + restricted token + deny-write ACLs, **sans
élévation**". That last clause is the one that decides the product: if it holds,
Windows can claim `full` with no installer prompt; if it does not, every Windows
user meets a UAC dialog before the sandbox exists.

This note records the measurement, not a plan.

## Why the question is not trivial

A deny ACE has to name an **identity**. "Deny writes under `C:\` except the
workdir" cannot be written against the user account, because the agent runs as
that same account: the ACE would forbid the agent its own writes. So the sandbox
needs a *second* identity — and the question becomes whether Windows will give a
normal user one.

## What codex does (read from `helps/codex`, 2026-10-28)

`codex-rs/windows-sandbox-rs` is 30 369 lines. It uses **no AppContainer**: the
crate mentions neither `AppContainer` nor `CreateAppContainerProfile` nor
integrity levels. It uses:

| mechanism | where |
|---|---|
| disposable **local user accounts** | `setup_provisioning/sandbox_users.rs`, `ensure_local_user` |
| deny/allow ACLs per path | `acl.rs` (1 162 lines), `deny_read_paths` / `deny_write_paths` |
| WFP firewall filters | `setup_provisioning/firewall.rs` (643 lines) |
| an **elevated helper** to provision all of it | `setup.rs` (2 757 lines) via `ShellExecuteExW`, plus `windows-sandbox-service` |

Its own test says it plainly: *"creating its disposable user requires
elevation"*. So the codex answer to the phase-1 question is **no** — with that
design, Windows sandboxing needs administrator rights.

## What this machine says

Windows, normal user (`elevated = False`, `DESKTOP-31OAHD7\Amiche`), no
administrator rights. Harness: PowerShell + P/Invoke, nothing installed.

| step | result |
|---|---|
| `CreateAppContainerProfile("seshat.spike.*")` | **OK**, SID `S-1-15-2-2862562986-…` |
| `DeleteAppContainerProfile` | OK |
| launch `cmd.exe` with `PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES` | OK, exit 0 |
| write **inside** the workdir, after `icacls /grant *<package SID>` | **succeeds** |
| write **outside** the workdir, no grant | **Access is denied**, no file created |
| read a file outside the workdir | **Access is denied** |
| `whoami /groups` inside the container | `Mandatory Label\Low Mandatory Level`, package SID present |

The read case matters as much as the write case: the plan's acceptance criteria
ask for `~/.ssh` to be unreadable, and in an AppContainer the user profile is not
granted to the package SID at all.

**Answer: yes.** Without elevation, Windows can hand out an identity that is
granted nothing, and a path rule becomes an ACE for that identity.

## What this buys, and what it costs

Bought:

- **Write and read confinement by path** with no administrator and no second
  account.
- **Network confinement for free**: an AppContainer has no `internetClient`
  capability, so outbound connections fail, which is exactly
  `allow_network = false`. Turning the network on becomes granting one
  capability for that run.
- **Low integrity** comes with it, so even objects that grant the package SID
  cannot be written by a medium-integrity leak path.
- **No new account**: nothing to create, nothing to clean up in the user list.

Cost, all of it to be measured against real workloads rather than assumed:

- **The toolchain must be readable.** `cmd.exe` runs because system paths grant
  `ALL APPLICATION PACKAGES`; a git, node, python or go install under
  `C:\Program Files` may not. Reading it means an ACE we cannot write without
  administrator rights, so a toolchain installed machine-wide is likely a
  `degraded` reason while a user-owned one is fine.
- **The workdir grant needs ownership.** `icacls /grant` on a directory we own
  works; on a protected location it does not.
- **`%TEMP%` is not writable** inside the container. A run's temporary files
  have to land in the workdir, or `TEMP`/`TMP` must be granted per run. Tools
  that assume `%TEMP%` exist will discover otherwise.
- **Profile lifecycle is ours**: create before the run, delete after it. A
  crashed sidecar leaves a profile behind — inert, but it must be cleaned, and
  the name has to be unique per run for that to be safe.
- **AppContainer starts at Windows 8 / Server 2012.** Older hosts report
  `none`, not a fallback.

## What this spike does **not** prove

- That the sidecar's own spawn path runs inside the container. Only the
  PowerShell harness did; the Rust identity plumbing exists
  ([`rust/sandboxd/src/appcontainer.rs`](../rust/sandboxd/src/appcontainer.rs))
  but the launch is still the next step. Until then the handshake keeps saying
  `degraded` — a capability that can be created is not one that is applied.
- That resource limits and the process tree are covered: those stay a Job
  Object, orthogonal to the identity.
- That the acceptance criteria of phase 1 pass: they need the real commands
  (`git`, `go build`, a Python tool) run inside. That is
  [`spike-windows-toolchain.md`](spike-windows-toolchain.md), the second note in
  this series — it finds `go build` working and `git init` failing, and says why.

## Consequence for the plan

Windows can aim for `full` without elevation, so the backend is worth building
rather than documenting as `degraded`. The order stays: launch inside the
identity, grant the workdir, then measure what a real toolchain needs. The
moment a common tool requires an ACE we cannot write, that case becomes a named
`degraded_reason` instead of a silent hole — which is the same rule the sidecar
already follows everywhere else.
