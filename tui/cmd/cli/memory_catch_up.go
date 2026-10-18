package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// memoryCatchUpTimeout bounds the background pass that reads earlier sessions.
const memoryCatchUpTimeout = 10 * time.Minute

// catchUpMemories reads, in the background, the sessions an earlier run left without closing them (the terminal was closed
// or crashed, which is how most sessions end here), so that what they taught is not lost. It does nothing unless memory is on.
func catchUpMemories(client *sdk.Client, enabled bool) {
	if client == nil || !enabled {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), memoryCatchUpTimeout)
		defer cancel()
		read, err := client.ExtractPendingMemories(ctx, "", sdk.PendingMemoryOptions{})
		if err != nil {
			slog.Warn("memory catch-up: some sessions could not be read", "error", err)
		}
		if read > 0 {
			slog.Info("memory catch-up", "sessions_read", read)
		}
	}()
}
