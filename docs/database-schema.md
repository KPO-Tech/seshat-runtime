# Seshat CLI — Database Schema & Configuration Reference

> Scope: CLI only (`~/.config/seshat-cli/`). The backend server uses a separate
> schema managed in the private product repository.

---

## 1. Runtime Root & File Layout

The CLI sets `SESHAT_RUNTIME_ROOT=~/.config/seshat-cli` before calling
`pkg/config.Load()`. Everything is derived from that root.

```
~/.config/seshat-cli/               ← SESHAT_RUNTIME_ROOT (CLI)
├── config.yaml                    ← optional YAML config file
├── data/
│   └── seshat.db                   ← single SQLite file (all persistence)
│   └── seshat.db-shm               ← WAL shared-memory file (auto-managed)
│   └── seshat.db-wal               ← WAL journal (auto-managed)
├── skills/                        ← cloned skill repositories
├── cache/
├── logs/
├── storage/                       ← local file-storage backend
└── tmp/
    ├── tasks/
    └── bash-tasks/

~/.config/seshat/                   ← DEFAULT runtime root (server / general)
└── secret.key                     ← 32-byte AES-256 encryption key (0600)
                                      shared between CLI and server on the
                                      same machine — intentional design
```

**Path helpers** (`pkg/runtimepath`):

| Function | Returns |
|---|---|
| `ResolveRoot(explicit)` | Resolves root: explicit → `SESHAT_RUNTIME_ROOT` → `~/.config/seshat` → `$TMP/seshat` |
| `BackendDBPath(root)` | `<root>/data/seshat.db` |
| `DataDir(root)` | `<root>/data` |
| `SkillsDir(root)` | `<root>/skills` |
| `PlansDir(root)` | `<root>/plans` |
| `TasksDir(root)` | `<root>/tmp/tasks` |
| `StorageDir(root)` | `<root>/storage` |

---

## 2. Database Connection Configuration

**Driver**: SQLite only for the CLI. Multi-driver support exists in the codebase
(`postgres`, `mysql`) but `tui/cmd/cli` always calls `db.DefaultSQLiteConfig(path)`.

**Open sequence** (`internal/db/db.go`):

```
db.Open(ctx, db.DefaultSQLiteConfig(path))
  1. gorm.Open(glebarez/sqlite, DSN)   ← pure-Go SQLite driver, no CGO
  2. db.configure(ctx, cfg)            ← apply pragmas + pool settings
  3. sqlDB.PingContext(ctx)
  4. db.Initialize(ctx)                ← run sqliteCoreMigrations() if AutoMigrate=true
```

**SQLite pragmas** (applied on every connection open):

```sql
PRAGMA foreign_keys       = ON          -- FK constraints enforced
PRAGMA journal_mode       = WAL         -- concurrent reads during writes
PRAGMA synchronous        = NORMAL      -- fsync after WAL checkpoint, not every write
PRAGMA cache_size         = -20000      -- 20 MB page cache (default ~2 MB)
PRAGMA mmap_size          = 134217728   -- 128 MB memory-mapped I/O
PRAGMA temp_store         = MEMORY      -- temp tables in RAM, never disk
PRAGMA wal_autocheckpoint = 1000        -- checkpoint every 1 000 WAL pages
PRAGMA busy_timeout       = 5000        -- wait 5 s on lock before SQLITE_BUSY
```

**`PRAGMA optimize`** is called in `db.Close()` to update query planner statistics.

**Connection pool** (SQLite limitation):

```go
sqlDB.SetMaxOpenConns(1)   // SQLite is single-writer
sqlDB.SetMaxIdleConns(1)
```

---

## 3. Complete Schema — Core SQLite Tables

All tables below are created by versioned migrations in
`internal/db/migrations_sqlite_core.go` and tracked in `seshat_schema_migrations`.

---

### 3.1 `seshat_schema_migrations` — Migration Tracking

```sql
CREATE TABLE seshat_schema_migrations (
    id             TEXT    NOT NULL,          -- migration ID string
    scope          TEXT    NOT NULL,          -- "core_sqlite" for CLI tables
    applied_at_unix INTEGER NOT NULL,
    PRIMARY KEY (id, scope)
);
```

