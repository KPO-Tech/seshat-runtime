package sdk

import (
	"context"
	"errors"

	"github.com/KPO-Tech/seshat-runtime/internal/memory/longterm"
)

// MemorySnapshot describes a copy of a user's long-term memory taken before it was rewritten.
type MemorySnapshot = longterm.SnapshotInfo

// ErrMemorySnapshotNotFound is returned by RestoreMemorySnapshot when the user has no copy with that id.
var ErrMemorySnapshotNotFound = longterm.ErrSnapshotNotFound

// errNoMemorySnapshots is returned when the store cannot copy memory.
var errNoMemorySnapshots = errors.New("the long-term memory store cannot take snapshots")

// ListMemorySnapshots returns the copies of a user's memory that consolidations (and restores) took, newest first. At
// most ten are kept per user.
func ListMemorySnapshots(ctx context.Context, store LongTermMemory, userID string) ([]MemorySnapshot, error) {
	snapshotter, ok := store.(longterm.Snapshotter)
	if !ok {
		return nil, errNoMemorySnapshots
	}
	return snapshotter.ListSnapshots(ctx, userID)
}

// RestoreMemorySnapshot puts a user's memory back as it was in the copy, which undoes a consolidation. The memory as it
// is now is copied first, so a restore can itself be undone.
func RestoreMemorySnapshot(ctx context.Context, store LongTermMemory, userID, snapshotID string) error {
	snapshotter, ok := store.(longterm.Snapshotter)
	if !ok {
		return errNoMemorySnapshots
	}
	return snapshotter.RestoreSnapshot(ctx, userID, snapshotID)
}
