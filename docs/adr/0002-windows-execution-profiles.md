# ADR-0002 — Two execution profiles on Windows, named for what they protect

- **Status:** accepted
- **Date:** 2026-11-02
- **Deciders:** maintainer
- **Supersedes:** nothing. **Amends:** nothing; it applies [ADR-0001](0001-go-rust-boundary.md).

## Context

The sidecar's Windows backend runs every command in an AppContainer. It is the
stronger boundary: reads and writes outside the workdir are refused by the system.
But [`spike-windows-project-profile.md`](../spike-windows-project-profile.md) measured
that it cannot run a project: `git init` fails, `npm install` fails, and `node` cannot
run a script file, because these tools walk the leading directories of a path and
those reads are denied. Fixing that needs elevation.

The product needs the opposite: an agent working in a real project, and a dev server
the app can show. The same note measured an identity that does it without elevation,
at the price of a weaker boundary: writes are confined, reads and the network are not.

There is no single identity that gives both on a non-elevated install.

## Decision

**The sidecar offers two profiles, and the runtime chooses per session.**

| | `project` | `strict` |
|---|---|---|
| Identity | restricted token + low integrity + job object | AppContainer + job object |
| Writes | the workdir only | the workdir only |
| Reads | everything, except what is denied by name | the workdir only |
| Network | open | closed |
| Runs `git init`, `npm`, a dev server | yes | no |
| For | working in a project | running what needs no toolchain |

Rules that come with it:

1. **`level` stays `degraded` for both, with its reasons.** `project` names what it
   leaves open: `reads are not confined`, `network is open`, `user secrets are
   reachable through DPAPI`. A profile never claims more than was measured.
2. **The product says what it protects, in those words.** Not "sandboxed". For
   `project`: writes limited to the project, reads and network open.
3. **Protected folders are a deny ACE on a capability SID.** The SID is random, made
   once per install, and kept by the runtime. The ACE denies `ReadData` only, so `stat`
   keeps working and a workspace below a protected folder still runs its tools.
4. **Changing the ACL of a user's folder is the user's decision.** Nothing is written
   until the sandbox is turned on. The list is visible, each entry can be removed, and
   turning the sandbox off takes every ACE back. The default list:
   - `.ssh`, `.gnupg`, `.aws`, `.azure`, `.kube`, `.docker`, `.config/gh`
   - `.git-credentials`, `.npmrc`, and the `.env` files of Seshat's own configuration
   - the profiles of Chrome, Edge and Firefox
   - Seshat's configuration folder, whose `workspaces` keep working (the spike's
     nested case)
5. **Fails closed, as ADR-0001 requires.** When the sandbox is on and a profile cannot
   be applied, the command is refused. It never runs on the host instead.
6. **Off by default at first.** It becomes the default only after real use, in a
   decision of its own.

The boundary of ADR-0001 holds: Go chooses the profile and the list, and Rust applies
them. Neither the profile nor the list comes from the model: a command cannot widen
its own sandbox.

## Consequences

**Positive**

- A real project works confined on a stock Windows, with no elevation and no Docker:
  measured from `npm create` to a page served to the host.
- A destructive command can no longer touch anything outside the project.
- The AppContainer work is kept, for what it is good at.

**Negative, accepted**

- `project` does not stop exfiltration. A command that was steered by a web page can
  read what is not listed, ask DPAPI, and use the network. Stopping that takes Docker,
  or a future mode with no network. The product must not suggest otherwise.
- The sidecar writes to the ACLs of real user folders. It is reversible and has no
  effect on the user, but a crash can leave an ACE behind, so the runtime must be able
  to remove every ACE of its SID without having a record of where they are, or keep
  that record outside the protected folders.
- Workdirs carry a low-integrity label while in use, which other low-integrity programs
  on the machine could write through. It is taken back when the session ends.
- Tools that use a temporary folder or a home directory must be pointed into the workdir,
  as the spike did. Some caches are rebuilt for each workdir, which costs time.

## Not decided here

- macOS: no backend yet. It reports `none`, as ADR-0001 requires.
- Linux: Landlock stays where it is. Moving it into the sidecar is a separate question.
- A `project` mode without network, and allow-listing hosts: closing the network per
  process needs the firewall, so elevation.
- How a dev server's port is found and opened in the app.