---

### 3.2 `session_metadata` — Session Registry

One row per conversation session.

```sql
CREATE TABLE session_metadata (
    session_id      TEXT    PRIMARY KEY,      -- UUID string
    status          TEXT    NOT NULL,         -- "active" | "archived" | ...
    created_at_unix INTEGER NOT NULL,
    updated_at_unix INTEGER NOT NULL,
    metadata_json   TEXT    NOT NULL          -- JSON blob of SessionMetadata struct
);

CREATE INDEX idx_session_metadata_updated_at
    ON session_metadata(updated_at_unix DESC);
```

**Query served**: `ListSessions()` — ordered by most recently updated.

---

### 3.3 `session_transcript_entries` — Message History

One row per SDK message in a session.

```sql
CREATE TABLE session_transcript_entries (
    session_id  TEXT    NOT NULL,
    entry_index INTEGER NOT NULL,
    entry_json  TEXT    NOT NULL,             -- JSON blob of TranscriptEntry
    PRIMARY KEY (session_id, entry_index),
    FOREIGN KEY (session_id) REFERENCES session_metadata(session_id)
        ON DELETE CASCADE
);
```

**`entry_json` structure** (relevant TUI fields):
```json
{
  "role": "assistant",
  "content": [
    {
      "type": "tool_result",
      "tool_use_id": "toolu_01...",
      "content": [...],
      "metadata": {
        "content":                "raw file content",
        "type":                   "read_file | write_file | edit_file ...",
        "file_path":              "/abs/path/to/file",
        "execution_duration_ms":  120,
        "lines_added":            5,
        "lines_removed":          2,
        "structured_patch":       "unified diff string",
        "git_diff":               "git diff output",
        "original_file":          "content before edit"
      }
    }
  ]
}
```

**Search**: via `session_transcript_fts` FTS5 table (see §3.8).

---

### 3.4 `session_checkpoints` — Conversation State Snapshot

One row per session, overwritten on each checkpoint save.

```sql
CREATE TABLE session_checkpoints (
    session_id      TEXT    PRIMARY KEY,
    updated_at_unix INTEGER NOT NULL,
    checkpoint_json TEXT    NOT NULL,         -- JSON blob of Checkpoint struct
    FOREIGN KEY (session_id) REFERENCES session_metadata(session_id)
        ON DELETE CASCADE
);
```

---

### 3.5 `session_files` — File Operations per Session

One row per file operation (write_file, edit_file, apply_patch).
`tool_use_id` links back to `session_transcript_entries.entry_json`
to retrieve the full diff/metadata without scanning the transcript.

```sql
CREATE TABLE session_files (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT    NOT NULL,
    tool_use_id     TEXT    NOT NULL DEFAULT '',  -- pointer into transcript JSON
    file_path       TEXT    NOT NULL,
    operation       TEXT    NOT NULL,             -- "create" | "update" | "edit" | "patch"
    timestamp_unix  INTEGER NOT NULL,
    lines_added     INTEGER NOT NULL DEFAULT 0,
    lines_removed   INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (session_id) REFERENCES session_metadata(session_id)
        ON DELETE CASCADE
);

-- Primary access pattern: all file ops for a session, chronologically
CREATE INDEX idx_session_files_session
    ON session_files(session_id, timestamp_unix);

-- Reverse lookup: which sessions touched a file
CREATE INDEX idx_session_files_path
    ON session_files(file_path);

-- Direct lookup of transcript metadata for a specific tool call
CREATE INDEX idx_session_files_tool_use
    ON session_files(tool_use_id);

-- Prevents duplicate rows when live-recording and backfill goroutines race
CREATE UNIQUE INDEX idx_session_files_tool_use_unique
    ON session_files(tool_use_id) WHERE tool_use_id != '';
```

**Write paths** (both in `tui/cmd/cli/tui.go` only, never in internal packages):

- `recordSessionFile()` — called async when a tool completes during a live turn
- `backfillSessionFiles()` — called once on session load if no rows exist yet (idempotent)

---

