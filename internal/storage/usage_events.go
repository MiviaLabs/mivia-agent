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

// calibrationSeedRows bounds how far back the seed query looks. Recent rows
// only: a ratio is a property of the CURRENT tokenizer and prompt shape, and
// months-old observations for a since-changed system prompt or tool surface
// are not evidence about today's requests.
const calibrationSeedRows = 50

// calibrationSeedMinRatio and calibrationSeedMaxRatio mirror the bounds
// contextmgr.Calibration.Update enforces on live observations. They are
// duplicated rather than imported because internal/storage must not depend on
// internal/contextmgr; TestCalibrationSeedClampsToCalibrationBounds pins that
// they agree.
const (
	calibrationSeedMinRatio = 0.2
	calibrationSeedMaxRatio = 3.0
)

// CalibrationSeed returns the estimate-vs-actual correction ratio observed
// for a (provider, model) binding in this workspace, so a freshly started
// process can plan its FIRST request with the correction it already learned
// instead of starting blind at 1.0.
//
// Without this the ratio was written to token_usage_events on every turn and
// never read back: every process, every session and every resume began
// assuming the len(s)/4 estimate was exact. For payloads that are mostly code
// and JSON tool schemas that estimate runs ~1.7x low, so the first request
// sailed past the compaction trigger and the next one repaid the whole error
// at once - the sequence that destroyed a real session's context.
//
// The ratio is aggregated (sum of actual over sum of estimated) rather than
// averaged per row, so large requests - the ones that actually approach the
// budget and matter for compaction - carry proportionate weight. ok is false
// when the binding has no usable observation yet; the caller then keeps the
// uncorrected default.
func (s *SQLite) CalibrationSeed(ctx context.Context, workspaceID, provider, model string) (float64, bool, error) {
	var estimated, actual sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `SELECT SUM(estimated_tokens), SUM(input_tokens) FROM (
		SELECT estimated_tokens, input_tokens FROM token_usage_events
		WHERE workspace_id = ? AND provider = ? AND model = ?
		  AND kind = 'token_usage' AND estimated_tokens > 0 AND input_tokens > 0
		ORDER BY id DESC LIMIT ?)`,
		workspaceID, provider, model, calibrationSeedRows).Scan(&estimated, &actual)
	if err != nil {
		return 0, false, err
	}
	if !estimated.Valid || !actual.Valid || estimated.Float64 <= 0 || actual.Float64 <= 0 {
		return 0, false, nil
	}
	ratio := actual.Float64 / estimated.Float64
	if ratio < calibrationSeedMinRatio {
		ratio = calibrationSeedMinRatio
	}
	if ratio > calibrationSeedMaxRatio {
		ratio = calibrationSeedMaxRatio
	}
	return ratio, true, nil
}
