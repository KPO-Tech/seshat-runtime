# Security Policy

## Supported versions

| Version | Supported |
|---|---|
| `main` (latest) | ✅ Active |
| Older tags | ❌ No backports |

We only apply security fixes to the latest code on `main`. If you are using an older release, upgrade first.

---

## Reporting a vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Use GitHub's private vulnerability reporting:

1. Go to **Security → Advisories** on the repository page.
2. Click **Report a vulnerability**.
3. Fill in the affected component, a description, and steps to reproduce.
4. Submit — the report is private and only visible to maintainers.

If you cannot use GitHub, write to **seshatsupport@seshat-ai.com** instead.

We will acknowledge receipt within **72 hours** and aim to provide a patch or mitigation within **14 days** for critical issues.

---

## Scope

Security issues we want to know about:

- **Sandbox escapes** — ways to execute code or read files outside the agent's working directory when Landlock sandboxing is active.
- **Permission bypass** — ways to invoke destructive tools in `onRequest` or `never` mode without triggering the permission check.
- **Credential leakage** — API keys or OAuth tokens exposed in logs, error messages, or persisted sessions in plaintext.
- **MCP prompt injection** — malicious tool results that cause the agent to exfiltrate data or execute unintended actions.
- **gRPC / HTTP exposure** — unauthenticated endpoints that allow arbitrary agent execution on a deployed instance.
- **Session isolation** — ways to read or write another user's session data when seshat is deployed as a shared service.

Out of scope for this repository:

- Vulnerabilities in LLM providers (report directly to Anthropic, OpenAI, etc.).
- Social engineering or phishing.
- Issues requiring physical access to the host.
- Denial-of-service attacks on local single-user deployments.

---

## Security model

seshat is a **local-first** runtime. By default:

- It runs as the invoking user — no privilege escalation.
- Bash commands are sandboxed on Linux via **Landlock** (kernel-level filesystem isolation scoped to the working directory).
- On macOS and Windows commands are confined by [srt](https://github.com/anthropic-experimental/sandbox-runtime) when it is installed and working (Windows support is **alpha** and is not set up automatically); otherwise they run with your full rights, as they do on any host without a sandbox. Docker is the isolation that works everywhere. `sdk.SandboxBackend()` reports `landlock`, `srt` or `none`.
- No telemetry is sent anywhere.
- Credentials are read from environment variables or the local `~/.seshat/auth.json` store — never sent to external services except the configured LLM provider.
- A command the model runs gets the environment of the process **without its secrets** (provider keys, database and cloud credentials): see [`docs/tools.md`](./docs/tools.md).
- In the terminal UI, the parts of a project's configuration that start programs or choose who receives your API key (MCP servers, hooks, language servers, providers, allowed tools) are ignored until you run `seshat trust` in that project: see [`docs/project-trust.md`](./docs/project-trust.md). A `.env` file in the working directory cannot run a command or change where Seshat looks for its configuration.
- The gRPC server (`cmd/grpc`) listens on the loopback (`127.0.0.1`) by default. On another address it refuses to start unless a shared token (`SESHAT_GRPC_AUTH_TOKEN`) is set or the operator accepts the risk with `SESHAT_GRPC_ALLOW_INSECURE_REMOTE`; TLS is available, and `ConnectMCP` does not start a stdio MCP server unless `SESHAT_GRPC_ALLOW_STDIO_MCP` is set. The token is shared, not per user: put the server behind your own gateway if callers must be told apart. See [`docs/transports.md`](./docs/transports.md).

The model behind these defaults (permissions, sandbox, trust) is explained in [Security and trust](https://seshat-ai.com/en/docs/concepts/security-and-trust) on seshat-ai.com.

When deploying as a shared service (for example via SeshatCloud), additional security controls (user auth, session isolation, encrypted credential storage) are the responsibility of the product layer.

---

## Responsible disclosure

We follow a 90-day responsible disclosure policy. After a fix is shipped, we will publicly disclose the vulnerability in a GitHub Security Advisory with credit to the reporter (unless anonymity is requested).