### 3.6 `vector_records` — Embedding Store

Key-value store for RAG embeddings. Namespace groups records by domain.

```sql
CREATE TABLE vector_records (
    namespace TEXT NOT NULL,
    key       TEXT NOT NULL,
    text      TEXT NOT NULL DEFAULT '',
    vector    BLOB NOT NULL,                  -- raw float32 or float64 bytes
    metadata  TEXT NOT NULL DEFAULT '{}',     -- JSON extra fields
    PRIMARY KEY (namespace, key)
);

CREATE INDEX idx_vector_records_namespace
    ON vector_records(namespace);
```

---

### 3.7 `vector_records_fts` — FTS5 Hybrid Search

BM25 full-text index over the `text` column of `vector_records`.
Used alongside cosine-similarity vector search for hybrid retrieval.

```sql
CREATE VIRTUAL TABLE vector_records_fts USING fts5(
    namespace UNINDEXED,
    key       UNINDEXED,
    text,
    tokenize  = 'unicode61 remove_diacritics 1'
);
```

---

### 3.8 `session_transcript_fts` — Transcript Full-Text Search

FTS5 index over `session_transcript_entries.entry_json`.
Replaces the previous O(n) `LIKE` scan with O(log n) MATCH queries.

```sql
CREATE VIRTUAL TABLE session_transcript_fts USING fts5(
    session_id UNINDEXED,
    entry_json,
    tokenize   = 'unicode61 remove_diacritics 1'
);
```

**Synchronization triggers** (fire automatically, including for CASCADE deletes):

```sql
-- Sync on insert
CREATE TRIGGER trg_transcript_fts_insert
AFTER INSERT ON session_transcript_entries BEGIN
    INSERT OR REPLACE INTO session_transcript_fts(rowid, session_id, entry_json)
    VALUES (new.rowid, new.session_id, new.entry_json);
END;

-- Sync on delete (fires for CASCADE removes triggered by deleting session_metadata)
CREATE TRIGGER trg_transcript_fts_delete
AFTER DELETE ON session_transcript_entries BEGIN
    INSERT INTO session_transcript_fts(session_transcript_fts, rowid)
    VALUES ('delete', old.rowid);
END;
```

---

### 3.9 `credentials` — Encrypted Key-Value Store

All secrets (API keys, provider credentials, search keys) are stored here,
never in the YAML config file.

```sql
CREATE TABLE credentials (
    key             TEXT    PRIMARY KEY,           -- max 191 chars
    cipher_text     TEXT    NOT NULL,              -- base64(AES-256-GCM nonce || ciphertext)
    created_at_unix INTEGER NOT NULL,
    updated_at_unix INTEGER NOT NULL
);
```

**Encryption**: AES-256-GCM. Key file: `~/.config/seshat/secret.key` (32 bytes, mode 0600).

**Credential keys used by the CLI**:

| DB key | Purpose |
|---|---|
| `api_key` | Provider API key (scoped: `api_key:<provider_id>`) |
| `provider_base_url` | Custom base URL for Ollama |
| `model` | Persisted model selection (e.g. `anthropic:claude-opus-4-8`) |
| `TAVILY_API_KEY` | Tavily web search |
| `EXA_API_KEY` | Exa web search |
| `JINA_API_KEY` | Jina web search |
| `web_search_provider` | Active search provider mode (`auto`, `tavily`, …) |

---

### 3.10 `agent_profiles` — Multi-Agent Profiles

```sql
CREATE TABLE agent_profiles (
    id                      TEXT    PRIMARY KEY,  -- UUID (size 36)
    nickname                TEXT    NOT NULL,
    role                    TEXT    NOT NULL,
    team_id                 TEXT,
    system_prompt_template  TEXT    NOT NULL,     -- {{.Nickname}} resolved at runtime
    model                   TEXT,                 -- optional model override
    skills_json             TEXT    NOT NULL DEFAULT '[]',
    metadata_json           TEXT    NOT NULL DEFAULT '{}',
    created_at_unix         INTEGER NOT NULL,
    updated_at_unix         INTEGER NOT NULL
);

CREATE INDEX idx_agent_profiles_role    ON agent_profiles(role);
CREATE INDEX idx_agent_profiles_team_id ON agent_profiles(team_id);
```

