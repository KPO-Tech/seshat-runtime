# Contributing to Seshat

Thank you for your interest in contributing. This guide covers everything you need to get started.

Seshat is an open-source Go agent runtime for autonomous AI systems, multi-agent orchestration, workflow automation, MCP integrations, RAG pipelines, CLI apps, and embeddable SDK use cases. Contributions are welcome across the runtime core, tool system, provider adapters, memory, permissions, gRPC surfaces, and developer documentation.

---

## Where to contribute

- **Runtime core** — session loop, prompt assembly, execution orchestration, permissions, memory lifecycle, and runtime architecture.
- **Providers and model routing** — LLM provider adapters, auth flows, model metadata, fallback behavior, and transport compatibility.
- **Tools and automation** — built-in tools, tool surfaces, shell and file automation, MCP clients, and external integrations.
- **Agent teams** — mailbox-driven coordination, team primitives, structured delegation, and future multi-agent collaboration layers.
- **Docs and developer experience** — CLI docs, Go SDK docs, gRPC/API docs, examples, tutorials, and architecture explanations.

If you want to help but are unsure where to begin, open a discussion on the project vision, the agent runtime roadmap, or a specific automation or integration use case.

---

## Branching strategy

```
main          production-ready, tagged releases only
  └── dev     stable integration branch — all feature work lands here first
        └── <type>/<slug>   your working branch, one per issue
```

- Branch off `dev`, not `main`.
- Never commit or push directly to `main`.
- Never commit or push directly to `dev`.
- All work must flow through PRs: `<type>/*` → `dev`, then `dev` → `main`.
- Name your branch `<type>/<short-slug>` where `<type>` is one of `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `perf`, or `ci`.
- Open PRs **targeting `dev`**. The only allowed PR into `main` is `dev` → `main`, opened by maintainers for final validation at milestone boundaries.
- Direct PRs from topic branches into `main` will be closed without merge.
- The **Gate CI check** must be green before any PR can be merged (Build + Test + Lint all pass).

If someone makes an exceptional direct push to `main`, recreate or realign
`dev` from `main` before continuing normal feature work. Do not stack new
feature branches on top of a diverged `dev`.

### Stacked pull requests

A change that is too large to review in one diff, or that has to land in a fixed
order, is split into layers: each PR targets the branch below it, and only the
bottom layer targets `dev`. A PR whose base is another topic branch is a normal,
allowed PR here.

CI is not filtered by base branch, so **every layer gets its own full run** at
its own head SHA. That is the point of the rule: a stacked PR merged on the
strength of its parent is a PR nobody checked. The price is one suite per open
PR per push, so keep the number of layers to what the review needs — when the
split is cosmetic, one branch is cheaper.

Two consequences to keep in mind:

- Changing a PR's base (retargeting the top of a stack onto `dev` once the
  layers under it have merged) does not re-run CI: the check never reports and
  GitHub blocks the merge until it does. Push a commit, or run
  `gh workflow run ci.yml --ref <branch>`, then retarget. Merge bottom-up.
- A push to a lower layer does not re-run the PRs stacked on it, because their
  head has not moved. Each PR keeps the result of its own head SHA, so re-read
  the layers above before asking for a merge.

---

## Before you open a PR

- **Bug fix** — open an issue first to confirm the bug and discuss the fix approach.
- **New feature** — open an issue or discussion before writing code. Features need to align with the [project vision](./docs/vision/idea.md) and the [current roadmap](./docs/vision/roadmap.md).
- **Documentation** — PRs are welcome without prior discussion.
- **Refactor** — discuss first. We are conservative about structural changes that could introduce regressions.

---

## Development setup

This section is for building seshat from source to change it. To use it, follow the [installation guide](https://seshat-ai.com/en/docs/getting-started/installation) on seshat-ai.com instead.

### Requirements

- Go 1.25+
- `golangci-lint` for linting
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` if modifying the proto file (see [`docs/transports.md`](./docs/transports.md))

### Clone and build

```bash
git clone https://github.com/KPO-Tech/seshat-runtime
cd seshat-runtime

# Install git pre-commit hooks (run once)
make hooks

# Build
make build

# Test
make test
make test-race
```

### Environment

