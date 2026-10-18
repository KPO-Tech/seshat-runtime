package sdk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// memoryExtractedKey is the SessionMetadata.Additional key that records when the memory of a session was extracted (Unix
// seconds), so a session is read once.
const memoryExtractedKey = "memory_extracted_at"

// Defaults of PendingMemoryOptions.
const (
	defaultPendingIdle  = 30 * time.Minute
	defaultPendingAge   = 7 * 24 * time.Hour
	defaultPendingLimit = 10
)

// PendingMemoryOptions bounds ExtractPendingMemories.
type PendingMemoryOptions struct {
	// Idle is how long a session must have been untouched to be read. A session still in use is left alone. Default 30 minutes.
	Idle time.Duration
	// MaxAge leaves out sessions older than this, so turning extraction on does not read a whole history at once.
	// Default 7 days.
	MaxAge time.Duration
	// Limit is the most sessions read in one call, the most recent first. Default 10.
	Limit int
}

// ExtractPendingMemories reads the conversations of sessions that were never closed (a host that keeps its sessions
// open, or a crash) and stores what they taught, as Session.Close would have. It is the catch-up path of ExtractMemories:
// call it when the host starts and now and then. A session is read once: it is marked when its extraction succeeds. It
// returns the number of sessions read, and does nothing when ExtractMemories is off.
func (c *Client) ExtractPendingMemories(ctx context.Context, userID string, opts PendingMemoryOptions) (int, error) {
	if c == nil || c.memoryExtractor == nil {
		return 0, nil
	}
	if c.store == nil {
		return 0, fmt.Errorf("session persistence not enabled")
	}
	if opts.Idle <= 0 {
		opts.Idle = defaultPendingIdle
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = defaultPendingAge
	}
	if opts.Limit <= 0 {
		opts.Limit = defaultPendingLimit
	}
	if userID == "" {
		userID = "local"
	}
	infos, err := c.store.GetAllSessionsInfo()
	if err != nil {
		return 0, err
	}
	now := time.Now()
	var candidates []*SessionInfo
	for _, info := range infos {
		if info == nil || info.Subagent || info.TotalTurns == 0 {
			continue
		}
		updated := time.Unix(info.UpdatedAt, 0)
		if now.Sub(updated) < opts.Idle || now.Sub(updated) > opts.MaxAge {
			continue
		}
		candidates = append(candidates, info)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].UpdatedAt > candidates[j].UpdatedAt })

	read := 0
	var errs []error
	for _, info := range candidates {
		if read >= opts.Limit {
			break
		}
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		done, err := c.extractSessionMemory(ctx, userID, info.ID)
		if err != nil {
			slog.Warn("catch-up memory extraction failed", "session_id", info.ID, "error", err)
			errs = append(errs, err)
			continue
		}
		if done {
			read++
		}
	}
	return read, errors.Join(errs...)
}

// extractSessionMemory reads one session unless it was already read. It reports whether it ran an extraction.
func (c *Client) extractSessionMemory(ctx context.Context, userID string, id types.SessionID) (bool, error) {
	metadata, err := c.store.LoadSession(id)
	if err != nil {
		return false, err
	}
	if metadata == nil {
		return false, nil
	}
	if _, done := metadata.Additional[memoryExtractedKey]; done {
		return false, nil
	}
	messages, err := c.store.LoadCanonicalMessages(id)
	if err != nil {
		return false, err
	}
	if len(messages) == 0 {
		return false, c.markMemoryExtracted(id)
	}
	extractCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if err := c.memoryExtractor.Extract(extractCtx, userID, messages); err != nil {
		return false, err
	}
	return true, c.markMemoryExtracted(id)
}

// markMemoryExtracted records that the memory of a session was extracted. It reloads the metadata so it changes nothing else.
func (c *Client) markMemoryExtracted(id types.SessionID) error {
	metadata, err := c.store.LoadSession(id)
	if err != nil || metadata == nil {
		return err
	}
	if metadata.Additional == nil {
		metadata.Additional = make(map[string]any)
	}
	metadata.Additional[memoryExtractedKey] = time.Now().Unix()
	return c.store.SaveSession(id, metadata)
}

// markExtractedAfterClose is the engine hook that records a session whose extraction ran when it was closed.
func (c *Client) markExtractedAfterClose(id types.SessionID) {
	if c.store == nil {
		return
	}
	if err := c.markMemoryExtracted(id); err != nil {
		slog.Warn("could not mark session memory as extracted", "session_id", id, "error", err)
	}
}