---

### 3.11 `mailbox_messages` — Inter-Agent Messaging

```sql
CREATE TABLE mailbox_messages (
    id         TEXT    PRIMARY KEY,  -- UUID
    kind       TEXT    NOT NULL,     -- message type
    from_agent TEXT    NOT NULL,
    to_agent   TEXT    NOT NULL,
    subject    TEXT    NOT NULL,
    body       TEXT    NOT NULL,
    reply_to   TEXT,                 -- parent message ID for threading
    team_id    TEXT,
    read_at    INTEGER,              -- NULL = unread
    created_at INTEGER NOT NULL
);

-- Single-column indexes (GORM AutoMigrate)
CREATE INDEX idx_mailbox_messages_kind       ON mailbox_messages(kind);
CREATE INDEX idx_mailbox_messages_from_agent ON mailbox_messages(from_agent);
CREATE INDEX idx_mailbox_messages_to_agent   ON mailbox_messages(to_agent);
CREATE INDEX idx_mailbox_messages_reply_to   ON mailbox_messages(reply_to);
CREATE INDEX idx_mailbox_messages_team_id    ON mailbox_messages(team_id);
CREATE INDEX idx_mailbox_messages_created_at ON mailbox_messages(created_at);

-- Composite indexes (migration 008)
-- GetUnreadMessages: to_agent + unread filter + ASC order
CREATE INDEX idx_mailbox_to_agent_unread
    ON mailbox_messages(to_agent, created_at ASC) WHERE read_at IS NULL;

-- GetMessageHistory: to_agent + newest-first order
CREATE INDEX idx_mailbox_to_agent_history
    ON mailbox_messages(to_agent, created_at DESC);
```

---

### 3.12 `schema_migrations` — Legacy Tracking (backward compat)

```sql
CREATE TABLE schema_migrations (
    id             TEXT    PRIMARY KEY,
    applied_at_unix INTEGER NOT NULL DEFAULT (unixepoch())
);
```

Kept for databases created before `seshat_schema_migrations` was introduced.
Not written by any current migration.

---

## 4. Entity Relationship Diagram

```
┌──────────────────────────────────────────────────────────┐
│                    session_metadata                      │
│  PK: session_id TEXT                                     │
│      status, created_at_unix, updated_at_unix            │
│      metadata_json                                       │
│  IDX: updated_at_unix DESC                               │
└──────┬───────────────────┬──────────────────┬────────────┘
       │ CASCADE           │ CASCADE          │ CASCADE
       ▼                   ▼                  ▼
┌──────────────────┐ ┌────────────────┐ ┌────────────────────────────────┐
│session_transcript│ │session_        │ │session_files                   │
│_entries          │ │checkpoints     │ │  PK: id AUTOINCREMENT          │
│  PK: (session_id,│ │  PK: session_id│ │  FK: session_id → session_meta │
│       entry_index)│ │  FK: session_id│ │  tool_use_id ─────────────┐   │
│  FK: session_id  │ │  checkpoint_   │ │  file_path, operation      │   │
│  entry_json TEXT │ │  json TEXT     │ │  timestamp_unix            │   │
│                  │ └────────────────┘ │  lines_added, lines_removed│   │
│  rowid ──────────┼──────────────────────────────────────────────────┐ │
└──────────────────┘                   └────────────────────────────────┘ │
       │                                                                   │
       │ triggers (INSERT/DELETE)                                          │
       ▼                                                                   │
┌──────────────────────────────────────────────┐                          │
│ session_transcript_fts  (FTS5 virtual)        │                          │
│   session_id UNINDEXED                        │                          │
│   entry_json (BM25 indexed)                   │                          │
│   tokenize: unicode61, remove_diacritics=1    │                          │
└──────────────────────────────────────────────┘                          │
                                                                           │
       ┌───────────────────────────────────────────────────────────────────┘
       │ logical pointer (no SQL FK)
       │ tool_use_id → session_transcript_entries.entry_json
       │              → ToolResultContent.Metadata
       │              → { content, structured_patch, git_diff, … }
       ▼
  session_transcript_entries (look up by rowid via idx_session_files_tool_use)


┌──────────────────────────────────────────────┐
│ vector_records                                │
│   PK: (namespace, key)                        │
│   text, vector BLOB, metadata                 │
│   IDX: namespace                              │
└──────────┬───────────────────────────────────┘
           │ INSERT OR IGNORE backfill on migration
           ▼
┌──────────────────────────────────────────────┐
│ vector_records_fts  (FTS5 virtual)            │
│   namespace UNINDEXED, key UNINDEXED, text    │
│   tokenize: unicode61, remove_diacritics=1    │
└──────────────────────────────────────────────┘

┌──────────────────────────────────────────────┐
│ credentials                                   │
│   PK: key TEXT                                │
│   cipher_text (AES-256-GCM base64)            │
│   created_at_unix, updated_at_unix            │
└──────────────────────────────────────────────┘

┌──────────────────────────────────────────────┐
│ agent_profiles                                │
│   PK: id UUID                                 │
│   IDX: role, team_id                         │
└──────────────────────────────────────────────┘
         (no FK to sessions — profiles are global)

┌──────────────────────────────────────────────┐
│ mailbox_messages                              │
│   PK: id UUID                                 │
│   reply_to → id (self-referencing, logical)   │
│   IDX: kind, from_agent, to_agent, reply_to,  │
│        team_id, created_at                    │
│   IDX: (to_agent, created_at) WHERE unread    │
│   IDX: (to_agent, created_at DESC)            │
└──────────────────────────────────────────────┘
```

