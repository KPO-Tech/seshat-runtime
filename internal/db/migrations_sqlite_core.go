package db

import (
	"context"
	"log"
)

func sqliteCoreMigrations() []schemaMigration {
	return []schemaMigration{
		{
			ID:    "20260514_001_runtime_session_tables",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteRuntimeSessionTables,
		},
		{
			ID:    "20260514_002_runtime_vector_records",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteRuntimeVectorRecords,
		},
		{
			// FTS5 virtual table for BM25 hybrid search alongside the vector_records table.
			// Stores (namespace, key, text) so BM25 scores can be joined back by namespace+key.
			// The tokenizer uses unicode61 with diacritic removal for broad language support.
			ID:    "20260531_003_vector_records_fts5",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteVectorFTS5,
		},
		{
			ID:    "20260603_004_credentials_table",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteCredentials,
		},
		{
			ID:    "20260604_005_agent_profiles",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteAgentProfiles,
		},
		{
			ID:    "20260604_006_mailbox_messages",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteMailboxMessages,
		},
		{
			ID:    "20260607_007_session_files",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteSessionFiles,
		},
		{
			// Unique constraint on session_files.tool_use_id prevents duplicate records
			// from concurrent live-recording and backfill goroutines.
			// Composite indexes on mailbox_messages cover the two hottest query shapes:
			//   GetUnreadMessages (to_agent + read_at IS NULL + created_at ASC)
			//   GetMessageHistory (to_agent + created_at DESC)
			ID:    "20260607_008_indexes_and_constraints",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteIndexesAndConstraints,
		},
		{
			// FTS5 virtual table for O(log n) transcript full-text search.
			// INSERT/DELETE triggers keep it in sync with session_transcript_entries,
			// including rows removed by ON DELETE CASCADE from session_metadata.
			ID:    "20260607_009_transcript_fts5",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteTranscriptFTS5,
		},
		{
			// Rebuild FTS5 virtual table and triggers to cleanly fix legacy FTS3/4 delete trigger syntax.
			ID:    "20260608_010_rebuild_transcript_fts5",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteTranscriptFTS5Rebuild,
		},
		{
			ID:    "20260612_011_session_tasks",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteSessionTasks,
		},
		{
			ID:    "20260615_012_longterm_memory",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteLongtermMemory,
		},
		{
			ID:    "20260618_013_teams",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteTeams,
		},
		{
			ID:    "20260629_014_automation_jobs",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteAutomationJobs,
		},
		{
			ID:    "20260629_015_automation_agent_slug",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteAutomationAgentSlug,
		},
		{
			ID:    "20260801_016_session_goals",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteSessionGoals,
		},
		{
			ID:    "20260831_017_automation_job_limits",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteAutomationJobLimits,
		},
		{
			ID:    "20261018_018_memory_snapshots",
			Scope: migrationScopeCoreSQLite,
			Run:   migrateSQLiteMemorySnapshots,
		},
	}
}

