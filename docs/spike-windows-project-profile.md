# Spike — a Windows identity that can run a real project

[`spike-windows-toolchain.md`](spike-windows-toolchain.md) measured what a real
toolchain does inside an AppContainer and left a product question open: `level`
stays `degraded`, and `git init` fails. This note asks the question the product
actually needs answered: **can a confined command create a React project, install
it, build it and serve it to the app, without elevation?**

## Method

Throwaway PowerShell harnesses with P/Invoke, nothing installed, no elevation:
`CreateRestrictedToken`, `SetTokenInformation(TokenIntegrityLevel)`,
`CreateProcessAsUserW`, `CreateAppContainerProfile`. The environment block is built
by the harness, with `HOME`, `TEMP` and the npm cache inside the workdir. The
workdir is on `D:`, like a real project, unless stated otherwise. Everything created
was removed afterwards. Measured 2026-11-02.

Machine: Windows 11 Home `10.0.26200`, non-elevated user, same as the two earlier
notes. `node 24.16.0` in `C:\Program Files\nodejs`, `git 2.54.0` in `C:\Program
Files\Git`, Python 3.12 under the user profile.

## Three identities, side by side

| Inside the identity | AppContainer + network capabilities | low integrity | low integrity + restricted token |
|---|---|---|---|
| write in the workdir | ok | ok | ok |
| write in the user profile | **refused** | **refused** | **refused** |
| write in another folder of the same disk | **refused** | **refused** | not run |
| read a file of the user profile | **refused** | allowed | allowed |
| read a folder carrying a deny ACE | not run | not run | **refused** |
| `git init` + commit | fails | ok | ok |
| Git Bash: pipe, `mktemp`, git | not run | ok | ok |
| Python under the user profile | refused | ok | ok |
| outbound HTTPS | ok | ok | ok |
| `npm install` | fails, `EPERM lstat D:\` | ok | ok |
| run a script file with `node` | fails, same walk | ok | ok |
| dev server reached from the host | no, it never starts | ok | ok |

The restricted token is `DISABLE_MAX_PRIVILEGE | LUA_TOKEN`, with these restricting
SIDs: a capability SID made for the sandbox (random, `S-1-5-21-…`), the logon SID,
Everyone, the user, Users, Authenticated Users and Interactive. The low integrity
level is set on that token. The workdir gets `(OI)(CI)` low.

The real case, under the last column:

| step | result |
|---|---|
| `npm create vite@latest app -- --template react` | ok |
| `npm install` | ok, 24 packages, 427 files, 42 s |
| `npm run build` | ok |
| `npm run dev`, reached from the host | first answer after **1.3 s** |
| `GET /src/main.jsx` from the host | HTTP 200, compiled |

## Findings

### 1. The AppContainer cannot run a project, and `Program Files` is not a rule

Earlier notes showed `git init` failing on the leading-directory walk. Node does the
same when it resolves a script's real path: `lstat D:\` is denied, so a script file
does not run at all, only `node -e`. That rules out every JavaScript project.

The earlier note also read `C:\Program Files` as readable by package identities.
That is an inherited ACE, and it is not general: `C:\Program Files\nodejs` has a
protected ACL with no `ALL APPLICATION PACKAGES` entry, so Node's installer leaves it
unreadable. Measuring needed a copy of Node inside the workdir.

### 2. No-Read-Up does nothing for files

`icacls <folder> /setintegritylevel (OI)(CI)M:NR` succeeds, with and without `/T`.
A low-integrity process then reads the files, nested ones and new ones, and lists
the folder. The label is accepted and not enforced on files.

### 3. A deny ACE on the capability SID does what No-Read-Up does not

A normal user can add the ACE to a folder they own, through the ACL API. `icacls`
refuses, because no account maps to the SID. The confined command gets `Access is
denied`; the user is unaffected, because their own token does not carry the SID.

Denying only `ReadData` keeps `stat` working on the folder, so the leading-directory
walk of finding 1 does not come back. A workspace below such a folder, which is how
`~/.config/seshat/workspaces` is laid out, runs `git init`, `node` and `npm install`,
while the credentials file next to it stays refused and its folder cannot be listed.

### 4. Labelling the root is enough, and cheap

On a tree of 5 000 existing files created at medium, labelling only the root took
302 ms. The confined command could then edit existing files and create new ones at
every depth, so the label reached what was already there. The ACE on the root took
605 ms. On a larger tree, the `(OI)(CI)` grant took 17.5 s for 50 000 files.

### 5. A job object holds under low integrity

A process created suspended, assigned to a job with `KILL_ON_JOB_CLOSE` and resumed:
a detached grandchild ran, and stopped when the job closed. This was measured on the
low-integrity token without the restriction; the sidecar's tests must repeat it on
the final token.

### 6. The write-restricted flags do not start here

`WRITE_RESTRICTED` with a capability SID, the logon SID and Everyone, at low
integrity, fails every launch with `0xC0000142`. Codex uses that flag; this exact
combination was not investigated further, since the fully restricted token works.

## What stays open

These are limits of the identity, not items left for later:

- **The network is open.** Closing it per process needs the Windows firewall, and
  so elevation. `npm install` needs it anyway.
- **Unlisted files are readable.** The deny ACE protects what is named, nothing else.
- **DPAPI decrypts** under the final token: a blob protected for the user comes back
  in clear, because the identity is still that user.
- **The Credential Manager is listed** (`cmdkey /list`). Reading a stored password
  was not attempted; assume it is reachable.

So a command can read what is not listed and send it somewhere. This identity stops
damage, not a determined exfiltration.

## What this does not prove

- **That it cannot get out.** Under the final token `explorer.exe` fails to start
  (`0xC0000142`), and `schtasks /create` fails. Under low integrity alone,
  `HKCU` refuses writes. These are two doors tried, not an audit of the COM brokers
  a medium-integrity process exposes.
- **GUI programs.** `explorer.exe` failing suggests desktop programs do not start.
  A headless browser for tests was not tried.
- **Undoing a label.** Restoring a tree to medium was measured with `/T` only: 33 s
  for 50 000 files. Restoring the root alone was not measured.
- **Files with a protected ACL or their own label inside a project**, which would not
  inherit from the root.
- **One machine, one Windows build.**