---

## 5. Index Coverage Map

| Table | Query | Index used | Complexity |
|---|---|---|---|
| `session_metadata` | List sessions by recency | `idx_session_metadata_updated_at` | O(log n) |
| `session_transcript_entries` | Load full transcript | PK `(session_id, entry_index)` | O(log n) |
| `session_transcript_entries` | Count rows | PK scan | O(log n) |
| `session_transcript_fts` | Search text content | FTS5 MATCH | O(log n) |
| `session_checkpoints` | Load checkpoint | PK `session_id` | O(1) |
| `session_files` | Files for a session | `idx_session_files_session` | O(log n) |
| `session_files` | Sessions for a file | `idx_session_files_path` | O(log n) |
| `session_files` | Metadata by tool_use_id | `idx_session_files_tool_use` | O(log n) |
| `vector_records` | All records in namespace | `idx_vector_records_namespace` | O(log n) |
| `vector_records_fts` | BM25 text search | FTS5 MATCH | O(log n) |
| `credentials` | Single key lookup | PK `key` | O(1) |
| `credentials` | List all keys | PK full scan | O(n) |
| `agent_profiles` | By role | `idx_agent_profiles_role` | O(log n) |
| `agent_profiles` | By team | `idx_agent_profiles_team_id` | O(log n) |
| `mailbox_messages` | Unread for agent | `idx_mailbox_to_agent_unread` (partial) | O(log n) |
| `mailbox_messages` | History for agent | `idx_mailbox_to_agent_history` | O(log n) |
| `mailbox_messages` | Thread by reply_to | `idx_mailbox_messages_reply_to` | O(log n) |
| `mailbox_messages` | Team broadcast | `idx_mailbox_messages_team_id` | O(log n) |

---

## 6. Migration System

**Tracking table**: `seshat_schema_migrations` (PK: `id + scope`)

**Scope**: `core_sqlite` — all CLI/engine-owned migrations

**Applied via**: `db.Initialize(ctx)` → `runSQLiteCoreMigrations()` → `applyMigrations()`

Each migration runs in its own transaction. If it fails, the transaction is
rolled back and startup aborts. Migrations are idempotent (all DDL uses
`CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`).

**Migration history**:

