# Spike — what a real toolchain can do inside a Windows AppContainer

[`spike-windows-confinement.md`](spike-windows-confinement.md) proved that Windows
hands a normal user an identity nobody is granted anything to, and that writes
outside a granted directory are refused. It left the acceptance criterion open:
that note's last section says the phase-1 criteria need the real commands —
`git`, `go build`, a Python tool — run **inside** that identity.

This note records that measurement. It is the second of the series, and it is
also the one that decides whether the Windows backend can claim `full` or has to
name a `degraded_reason`.

## Method

A throwaway PowerShell harness, nothing installed, no available privileges:
`CreateAppContainerProfile`, `DeriveAppContainerSidFromAppContainerName`, then
`CreateProcessW` with `PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES`. Output is
captured through handles the parent opens and marks inheritable, so a command's
own redirections are never involved. The workdir receives one inherited grant,
`(OI)(CI)M`, for the package SID and nothing else. The environment block is built
by the harness, not inherited.

Machine: Windows 11 Home `10.0.26200`, `DESKTOP-31OAHD7\Amiche`,
`elevated = False`. `git 2.54.0.windows.1` in `C:\Program Files\Git`;
`go 1.26.5 windows/amd64` in `C:\Program Files\Go`. Measured 2026-10-29.

The harness is not the sidecar. It performs the same launch the sidecar will,
which is the point of measuring before writing that launch.

## Results

Inside the container, `cwd` = the granted workdir:

| command | result |
|---|---|
| `echo hello` | ok |
| `cd` | the workdir path |
| `whoami /groups` | `Mandatory Label\Low Mandatory Level`; `BUILTIN\Administrators` deny-only; `BUILTIN\Users` present |
| `echo probe>inside.txt` | ok, file created |
| `mkdir testdir` then a file inside it | ok |
| `echo probe>%USERPROFILE%\out.txt` | **Access is denied** |
| `echo probe>%TEMP%\out.txt` (the real `%TEMP%`) | **Access is denied** |
| `dir %USERPROFILE% /b` | **Access is denied** |
| `dir "C:\Program Files" /b` | **Access is denied** |
| `"C:\Program Files\Git\cmd\git.exe" --version` | ok — `git version 2.54.0.windows.1` |
| `git --version` (through `PATH`) | ok |
| git bundled under `%LOCALAPPDATA%\Programs\…` | **Access is denied**, at launch |
| `git -C srcrepo log` / `status` / `config --local` | ok (`status` warns it cannot read `~/.config/git/ignore`) |
| `git init` | **fails** — `cannot lock ref 'HEAD': unable to create directory for …/.git/HEAD` |
| `git clone srcrepo` (local repository) | **fails** — `Could not read from remote repository` |
| `git clone https://github.com/…` | **fails** — `Could not resolve host: github.com` |
| `go version`, `go env` | ok |
| `go build` of a hello module, cold cache | **ok**, 8.5 s |
| run the produced `hello.exe` | **ok** — prints `built inside` |

A small Go program, compiled and run by that same `go build`, then measured what
the refusal is actually made of:

