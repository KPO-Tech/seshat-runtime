package longterm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Consolidatable is a Store that can be rewritten as a whole, which consolidation needs: the plain Store only adds.
// SQLiteStore implements it; a host with its own Store opts in by implementing it too.
type Consolidatable interface {
	Store

	// ListEntities returns every entity of the user, ordered by name.
	ListEntities(ctx context.Context, userID string) ([]Entity, error)

	// ReplaceEntities rewrites the entities of snapshot (what ListEntities returned) as entities, atomically: a
	// snapshot entity not in entities is removed, one in entities is created or rewritten. It returns
	// ErrChangedDuringConsolidation, changing nothing, if any snapshot entity was modified or removed since the
	// snapshot; entities created since are left alone. renames maps the name of an entity merged into another to the
	// name it now has, so that relations follow it; a relation left without both endpoints, or pointing to itself, is
	// removed.
	ReplaceEntities(ctx context.Context, userID string, snapshot []Entity, entities []EntityInput, renames map[string]string) error
}

// ErrChangedDuringConsolidation is returned when memory was written while a consolidation was being computed. The
// consolidation is dropped, not retried: the next one starts from the new state.
var ErrChangedDuringConsolidation = errors.New("memory changed while it was being consolidated")

var _ Consolidatable = (*SQLiteStore)(nil)

// ListEntities returns every entity of the user, ordered by name.
func (s *SQLiteStore) ListEntities(ctx context.Context, userID string) ([]Entity, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, name, entity_type, observations_json, created_at_unix, updated_at_unix
		 FROM memory_entities WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list entities: %w", err)
	}
	return scanEntities(rows)
}

// ReplaceEntities implements Consolidatable.
func (s *SQLiteStore) ReplaceEntities(ctx context.Context, userID string, snapshot []Entity, entities []EntityInput, renames map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := verifySnapshotTx(ctx, tx, userID, snapshot); err != nil {
		return err
	}
	if err := replaceEntitiesTx(ctx, tx, userID, snapshot, entities); err != nil {
		return err
	}
	if err := followRenamesTx(ctx, tx, userID, renames); err != nil {
		return err
	}
	return tx.Commit()
}

// verifySnapshotTx fails if any entity of the snapshot was changed or removed since it was read. The timestamp has
// a resolution of a second, so the observations are compared too.
func verifySnapshotTx(ctx context.Context, tx *sql.Tx, userID string, snapshot []Entity) error {
	for _, e := range snapshot {
		var updated int64
		var obsJSON string
		err := tx.QueryRowContext(ctx,
			`SELECT updated_at_unix, observations_json FROM memory_entities WHERE user_id = ? AND name = ?`, userID, e.Name).
			Scan(&updated, &obsJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrChangedDuringConsolidation
		}
		if err != nil {
			return fmt.Errorf("check entity %q: %w", e.Name, err)
		}
		var current []string
		_ = json.Unmarshal([]byte(obsJSON), &current)
		if updated != e.UpdatedAt.Unix() || !sameStrings(current, e.Observations) {
			return ErrChangedDuringConsolidation
		}
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func replaceEntitiesTx(ctx context.Context, tx *sql.Tx, userID string, snapshot []Entity, entities []EntityInput) error {
	now := time.Now().UTC().Unix()
	keep := make(map[string]bool, len(entities))
	for _, e := range entities {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		keep[name] = true
		obs := e.Observations
		if obs == nil {
			obs = []string{}
		}
		raw, _ := json.Marshal(obs)
		// Existing entities keep their id and creation time; new ones are inserted.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_entities (id, user_id, name, entity_type, observations_json, created_at_unix, updated_at_unix)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(user_id, name) DO UPDATE SET
			   entity_type = excluded.entity_type, observations_json = excluded.observations_json, updated_at_unix = excluded.updated_at_unix`,
			uuid.NewString(), userID, name, strings.TrimSpace(e.EntityType), string(raw), now, now); err != nil {
			return fmt.Errorf("write entity %q: %w", name, err)
		}
	}

	// Only what the snapshot held can be removed: an entity written since is not ours to delete.
	for _, e := range snapshot {
		if keep[e.Name] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entities WHERE user_id = ? AND name = ?`, userID, e.Name); err != nil {
			return fmt.Errorf("remove entity %q: %w", e.Name, err)
		}
	}
	return nil
}

// followRenamesTx points relations at the merged entity, then removes what can no longer stand.
func followRenamesTx(ctx context.Context, tx *sql.Tx, userID string, renames map[string]string) error {
	for from, to := range renames {
		if from == to {
			continue
		}
		// OR IGNORE: the merged entity may already have the same relation; the duplicate is removed below.
		for _, column := range []string{"from_name", "to_name"} {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`UPDATE OR IGNORE memory_relations SET %s = ? WHERE user_id = ? AND %s = ?`, column, column),
				to, userID, from); err != nil {
				return fmt.Errorf("follow rename %q: %w", from, err)
			}
		}
	}
	for _, stmt := range []string{
		`DELETE FROM memory_relations WHERE user_id = ? AND from_name = to_name`,
		`DELETE FROM memory_relations WHERE user_id = ? AND from_name NOT IN (SELECT name FROM memory_entities WHERE user_id = ?)`,
		`DELETE FROM memory_relations WHERE user_id = ? AND to_name NOT IN (SELECT name FROM memory_entities WHERE user_id = ?)`,
	} {
		args := []any{userID}
		if strings.Contains(stmt, "NOT IN") {
			args = append(args, userID)
		}
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			return fmt.Errorf("clean relations: %w", err)
		}
	}
	return nil
}