| ID | Description |
|---|---|
| `20260514_001_runtime_session_tables` | `session_metadata`, `session_transcript_entries`, `session_checkpoints` |
| `20260514_002_runtime_vector_records` | `vector_records` |
| `20260531_003_vector_records_fts5` | `vector_records_fts` FTS5 virtual table |
| `20260603_004_credentials_table` | `credentials` |
| `20260604_005_agent_profiles` | `agent_profiles` |
| `20260604_006_mailbox_messages` | `mailbox_messages` |
| `20260607_007_session_files` | `session_files` + 3 indexes |
| `20260607_008_indexes_and_constraints` | Unique constraint on `session_files.tool_use_id`; composite indexes on `mailbox_messages` |
| `20260607_009_transcript_fts5` | `session_transcript_fts` FTS5 + INSERT/DELETE triggers |

---

## 7. Configuration Chain

```
cmd/cli main()
  └─ os.Setenv("SESHAT_RUNTIME_ROOT", "~/.config/seshat-cli")
       └─ pkg/config.Load()
            └─ viper reads ~/.config/seshat-cli/config.yaml   (if present)
            └─ viper reads env vars (prefix: SESHAT_)
            └─ config.RuntimeRoot = runtimepath.ResolveRoot("")
                 → "~/.config/seshat-cli"
            └─ ExpandShellValues(config)    -- resolves $(...) in yaml values
            └─ ApplyRuntimeEnv(config)      -- sets env vars for sub-packages

       └─ openCredentialsDB(config)
            └─ engineconfig.EffectiveSessionDBPath(config)
                 → config.SessionDBPath  if set (SESHAT_SESSION_DB_PATH)
                 → config.DBPath         if set (SESHAT_DB_PATH)
                 → runtimepath.BackendDBPath(config.RuntimeRoot)
                      → ~/.config/seshat-cli/data/seshat.db
            └─ db.Open(ctx, db.DefaultSQLiteConfig(path))
                 → applies all pragmas
                 → runs sqliteCoreMigrations()
                 → returns *db.DB handle

       └─ loadCredsIntoConfig(config, database)
            -- pulls api_key, model, search keys from credentials table
            -- decrypts with AES-256-GCM key from ~/.config/seshat/secret.key

       └─ ApplySearchKeys(config)
            -- sets TAVILY_API_KEY, EXA_API_KEY, JINA_API_KEY env vars
```

---

## 8. Config File (`~/.config/seshat-cli/config.yaml`)

Secrets are **never stored** in this file (`stripRuntimeSecrets()` removes them
before saving). Use the TUI settings screen or `SESHAT_*` env vars instead.

```yaml
# Model — override the persisted selection from credentials table
model: "anthropic:claude-opus-4-8"

# Working directory for file tools
cwd: "."

# Generation parameters
max_tokens: 4096
temperature: 0.7

# Feature flags
mcp_enabled: true
skills_enabled: true
debug: false

# Override database paths (rarely needed)
db_path: ""
session_db_path: ""

# Web search provider (auto | tavily | exa | jina | langsearch | ddg)
web_search_provider: "auto"

# Hooks: shell commands that fire on lifecycle events
hooks:
  pre_tool_use:
    - command: "echo tool called"
      matcher: "bash"    # optional regex match on tool name
      timeout: 10        # seconds
```

**All config fields with their env vars**:

| Field | Env var | Default | Notes |
|---|---|---|---|
| `runtime_root` | `SESHAT_RUNTIME_ROOT` | `~/.config/seshat` | CLI overrides to `~/.config/seshat-cli` |
| `cwd` | `SESHAT_CWD` | `.` | Working directory for file tools |
| `model` | `SESHAT_MODEL` | `""` | Format: `provider:model` or bare model name |
| `max_tokens` | — | `4096` | Max output tokens per turn |
| `temperature` | — | `0.7` | Sampling temperature |
| `api_key` | `SESHAT_API_KEY` | `""` | Loaded from credentials DB at runtime |
| `db_path` | `SESHAT_DB_PATH` | `""` | Overrides default SQLite path |
| `session_db_path` | `SESHAT_SESSION_DB_PATH` | `""` | Separate session DB (optional) |
| `provider_base_url` | `SESHAT_PROVIDER_BASE_URL` | `""` | Ollama custom endpoint |
| `mcp_enabled` | — | `true` | Enable MCP server integrations |
| `skills_enabled` | — | `true` | Enable slash-command skills |
| `debug` | `SESHAT_DEBUG` | `false` | Verbose logging |
| `web_search_provider` | `WEB_SEARCH_PROVIDER` | `"auto"` | Active search provider |
| `skill_repos` | `SESHAT_SKILL_REPOS` | `""` | Comma-separated git URLs to clone |
| `default_skill_repo` | `SESHAT_DEFAULT_SKILL_REPO` | canonical seshat-skills URL | Set to `"none"` to disable |
| `hooks` | — | `{}` | Lifecycle hook commands |

