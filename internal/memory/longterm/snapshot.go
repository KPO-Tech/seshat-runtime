package longterm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Consolidation rewrites a user's memory, and a model decides what to keep. The refusal thresholds stop the worst
// results, but a plausible one that drops something that mattered would be gone for good. So memory is copied before it
// is rewritten, and the copy can be put back.

// maxSnapshotsPerUser is how many copies are kept per user; older ones are removed.
const maxSnapshotsPerUser = 10

// SnapshotInfo describes a copy of a user's memory.
type SnapshotInfo struct {
	ID           string    `json:"id"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"created_at"`
	Entities     int       `json:"entities"`
	Observations int       `json:"observations"`
}

// Snapshotter is a store that can copy a user's memory and put the copy back. SQLiteStore implements it; a host with its
// own Consolidatable store implements it too to allow consolidation, which refuses to run without a way back.
type Snapshotter interface {
	// SaveSnapshot copies the user's entities and relations and returns the copy's description.
	SaveSnapshot(ctx context.Context, userID, reason string) (SnapshotInfo, error)

	// ListSnapshots returns the user's copies, newest first.
	ListSnapshots(ctx context.Context, userID string) ([]SnapshotInfo, error)

	// RestoreSnapshot replaces the user's memory with the copy. It first copies the memory as it is, so that a restore
	// can be undone too.
	RestoreSnapshot(ctx context.Context, userID, snapshotID string) error
}

// ErrSnapshotNotFound is returned when a snapshot does not exist for the user.
var ErrSnapshotNotFound = errors.New("memory snapshot not found")

// ErrNoSnapshotSupport is returned by a consolidation whose store cannot copy memory first: it does not run without a
// way back.
var ErrNoSnapshotSupport = errors.New("the memory store cannot take snapshots, so memory is not consolidated")

var _ Snapshotter = (*SQLiteStore)(nil)

// SaveSnapshot implements Snapshotter.
func (s *SQLiteStore) SaveSnapshot(ctx context.Context, userID, reason string) (SnapshotInfo, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	info, err := saveSnapshotTx(ctx, tx, userID, reason)
	if err != nil {
		return SnapshotInfo{}, err
	}
	return info, tx.Commit()
}

func saveSnapshotTx(ctx context.Context, tx *sql.Tx, userID, reason string) (SnapshotInfo, error) {
	entities, err := entitiesTx(ctx, tx, userID)
	if err != nil {
		return SnapshotInfo{}, err
	}
	relations, err := relationsTx(ctx, tx, userID)
	if err != nil {
		return SnapshotInfo{}, err
	}
	entitiesJSON, err := json.Marshal(entities)
	if err != nil {
		return SnapshotInfo{}, err
	}
	relationsJSON, err := json.Marshal(relations)
	if err != nil {
		return SnapshotInfo{}, err
	}
	info := SnapshotInfo{ID: uuid.NewString(), Reason: reason, CreatedAt: time.Now().UTC(), Entities: len(entities)}
	for _, e := range entities {
		info.Observations += len(e.Observations)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_snapshots (id, user_id, created_at_unix, reason, entities_json, relations_json) VALUES (?, ?, ?, ?, ?, ?)`,
		info.ID, userID, info.CreatedAt.UnixNano(), reason, string(entitiesJSON), string(relationsJSON)); err != nil {
		return SnapshotInfo{}, fmt.Errorf("save snapshot: %w", err)
	}
	// Keep the newest maxSnapshotsPerUser.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM memory_snapshots WHERE user_id = ? AND id NOT IN (
			SELECT id FROM memory_snapshots WHERE user_id = ? ORDER BY created_at_unix DESC LIMIT ?)`,
		userID, userID, maxSnapshotsPerUser); err != nil {
		return SnapshotInfo{}, fmt.Errorf("prune snapshots: %w", err)
	}
	return info, nil
}

func entitiesTx(ctx context.Context, tx *sql.Tx, userID string) ([]Entity, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, user_id, name, entity_type, observations_json, created_at_unix, updated_at_unix
		 FROM memory_entities WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, fmt.Errorf("read entities: %w", err)
	}
	return scanEntities(rows)
}

func relationsTx(ctx context.Context, tx *sql.Tx, userID string) ([]Relation, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, user_id, from_name, to_name, relation_type, created_at_unix FROM memory_relations WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("read relations: %w", err)
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		var r Relation
		var created int64
		if err := rows.Scan(&r.ID, &r.UserID, &r.From, &r.To, &r.RelationType, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSnapshots implements Snapshotter.
func (s *SQLiteStore) ListSnapshots(ctx context.Context, userID string) ([]SnapshotInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at_unix, reason, entities_json FROM memory_snapshots WHERE user_id = ? ORDER BY created_at_unix DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()
	var out []SnapshotInfo
	for rows.Next() {
		var info SnapshotInfo
		var created int64
		var entitiesJSON string
		if err := rows.Scan(&info.ID, &created, &info.Reason, &entitiesJSON); err != nil {
			return nil, err
		}
		info.CreatedAt = time.Unix(0, created).UTC()
		var entities []Entity
		_ = json.Unmarshal([]byte(entitiesJSON), &entities)
		info.Entities = len(entities)
		for _, e := range entities {
			info.Observations += len(e.Observations)
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// RestoreSnapshot implements Snapshotter.
func (s *SQLiteStore) RestoreSnapshot(ctx context.Context, userID, snapshotID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var entitiesJSON, relationsJSON string
	err = tx.QueryRowContext(ctx,
		`SELECT entities_json, relations_json FROM memory_snapshots WHERE user_id = ? AND id = ?`, userID, snapshotID).
		Scan(&entitiesJSON, &relationsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSnapshotNotFound
	}
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}
	var entities []Entity
	var relations []Relation
	if err := json.Unmarshal([]byte(entitiesJSON), &entities); err != nil {
		return fmt.Errorf("decode snapshot entities: %w", err)
	}
	if err := json.Unmarshal([]byte(relationsJSON), &relations); err != nil {
		return fmt.Errorf("decode snapshot relations: %w", err)
	}

	if _, err := saveSnapshotTx(ctx, tx, userID, "before restoring "+snapshotID); err != nil {
		return err
	}
	for _, stmt := range []string{`DELETE FROM memory_relations WHERE user_id = ?`, `DELETE FROM memory_entities WHERE user_id = ?`} {
		if _, err := tx.ExecContext(ctx, stmt, userID); err != nil {
			return fmt.Errorf("clear memory: %w", err)
		}
	}
	for _, e := range entities {
		raw, _ := json.Marshal(e.Observations)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_entities (id, user_id, name, entity_type, observations_json, created_at_unix, updated_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			e.ID, userID, e.Name, e.EntityType, string(raw), e.CreatedAt.Unix(), e.UpdatedAt.Unix()); err != nil {
			return fmt.Errorf("restore entity %q: %w", e.Name, err)
		}
	}
	for _, r := range relations {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_relations (id, user_id, from_name, to_name, relation_type, created_at_unix) VALUES (?, ?, ?, ?, ?, ?)`,
			r.ID, userID, r.From, r.To, r.RelationType, r.CreatedAt.Unix()); err != nil {
			return fmt.Errorf("restore relation: %w", err)
		}
	}
	return tx.Commit()
}
