# ADR-0003 — A full environment for the agent, through microsandbox

- **Status:** accepted
- **Date:** 2026-11-03
- **Deciders:** maintainer
- **Supersedes:** nothing. **Amends:** [ADR-0001](0001-go-rust-boundary.md), on two points: Rust
  confinement for Linux and macOS is no longer planned in `seshat-sandboxd`, and command-policy
  evaluation stays in Go, in `internal/permissions/access`. That policy is a list of commands to
  refuse or to confirm, and confinement is what holds, so porting a shell parser would add little.

## Context

The sidecar's `project` profile ([ADR-0002](0002-windows-execution-profiles.md)) keeps the
agent's commands on the user's machine, with the user's tools. It guards against mistakes. It
leaves other files readable and the network open, and it exists only on Windows.

Some work needs something else: a whole environment the agent can change freely, where installing
a package or running code it does not trust cannot reach the rest of the machine. Docker gives
that but asks every user to install and run Docker.

[microsandbox](https://github.com/superradcompany/microsandbox) runs OCI images in microVMs on
KVM, Hypervisor.framework and the Windows Hypervisor Platform, with no daemon and no elevation
when the platform is already on. It is about 270 000 lines of Rust plus a patched Linux kernel, so
writing or forking the equivalent would be a second product.

## Measured

v0.7.13 on Windows 11 Home 26200, a React and Vite project on D:, 4 CPUs, 2 GB, image
`node:22-alpine`.

| Step | Native | VM, folder mounted | VM, `node_modules` on a volume |
|---|---|---|---|
| `npm install` | 24 s | 307 s | 38-43 s |
| `vite build` | 1.8 s | 9.7 s | 2.4-3.1 s |

- Boot of a created sandbox 1.1-2.2 s, `stop` then `start` 0.9 s, 157-314 MiB when idle.
- The host and the LAN are refused: a listener on the host received nothing. Internet is open.
- A symlink or `..` from the guest does not leave the mount. On Windows a guest symlink is a plain
  file holding the target.
- Killing the `msb exec` client ends the command in the guest. A detached job survives with no client.
- Pulling `node:22-alpine` took 101 s, `node:22-bookworm` 6 min 29 s.
- On `node:22-bookworm`, the image chosen as the default, `npm install` on a volume took 82 s and a
  smaller project 99 s, on separate runs with no npm cache. An edit from the host reached the
  server in about 4 s, against 0.24-0.47 s on `node:22-alpine`. The difference is not explained.

## Decision

**A fourth `Executor`, `EnvironmentMicroVM`, that drives the `msb` command line.** Not the Rust
crate: 868 crates would end the small, auditable sidecar. Not the Go SDK: it needs CGO, which
ADR-0001 excludes. A command line can be replaced by changing one pinned version.

The runtime takes the binary's path and does not fetch it. The embedder downloads a pinned,
checksummed version when the user turns the mode on, and `PrepareMicroVM` pulls the image then,
since a pull takes minutes and must not happen inside a command's timeout.

What the measurements force:

1. **One sandbox per project, kept between sessions**, named from the project's path. A last user
   closing stops it. A stopped sandbox keeps its disk, so the next session starts in a second.
2. **The project is mounted at `/workspace`, and what a package manager installs goes on a
   volume**: `node_modules`, `.venv`, `target`. In the mount, an install is 8 times slower than on
   a volume and leaves Linux binaries that break the same project on Windows. A volume can only be
   attached when a sandbox is created, so a marker file appearing later (`package.json`) replaces
   the sandbox. That loses what was installed outside the volumes and ends running commands.
3. **Commands get file watchers in polling mode.** Edits made on the host do not reach a watcher in
   the guest, and `write_file` and `edit_file` write on the host. With polling they arrive, see above.
4. **A private `DOCKER_CONFIG`.** With Docker Desktop's credential store on the host, pulling a
   public image failed with "Not authorized".
5. **`node:22-bookworm` by default.** It has git, python, a compiler and node, which `alpine` lacks,
   at the price of a longer first pull.
6. **A range of common dev ports is published at creation**, on `127.0.0.1`, because a command does
   not announce its port. A server must listen on `0.0.0.0`: one on the guest's `localhost`, the
   default of Vite, cannot be reached and the image has no `iptables` to redirect it. The bash tool
   tells the model.
7. **The host's environment is never passed in.** Secrets stay out unless the embedder names them.
8. **`msb exec --stream`.** Without it, nothing reaches the client until the command ends, so a dev
   server's address and errors would never reach the agent.

It fails closed, like the sidecar: an `msb` that is missing or unhealthy refuses every command.

## Consequences

**Positive**

- A real boundary on every platform with a hypervisor, including what `project` leaves open.
- The planned Linux and macOS backends in Rust are dropped. `project` stays for Windows, where
  working with the user's own tools is the point. Landlock stays too: SeshatCloud relies on it on a
  server, where a microVM would need KVM inside a container.
- The Docker backend and the `srt` backend are removed: this mode replaces the first, and `srt` failed
  on Windows. Windows 10 and Intel Macs are left with no isolated mode, and macOS with no confinement
  in direct mode.

**Negative, accepted**

- A dependency in beta, with Windows in preview. `msb` does not start on Windows 10 since 0.7.0
  (issues #1886 and #1996), and Intel Macs are not supported. The embedder hides the mode where
  `PrepareMicroVM` fails, and a new version is adopted only after these measurements are redone.
- Commands run in Linux, so Windows paths and the user's installed tools are not there. The bash
  tool says so.
- `write_file` and `edit_file` still write on the host, under the permission mode.
- Two projects running at once want the same host ports. Not measured, and not handled here.
- `libkrunfw` bundles a GPL-2.0 kernel and LGPL-2.1 code. Downloading it from the upstream release
  rather than shipping it is one reason for the download. To be reviewed before a release.

## Not measured

A real agent turn, long uptime, a reboot, another machine, projects other than Node.