---

## 9. Provider & Model Configuration

**Provider catalog** (`pkg/config/provider_catalog.go`):

| Provider ID | Display name | Auth type | Key env var |
|---|---|---|---|
| `anthropic` | Anthropic | API key | `ANTHROPIC_API_KEY` |
| `openai` | OpenAI | API key | `OPENAI_API_KEY` |
| `codex` | Codex | API key | `CODEX_API_KEY` |
| `gemini` | Google Gemini | API key | `GOOGLE_API_KEY` |
| `z-ai` | Z.AI (ZhipuAI) | API key | `ZHIPUAI_API_KEY` |
| `openrouter` | OpenRouter | API key | `OPENROUTER_API_KEY` |
| `deepseek` | DeepSeek | API key | `DEEPSEEK_API_KEY` |
| `opencode` | OpenCode | API key | `OPENCODE_API_KEY` |
| `mistral` | Mistral AI | API key | `MISTRAL_API_KEY` |
| `minimax` | MiniMax | API key | `MINIMAX_API_KEY` |
| `ollama` | Ollama (local) | None | — |

**Model identifier format**: `provider:model-name`

```
anthropic:claude-opus-4-8
openai:gpt-4o
ollama:llama3.3:70b
```

**Resolution order** (`EffectiveAPIKeyAndProvider`):
1. Explicit provider prefix in model string
2. Model name pattern match (detect provider from model name)
3. Check provider env vars in priority order
4. `config.APIKey` fallback + provider inference from key prefix
5. Provider inferred from model name alone

**Credential DB keys per provider** (stored as `api_key:<provider_id>`):

```
api_key:anthropic     → ANTHROPIC_API_KEY
api_key:openai        → OPENAI_API_KEY
api_key:ollama        → (not needed)
provider_base_url     → Ollama host
```

---

## 10. Credentials / Auth

**Storage**: `credentials` table, encrypted at rest with AES-256-GCM.

**Encryption flow**:
```
loadOrCreateEncryptionKey()
  1. Read ~/.config/seshat/secret.key (32 bytes)
  2. If absent, try legacy ~/.seshat_secret (migration)
  3. If absent, generate new key with crypto/rand, write with mode 0600

encryptAESGCM(key, plaintext)
  → nonce (12 bytes random) || ciphertext (AES-256-GCM sealed)
  → base64-encode the whole thing → stored as cipher_text

decryptAESGCM(key, encoded)
  → base64-decode → split nonce | ciphertext → GCM.Open → plaintext
```

**Key note**: The secret key lives in `~/.config/seshat/secret.key` even when
the CLI uses `~/.config/seshat-cli/data/seshat.db`. This is intentional — both
CLI and server on the same machine share one key so credentials can be
migrated or shared between the two DB files if needed.

**Session auth / JWT**: Not applicable to the CLI. The CLI authenticates to
provider APIs directly using the stored API key. There is no JWT or session
cookie in the CLI flow.

---

## 11. Skills System

**Location**: `~/.config/seshat-cli/skills/<repo-name>/`

Skills are cloned git repositories. Each skill is a YAML file:

```yaml
name: my-skill
description: "One-line description"
when_to_use: "When to invoke this skill"
content: |
  # Skill instructions sent to the model
  ...
```

**Config fields**:

| Field | Purpose |
|---|---|
| `SESHAT_SKILL_REPOS` | Comma-separated git URLs cloned at startup |
| `SESHAT_DEFAULT_SKILL_REPO` | Official seshat-skills repo (auto-cloned on first boot) |
| `SESHAT_FEATURED_SKILL_REPOS` | Shown as installable catalog in the UI |
| `SESHAT_SKILL_REPO_HOSTS` | Allowed git hosts for skill installation (default: github.com, gitlab.com, bitbucket.org, codeberg.org) |

Skills are exposed as `/skill-name` slash commands in the TUI.
`LoadSkills(ctx)` in the `Workspace` interface returns all available skills.

---

## 12. Vector Store / RAG

**Default vector store**: SQLite (`vector_records` table).

**Alternative stores** (configured via env or config.yaml):

| Store | Config key | Notes |
|---|---|---|
| `sqlite` | `SESHAT_VECTOR_STORE=sqlite` | Default, no extra deps |
| `pgvector` | `SESHAT_PGVECTOR_DSN=postgres://...` | Requires pgvector extension |
| `qdrant` | `QDRANT_HOST`, `QDRANT_PORT` | External Qdrant service |
| `opensearch` | `SESHAT_VECTOR_STORE=opensearch`, `OPENSEARCH_ADDRESSES` | External OpenSearch service for enterprise search |
| `chroma` | `CHROMA_URL` | External Chroma service |
| `memory` | `SESHAT_VECTOR_STORE=memory` | In-process, no persistence |

**Embedder config**:

| Env var | Purpose |
|---|---|
| `RAG_EMBEDDING_URL` | Base URL for embedding API |
| `RAG_EMBEDDING_API_KEY` | Embedding API key |
| `RAG_EMBEDDING_MODEL` | Model identifier for embeddings |
| `RAG_EMBEDDING_PROVIDER` | Provider name |

Not configured → RAG still works in vectorless (BM25/keyword) mode, no
embedding provider required. See `internal/rag/service.go`.

**Reranker config** (optional second-pass reranking after retrieval):

| Env var | Purpose |
|---|---|
| `RAG_RERANK_URL` | Full rerank endpoint URL - point this at a self-hosted server (e.g. a local TEI or vLLM instance serving `BAAI/bge-reranker-v2-m3`) for a free alternative requiring no API key |
| `RAG_RERANK_API_KEY` | Optional; self-hosted servers usually don't need one |
| `RAG_RERANK_MODEL` | Model name; ignored by single-model servers such as TEI |
| `LANGSEARCH_API_KEY` | Fallback: used for LangSearch's hosted rerank API when `RAG_RERANK_URL` isn't set |

Neither configured → search results are returned in vector/BM25 order with
no reranking pass. See `internal/rag/reranker/reranker.go`.

---

## 13. MCP Servers

MCP (Model Context Protocol) server configuration is stored in the credentials
table (under `mcp_servers_json` key) and managed through the TUI settings screen.
`LoadMCPServers(ctx)` on the `Workspace` interface returns connected servers and
their tool counts.

MCP is enabled by default (`mcp_enabled: true`). Set `mcp_enabled: false` in
`config.yaml` or `SESHAT_MCP_ENABLED=false` to disable.

---

## 14. Hooks

Hooks are shell commands that fire on lifecycle events. Defined in `config.yaml`:

```yaml
hooks:
  pre_tool_use:
    - command: "my-audit-script"
      matcher: "bash"   # optional: only fire for tools matching this regex
      timeout: 30       # seconds before the hook is killed
```

**Supported events**: `pre_tool_use` (fires before every tool call).

Hook output is shown in the TUI as a system message. A non-zero exit code
blocks the tool call and shows the error to the user.

---

## 15. Execution Modes

The engine supports three modes, switchable via special tools:

| Mode | DB key | TUI badge | Tools |
|---|---|---|---|
| `execute` | default | `● execute` (muted) | All tools enabled |
| `plan` | `enter_plan_mode` / `exit_plan_mode` | `◈ plan` (primary) | Restricted: no write tools |
| `pair_programming` | `enter_pair_programming_mode` / `exit_pair_programming_mode` | `◎ pair` (secondary) | All tools, user confirms each |

Mode transitions are tracked in `chat.planDepth` / `chat.pairDepth` counters
(incremented/decremented as enter/exit tools complete).