Copy `.env.example` (if present) or set provider credentials directly:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
export OPENAI_API_KEY=sk-...
```

---

## Code conventions

### Go style

- Format with `gofmt` — enforced by the pre-commit hook and CI.
- Follow `go vet` — enforced by CI.
- Follow `golangci-lint` rules — see [`.golangci.yml`](./.golangci.yml) for the active linter set.
- No `fmt.Printf` in non-test code — use the structured logger in `internal/monitoring`.
- No global mutable state outside of registered singletons.

### Package boundaries

- `pkg/` is the public API. Only add to `pkg/` what external consumers need. Do not expose `internal/` types directly.
- `internal/` packages must not import `pkg/` — the dependency goes one way.
- `internal/backend` does not exist in this repository — it lives in SeshatOS and SeshatCloud. Do not recreate it here.
- New tools go in `internal/tools/<category>/`. New providers go in `internal/providers/`.

**Multi-agent packages** — respect the dependency direction (no cycles):

```
internal/agent    →  AgentProfile, ProfileRegistry (who agents are)
internal/mailbox  →  Message, Mailbox              (how they communicate)
internal/team     →  Dispatcher, TeamBus           (how the team is coordinated)
```

`internal/team` imports `agent` and `mailbox`. Neither `agent` nor `mailbox` imports `team`.

See [`docs/architecture.md`](./docs/architecture.md) and [`docs/team.md`](./docs/team.md) for the full package map.

### Adding a new tool

The tool tree under `internal/tools/` is organised by category:

| Directory | Contains |
|---|---|
| `bash/` | Shell execution, background tasks, monitor |
| `files/` | Read, write, edit, grep, glob, patch, fs |
| `math/` | Calculator, units, statistics, financial |
| `multimedia/` | generate_image, text_to_speech, speech_to_text |
| `notebook/` | Jupyter notebook + kernel tools |
| `social/` | Hacker News, dev.to (and planned: Reddit, Twitter…) |
| `agents/` | spawn_agent, wait_agent, list_agents, send_message, close |
| `special/` | LSP, FIM, RAG, memory, worktree, ask_user, plan, goal |
| `system/` | MCP, skills, seshatskill |
| `task/` | Task management (create/update/list/stop/get) |
| `web/` | browser, fetch, search |

**Steps to add a standalone tool:**

1. Create `internal/tools/<category>/<toolname>.go` (or a sub-package if the tool has >1 file).
2. Implement the `tool.Tool` interface (see [`docs/tools.md`](./docs/tools.md)).
3. Register in `internal/tools/builtin/builtin.go`.
4. Add to the built-in tools reference in `docs/tools.md`.
5. Write at least one test.

**Adding a tool to a consolidated package** (`multimedia/`, `bash/`, `agents/`): add a new `.go` file inside the existing package directory, then register the constructor in `builtin.go`. No new sub-directory needed.

### Adding a new provider

1. Add the provider constant to `internal/providers/registry.go`.
2. Implement or reuse a wire-format adapter in `internal/providers/adapter.go`.
3. Add model entries to the provider info table.
4. Add the env var to `docs/transports.md` and `docs/providers.md`.
5. Add golden tests in `internal/providers/adapter_test.go`.

### Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short description>

[optional body]

[optional footer]
```

Types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`, `ci`.

Examples:
```
feat(tools): add generate_image tool with OpenAI and Gemini providers
fix(providers): handle rate limit retry-after header in anthropic adapter
docs(sdk): document RuntimeEventFn callback and event types
```

Breaking changes: append `!` and add a `BREAKING CHANGE:` footer.

---

## Testing

- Run `make test-race` before submitting — the race detector catches concurrency bugs.
- Tests must not make real API calls. Use mock providers or recorded fixtures.
- Security-sensitive code (sandbox, permissions) requires tests that assert the deny path, not just the allow path.
- Table-driven tests preferred for cases with multiple inputs.

---

## Pull request checklist

The **Gate CI check** runs automatically on every PR and must be green before merge. It covers:

| Check | Command |
|---|---|
| Build | `go build ./...` |
| Test (race detector) | `go test -race ./... -timeout 300s` |
| Format | `gofmt -l .` |
| Vet | `go vet ./...` |
| Lint | `golangci-lint run ./...` |

Before pushing, run locally:

```bash
make build
make test-race
gofmt -w .
go vet ./...
```

Additional review checklist:

- [ ] New code is covered by tests
- [ ] Public API changes are reflected in `docs/`
- [ ] Commit messages follow Conventional Commits
- [ ] Breaking changes are called out in the PR description
- [ ] PR targets `dev`, not `main`

---

## What we will not merge

- Features that move `internal/backend` concerns into seshat (auth, users, orgs, billing).
- Provider-specific logic that bypasses the adapter interface.
- External HTTP calls in the main execution path without explicit user configuration.
- Changes that disable or weaken the permission system by default.
- New global mutable state.
- Code that uses `//nolint` without an explanation comment.

---

## Code of conduct

This project follows the [Contributor Covenant](./CODE_OF_CONDUCT.md). Be respectful and constructive.

---

## License

By contributing, you agree that your contributions will be licensed under the [Apache 2.0 License](./LICENSE).