| operation inside the container | result |
|---|---|
| `mkdir` a directory and a nested one, create `HEAD` in it | ok |
| create `…\work\abs-test\HEAD` **by absolute path** | ok |
| spawn `git --version` | ok |
| `stat C:\`, `C:\Users`, `C:\Users\Amiche`, `…\AppData`, `…\AppData\Local` | **Access is denied** |
| `stat C:\ProgramData` | **Access is denied** |
| `stat C:\Program Files` | ok |

## Three findings, with their causes

### 1. The launch needs `LOCALAPPDATA` in the environment block

`CreateProcessW` inside an AppContainer returns `ERROR_ENVVAR_NOT_FOUND` (203)
when the block is the minimal `SystemRoot` + `PATH` set. Adding one variable at a
time, `LOCALAPPDATA` is the one that flips it to success; `USERPROFILE`,
`SystemDrive`, `TEMP`, `USERNAME`, `COMPUTERNAME`, `ALLUSERSPROFILE` and `OS` all
leave it failing. An inherited block always works, which is why this hides until
somebody scrubs the environment.

That collides with invariant 3 of the protocol — "no environment inheritance" —
and resolves it without weakening it: the sidecar still forwards nothing of its
own, but it must **synthesise** `LOCALAPPDATA` for Windows, because the kernel
derives the container's per-package profile from it. A missing variable that has
to be supplied by the launcher is a fact about the platform, not a leak from the
parent.

### 2. What a package identity may read is decided by inherited package ACEs

`C:\Windows` and `C:\Program Files` carry
`APPLICATION PACKAGE AUTHORITY\ALL APPLICATION PACKAGES:(I)(RX)` and
`ALL RESTRICTED APP PACKAGES:(I)(RX)`, inherited down to the files installers put
there. `C:\Users`, `C:\ProgramData`, `C:\Users\Public` and a data volume grant
`BUILTIN\Users` and `Authenticated Users` — neither of which passes the check on
an AppContainer token, because such a token is a **restricted** token: access has
to satisfy the ordinary SID list *and* the restricted one, which contains only
the package SID and the app-package SIDs.

So "the machine-wide toolchain" is readable and "the toolchain under the user's
profile" is not — the opposite of what the first note guessed, and the reason a
`go` install in `C:\Program Files` works while the application's own bundled
`git` under `%LOCALAPPDATA%\Programs` cannot even be launched. `git status` also
demonstrates the read criterion the plan asks for: `~/.config/git/ignore` is
refused to the command.

### 3. `git init` and `git clone` fail, and it is git's path walk, not the disk

Git builds the lock file path absolutely and calls
`safe_create_leading_directories`, which `stat`s every leading component. `stat
C:\Users` is denied (finding 2), so git concludes the directory is missing, tries
to create it, is refused, and reports `unable to create directory for …/HEAD`.
`git init` dies at that point, after having created `.git`, `config`, `hooks/`,
`info/` and `refs/` — everything that never needs a leading-directory walk.

That the disk is not the obstacle is measured directly: the same Go program, in
the same container, creates `…\work\abs-test\HEAD` **by absolute path**, with
`mkdir` along the way. Read-only git commands and writes into an existing
repository work; only the commands that walk the path from the drive root fail.

This is not fixable from inside the sandbox. Making `stat C:\Users` succeed means
writing an ACE on `C:\Users`, which needs the rights the whole design exists to
avoid asking for; putting the workdir under `C:\Program Files` needs the same
rights for the write side. A non-elevated per-user install cannot have both a
writable workdir and a readable ancestor chain.

## Consequence for `level`

Filesystem confinement by path and network confinement both work, without
elevation: writes land only in the granted directory, `%TEMP%` is refused, and
`git clone https://…` fails at name resolution because the container carries no
`internetClient` capability. `degraded_reason` can therefore stop saying
"filesystem confinement is not implemented".

But Windows cannot claim `full` for a default install, and should not pretend to:

| reason to report | why |
|---|---|
| `git cannot initialise: stat of the workdir's leading directories is denied` | finding 3; measured, not inferred |
| `toolchain installed under the user profile is not readable` | finding 2; the app's bundled git is the first case |
| `%TEMP% is not writable: temporary files must go to the workdir` | tools that assume `%TEMP%` will fail late |

`full` becomes reachable only with a provisioning step that puts both the workdir
and the toolchain under a package-readable root — an installer running elevated,
which is exactly the cost the first note hoped to avoid. That trade-off is a
product decision, not a measurement, and this note does not make it.

## What this does not prove

- **Python was not measured.** The pattern suggests an installation under
  `C:\Program Files` behaves like Go and one under the profile behaves like the
  bundled git, but that has not been run.
- **The sidecar still does not launch inside the identity.** This harness does;
  [`rust/sandboxd/src/appcontainer.rs`](../rust/sandboxd/src/appcontainer.rs)
  creates, reads the SID and deletes. Wiring the launch, the workdir grant and
  the `LOCALAPPDATA` synthesis into it is the next step, and until it lands the
  handshake must keep saying `degraded`.
- **Job Objects were not exercised.** CPU, memory and process-tree limits are
  orthogonal to the identity and stay unmeasured here.
- **Only one machine, one Windows build.** The ACL layout of `C:\Program Files`
  is a Windows default, but the list of package-readable roots here comes from
  reading the ACLs of the directories on this machine. It is not an enumeration
  of every volume a Windows host might have, and a host with a differently
  permissioned share could behave better than this one.
