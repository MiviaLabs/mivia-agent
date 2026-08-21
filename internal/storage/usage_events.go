package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/usage"
)

// RecordUsageEvent durably records one token/cache/compaction usage
// measurement. One INSERT, its own transaction - mirrors every other durable
// write in this package (writeMu-serialized, retried on a transient busy
// lock, since a session's own store can be written by more than one mivia
// process sharing a workspace, same as every other table here).
func (s *SQLite) RecordUsageEvent(ctx context.Context, workspaceID string, record usage.UsageRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return retrySQLiteBusy(ctx, func() error {
		return s.inTx(ctx, func(tx *sql.Tx) error {
			var summarized any
			if record.Summarized != nil {
				if *record.Summarized {
					summarized = 1
				} else {
					summarized = 0
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO token_usage_events(
				workspace_id, session_id, turn_id, kind, provider, model,
				input_tokens, output_tokens, estimated_tokens, calibration_ratio,
				cached_input_tokens, cache_write_tokens,
				before_tokens, after_tokens, elided_messages, elided_bytes,
				summarized, reason, agent_task, agent_name, agent_depth, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				workspaceID, record.SessionID, record.TurnID, record.Kind, nullableText(record.Provider), nullableText(record.Model),
				record.InputTokens, record.OutputTokens, record.EstimatedTokens, record.CalibrationRatio,
				record.CachedInputTokens, record.CacheWriteTokens,
				record.BeforeTokens, record.AfterTokens, record.ElidedMessages, record.ElidedBytes,
				summarized, nullableText(record.Reason), nullableText(record.AgentTask), nullableText(record.AgentName), record.AgentDepth,
				time.Now().UTC().Unix(),
			)
			return err
		})
	})
}

// usageWriter adapts a *SQLite store, scoped to one workspace, to
// usage.UsageWriter. The workspace id is fixed at construction (a
// session's own workspace never changes mid-session), so the interface
// method itself only needs the record.
type usageWriter struct {
	store       *SQLite
	workspaceID string
}

// NewUsageWriter returns a usage.UsageWriter that records into store,
// scoped to workspaceID.
func NewUsageWriter(store *SQLite, workspaceID string) usage.UsageWriter {
	return usageWriter{store: store, workspaceID: workspaceID}
}

// Record dispatches the write off the caller's goroutine and returns
// immediately (nil, always - a fire-and-forget write reports no error
// synchronously by construction). This is deliberate, not merely an
// optimization: EmitCompaction (internal/agent) calls this while
// internal/chat still holds contextPublishMu, a session-wide lock also
// taken by /compact, session reset, and model switch - a synchronous write
// sharing writeMu with a contended checkpoint commit could extend that
// lock's hold time by several seconds under load. The Add happens here,
// synchronously, in the caller's own goroutine, BEFORE the go statement -
// registering inside the spawned goroutine would race Close's Wait if Close
// ran before the new goroutine got scheduled. w.store.Close waits on this
// same WaitGroup, so a one-shot process (mivia compact, a single
// non-interactive chat turn) or a test's TempDir cleanup cannot tear down
// the store while this write is still in flight or hasn't started running.
func (w usageWriter) Record(ctx context.Context, record usage.UsageRecord) error {
	w.store.usageWriteWG.Add(1)
	go func() {
		defer w.store.usageWriteWG.Done()
		// Not ctx: the caller may already have returned (or its ctx may be
		// canceled) by the time this goroutine actually runs, and a
		// best-effort write that outlives its triggering call should not be
		// aborted just because that call already returned.
		_ = w.store.RecordUsageEvent(context.Background(), w.workspaceID, record)
	}()
	return nil
}