// migrateSQLiteMemorySnapshots adds the table that holds a copy of a user's long-term memory taken before it is
// rewritten, so that a consolidation can be undone.
func migrateSQLiteMemorySnapshots(ctx context.Context, db *DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS memory_snapshots (
			id              TEXT    PRIMARY KEY,
			user_id         TEXT    NOT NULL,
			created_at_unix INTEGER NOT NULL,
			reason          TEXT    NOT NULL DEFAULT '',
			entities_json   TEXT    NOT NULL,
			relations_json  TEXT    NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_snapshots_user ON memory_snapshots(user_id, created_at_unix)`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteRuntimeSessionTables(ctx context.Context, db *DB) error {
	statements := []string{
		// Kept for backward compatibility with older SQLite runtime databases.
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			id TEXT PRIMARY KEY,
			applied_at_unix INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`CREATE TABLE IF NOT EXISTS session_metadata (
			session_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			created_at_unix INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL,
			metadata_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_metadata_updated_at
			ON session_metadata(updated_at_unix DESC)`,
		`CREATE TABLE IF NOT EXISTS session_transcript_entries (
			session_id TEXT NOT NULL,
			entry_index INTEGER NOT NULL,
			entry_json TEXT NOT NULL,
			PRIMARY KEY (session_id, entry_index),
			FOREIGN KEY (session_id) REFERENCES session_metadata(session_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS session_checkpoints (
			session_id TEXT PRIMARY KEY,
			updated_at_unix INTEGER NOT NULL,
			checkpoint_json TEXT NOT NULL,
			FOREIGN KEY (session_id) REFERENCES session_metadata(session_id) ON DELETE CASCADE
		)`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteSessionGoals(ctx context.Context, db *DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS session_goals (
			session_id TEXT PRIMARY KEY,
			objective TEXT NOT NULL,
			status TEXT NOT NULL,
			token_budget INTEGER,
			tokens_used INTEGER NOT NULL DEFAULT 0,
			created_at_unix_ms INTEGER NOT NULL,
			updated_at_unix_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_goals_status
			ON session_goals(status)`,
		`CREATE INDEX IF NOT EXISTS idx_session_goals_updated_at
			ON session_goals(updated_at_unix_ms DESC)`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteVectorFTS5(ctx context.Context, db *DB) error {
	statements := []string{
		// FTS5 virtual table: namespace and key are stored but not full-text indexed;
		// only the text column participates in BM25 scoring.
		`CREATE VIRTUAL TABLE IF NOT EXISTS vector_records_fts
		 USING fts5(
		 	namespace UNINDEXED,
		 	key       UNINDEXED,
		 	text,
		 	tokenize  = 'unicode61 remove_diacritics 1'
		 )`,
		// Populate FTS5 from any existing vector_records rows.
		`INSERT OR IGNORE INTO vector_records_fts(namespace, key, text)
		 SELECT namespace, key, text FROM vector_records`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			// FTS5 is an enhancement; a failure must not block startup.
			// Hybrid search degrades to LIKE scan when FTS5 is unavailable.
			log.Printf("[db] fts5 vector migration warning (hybrid search may be degraded): %v", err)
		}
	}
	return nil
}

func migrateSQLiteRuntimeVectorRecords(ctx context.Context, db *DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS vector_records (
			namespace TEXT    NOT NULL,
			key       TEXT    NOT NULL,
			text      TEXT    NOT NULL DEFAULT '',
			vector    BLOB    NOT NULL,
			metadata  TEXT    NOT NULL DEFAULT '{}',
			PRIMARY KEY (namespace, key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_vector_records_namespace
			ON vector_records(namespace)`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteCredentials(ctx context.Context, db *DB) error {
	return db.gormDB.WithContext(ctx).AutoMigrate(&gCredential{})
}

func migrateSQLiteAgentProfiles(ctx context.Context, db *DB) error {
	return db.gormDB.WithContext(ctx).AutoMigrate(&GAgentProfile{})
}

func migrateSQLiteMailboxMessages(ctx context.Context, db *DB) error {
	return db.gormDB.WithContext(ctx).AutoMigrate(&GMailboxMessage{})
}

func migrateSQLiteIndexesAndConstraints(ctx context.Context, db *DB) error {
	statements := []string{
		// Deduplicate any existing session_files rows before adding the unique index
		// (duplicates can occur if a live-recording goroutine races with backfill).
		`DELETE FROM session_files
		 WHERE id NOT IN (
		     SELECT MIN(id) FROM session_files
		     WHERE tool_use_id != ''
		     GROUP BY tool_use_id
		 ) AND tool_use_id != ''`,
		// One row per tool_use_id (skips rows where tool_use_id is empty).
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_session_files_tool_use_unique
		     ON session_files(tool_use_id) WHERE tool_use_id != ''`,
		// Partial covering index: to_agent + created_at on unread messages only.
		// Covers WHERE to_agent = ? AND read_at IS NULL ORDER BY created_at ASC.
		`CREATE INDEX IF NOT EXISTS idx_mailbox_to_agent_unread
		     ON mailbox_messages(to_agent, created_at ASC) WHERE read_at IS NULL`,
		// Covering index for GetMessageHistory: to_agent + newest-first ordering.
		`CREATE INDEX IF NOT EXISTS idx_mailbox_to_agent_history
		     ON mailbox_messages(to_agent, created_at DESC)`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteTranscriptFTS5(ctx context.Context, db *DB) error {
	statements := []string{
		// FTS5 index over raw transcript JSON — enables MATCH queries replacing LIKE full scans.
		// session_id is stored but not tokenized (used only for grouping in DISTINCT queries).
		`CREATE VIRTUAL TABLE IF NOT EXISTS session_transcript_fts USING fts5(
		     session_id UNINDEXED,
		     entry_json,
		     tokenize = 'unicode61 remove_diacritics 1'
		 )`,
		// Backfill from any existing transcript entries.
		`INSERT OR IGNORE INTO session_transcript_fts(rowid, session_id, entry_json)
		 SELECT rowid, session_id, entry_json FROM session_transcript_entries`,
		// Trigger: keep FTS5 in sync on insert.
		`CREATE TRIGGER IF NOT EXISTS trg_transcript_fts_insert
		 AFTER INSERT ON session_transcript_entries BEGIN
		     INSERT OR REPLACE INTO session_transcript_fts(rowid, session_id, entry_json)
		     VALUES (new.rowid, new.session_id, new.entry_json);
		 END`,
		// Trigger: keep FTS5 in sync on delete (also fires for ON DELETE CASCADE rows).
		`DROP TRIGGER IF EXISTS trg_transcript_fts_delete`,
		`CREATE TRIGGER IF NOT EXISTS trg_transcript_fts_delete
		 AFTER DELETE ON session_transcript_entries BEGIN
		     DELETE FROM session_transcript_fts WHERE rowid = old.rowid;
		 END`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			// FTS5 is an enhancement; a failure must not block startup.
			// Transcript full-text search degrades to LIKE scan when FTS5 is unavailable.
			log.Printf("[db] fts5 transcript migration warning (search may be degraded): %v", err)
		}
	}
	return nil
}

func migrateSQLiteSessionFiles(ctx context.Context, db *DB) error {
	statements := []string{
		// tool_use_id links back to the ToolResultContent in session_transcript_entries
		// so the full metadata (structured_patch, git_diff, content, …) can be
		// retrieved without scanning the entire transcript JSON.
		`CREATE TABLE IF NOT EXISTS session_files (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id      TEXT    NOT NULL,
			tool_use_id     TEXT    NOT NULL DEFAULT '',
			file_path       TEXT    NOT NULL,
			operation       TEXT    NOT NULL,
			timestamp_unix  INTEGER NOT NULL,
			lines_added     INTEGER NOT NULL DEFAULT 0,
			lines_removed   INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (session_id) REFERENCES session_metadata(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_files_session
			ON session_files(session_id, timestamp_unix)`,
		`CREATE INDEX IF NOT EXISTS idx_session_files_path
			ON session_files(file_path)`,
		`CREATE INDEX IF NOT EXISTS idx_session_files_tool_use
			ON session_files(tool_use_id)`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteTranscriptFTS5Rebuild(ctx context.Context, db *DB) error {
	statements := []string{
		`DROP TRIGGER IF EXISTS trg_transcript_fts_insert`,
		`DROP TRIGGER IF EXISTS trg_transcript_fts_delete`,
		`DROP TABLE IF EXISTS session_transcript_fts`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS session_transcript_fts USING fts5(
		     session_id UNINDEXED,
		     entry_json,
		     tokenize = 'unicode61 remove_diacritics 1'
		 )`,
		`INSERT OR IGNORE INTO session_transcript_fts(rowid, session_id, entry_json)
		 SELECT rowid, session_id, entry_json FROM session_transcript_entries`,
		`CREATE TRIGGER IF NOT EXISTS trg_transcript_fts_insert
		 AFTER INSERT ON session_transcript_entries BEGIN
		     INSERT OR REPLACE INTO session_transcript_fts(rowid, session_id, entry_json)
		     VALUES (new.rowid, new.session_id, new.entry_json);
		 END`,
		`CREATE TRIGGER IF NOT EXISTS trg_transcript_fts_delete
		 AFTER DELETE ON session_transcript_entries BEGIN
		     DELETE FROM session_transcript_fts WHERE rowid = old.rowid;
		 END`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteSessionTasks(ctx context.Context, db *DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS session_tasks (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id       TEXT    NOT NULL,
			task_id          TEXT    NOT NULL,
			position         INTEGER NOT NULL,
			subject          TEXT    NOT NULL,
			description      TEXT    NOT NULL DEFAULT '',
			status           TEXT    NOT NULL,
			active_form      TEXT    NOT NULL DEFAULT '',
			owner            TEXT    NOT NULL DEFAULT '',
			blocks_json      TEXT    NOT NULL DEFAULT '[]',
			blocked_by_json  TEXT    NOT NULL DEFAULT '[]',
			metadata_json    TEXT    NOT NULL DEFAULT '{}',
			created_at_unix  INTEGER NOT NULL,
			updated_at_unix  INTEGER NOT NULL,
			UNIQUE(session_id, task_id),
			UNIQUE(session_id, position)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_tasks_session_position
			ON session_tasks(session_id, position ASC)`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteTeams(ctx context.Context, db *DB) error {
	return db.gormDB.WithContext(ctx).AutoMigrate(&GTeam{})
}

func migrateSQLiteAutomationJobs(ctx context.Context, db *DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS automation_jobs (
			id                    TEXT    PRIMARY KEY,
			owner_id              TEXT    NOT NULL DEFAULT '',
			name                  TEXT    NOT NULL,
			description           TEXT    NOT NULL DEFAULT '',
			trigger_type          TEXT    NOT NULL,
			trigger_cron          TEXT    NOT NULL DEFAULT '',
			trigger_interval_ns   INTEGER NOT NULL DEFAULT 0,
			trigger_run_at        INTEGER,
			agent_base_type       TEXT    NOT NULL DEFAULT '',
			agent_tools_json      TEXT    NOT NULL DEFAULT '[]',
			agent_skills_json     TEXT    NOT NULL DEFAULT '[]',
			agent_model           TEXT    NOT NULL DEFAULT '',
			agent_max_turns       INTEGER NOT NULL DEFAULT 0,
			agent_system_prompt   TEXT    NOT NULL DEFAULT '',
			task                  TEXT    NOT NULL,
			status                TEXT    NOT NULL DEFAULT 'active',
			last_run_at           INTEGER,
			next_run_at           INTEGER,
			last_run_status       TEXT    NOT NULL DEFAULT '',
			created_at            INTEGER NOT NULL,
			updated_at            INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_automation_jobs_owner
			ON automation_jobs(owner_id)`,
		`CREATE INDEX IF NOT EXISTS idx_automation_jobs_status
			ON automation_jobs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_automation_jobs_next_run
			ON automation_jobs(next_run_at) WHERE status = 'active'`,
		`CREATE TABLE IF NOT EXISTS automation_runs (
			id          TEXT    PRIMARY KEY,
			job_id      TEXT    NOT NULL REFERENCES automation_jobs(id) ON DELETE CASCADE,
			started_at  INTEGER NOT NULL,
			ended_at    INTEGER,
			status      TEXT    NOT NULL,
			output      TEXT    NOT NULL DEFAULT '',
			error       TEXT    NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_automation_runs_job_id
			ON automation_runs(job_id, started_at DESC)`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteAutomationAgentSlug(ctx context.Context, db *DB) error {
	return db.gormDB.WithContext(ctx).Exec(
		`ALTER TABLE automation_jobs ADD COLUMN agent_slug TEXT NOT NULL DEFAULT ''`,
	).Error
}

// migrateSQLiteAutomationJobLimits adds per-job execution guardrails - a
// wall-clock timeout (max_duration_ns) and a total-run-count cap (max_runs,
// tracked against run_count) - so a hung agent turn or a runaway schedule
// can't run forever. Idea from studying neul-labs/m9m (MIT), which has the
// same concept on its schedule config.
func migrateSQLiteAutomationJobLimits(ctx context.Context, db *DB) error {
	statements := []string{
		`ALTER TABLE automation_jobs ADD COLUMN max_duration_ns INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE automation_jobs ADD COLUMN max_runs INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE automation_jobs ADD COLUMN run_count INTEGER NOT NULL DEFAULT 0`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateSQLiteLongtermMemory(ctx context.Context, db *DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS memory_entities (
			id                TEXT    PRIMARY KEY,
			user_id           TEXT    NOT NULL,
			name              TEXT    NOT NULL,
			entity_type       TEXT    NOT NULL,
			observations_json TEXT    NOT NULL DEFAULT '[]',
			created_at_unix   INTEGER NOT NULL,
			updated_at_unix   INTEGER NOT NULL,
			UNIQUE(user_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS memory_relations (
			id              TEXT    PRIMARY KEY,
			user_id         TEXT    NOT NULL,
			from_name       TEXT    NOT NULL,
			to_name         TEXT    NOT NULL,
			relation_type   TEXT    NOT NULL,
			created_at_unix INTEGER NOT NULL,
			UNIQUE(user_id, from_name, to_name, relation_type)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_entities_user ON memory_entities(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_relations_user ON memory_relations(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_relations_endpoints ON memory_relations(user_id, from_name, to_name)`,
	}
	for _, stmt := range statements {
		if err := db.gormDB.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}
