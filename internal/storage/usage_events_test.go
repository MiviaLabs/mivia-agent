package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/usage"
)

func TestRecordUsageEventRoundTripsTokenUsage(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	err = store.RecordUsageEvent(ctx, "ws-1", usage.UsageRecord{
		Kind: "token_usage", SessionID: "sess-1", TurnID: "turn:1",
		Provider: "deepseek", Model: "deepseek-v4-flash",
		InputTokens: 100, OutputTokens: 40, EstimatedTokens: 95, CalibrationRatio: 1.05,
	})
	if err != nil {
		t.Fatalf("RecordUsageEvent: %v", err)
	}

	var (
		workspaceID, sessionID, turnID, kind, provider, model string
		inputTokens, outputTokens                             int
	)
	row := store.db.QueryRow(`SELECT workspace_id, session_id, turn_id, kind, provider, model, input_tokens, output_tokens
		FROM token_usage_events WHERE session_id = 'sess-1'`)
	if err := row.Scan(&workspaceID, &sessionID, &turnID, &kind, &provider, &model, &inputTokens, &outputTokens); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if workspaceID != "ws-1" || sessionID != "sess-1" || turnID != "turn:1" || kind != "token_usage" {
		t.Fatalf("identity columns = %q %q %q %q", workspaceID, sessionID, turnID, kind)
	}
	if provider != "deepseek" || model != "deepseek-v4-flash" {
		t.Fatalf("provider/model = %q %q", provider, model)
	}
	if inputTokens != 100 || outputTokens != 40 {
		t.Fatalf("token counts = %d %d", inputTokens, outputTokens)
	}
}

func TestRecordUsageEventRoundTripsCompactionWithSummarizedFlag(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	summarized := true
	err = store.RecordUsageEvent(ctx, "ws-1", usage.UsageRecord{
		Kind: "compaction", SessionID: "sess-1", TurnID: "turn:2",
		BeforeTokens: 1000, AfterTokens: 400, ElidedMessages: 3, ElidedBytes: 2048,
		Summarized: &summarized, Reason: "",
	})
	if err != nil {
		t.Fatalf("RecordUsageEvent: %v", err)
	}

	var beforeTokens, afterTokens, summarizedCol int
	row := store.db.QueryRow(`SELECT before_tokens, after_tokens, summarized FROM token_usage_events WHERE turn_id = 'turn:2'`)
	if err := row.Scan(&beforeTokens, &afterTokens, &summarizedCol); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if beforeTokens != 1000 || afterTokens != 400 || summarizedCol != 1 {
		t.Fatalf("compaction columns = %d %d %d", beforeTokens, afterTokens, summarizedCol)
	}
}

// TestRecordUsageEventNilSummarizedStaysNull confirms a nil Summarized (the
// zero value most non-compaction records use) stores as SQL NULL, not a
// false 0 that would misreport an unset field as "not summarized".
func TestRecordUsageEventNilSummarizedStaysNull(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.RecordUsageEvent(ctx, "ws-1", usage.UsageRecord{
		Kind: "cache_usage", SessionID: "sess-1", TurnID: "turn:3",
		InputTokens: 50, CachedInputTokens: 30,
	}); err != nil {
		t.Fatalf("RecordUsageEvent: %v", err)
	}

	var summarized any
	row := store.db.QueryRow(`SELECT summarized FROM token_usage_events WHERE turn_id = 'turn:3'`)
	if err := row.Scan(&summarized); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if summarized != nil {
		t.Fatalf("summarized = %v, want NULL for a non-compaction record", summarized)
	}
}

// TestNewUsageWriterDelegatesToRecordUsageEvent waits on the store's own
// usageWriteWG (the exact mechanism Close uses) rather than asserting
// immediately after Record returns: Record dispatches the actual insert off
// its caller's goroutine and returns before it necessarily lands.
func TestNewUsageWriterDelegatesToRecordUsageEvent(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	writer := NewUsageWriter(store, "ws-writer")
	if err := writer.Record(context.Background(), usage.UsageRecord{
		Kind: "token_usage", SessionID: "sess-writer", TurnID: "turn:1", InputTokens: 10,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	store.usageWriteWG.Wait()
	var workspaceID string
	row := store.db.QueryRow(`SELECT workspace_id FROM token_usage_events WHERE session_id = 'sess-writer'`)
	if err := row.Scan(&workspaceID); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if workspaceID != "ws-writer" {
		t.Fatalf("workspace_id = %q, want ws-writer (from NewUsageWriter's constructor arg, not the record)", workspaceID)
	}
}

// TestUsageWriterRecordDoesNotBlockOnAContendedWrite pins the reviewed fix:
// EmitCompaction calls Record while internal/chat still holds
// contextPublishMu, so Record must return immediately even when the actual
// insert is stuck behind another writer holding writeMu - it dispatches the
// insert onto its own goroutine rather than running it inline.
func TestUsageWriterRecordDoesNotBlockOnAContendedWrite(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Simulate a contended write already in progress - the same lock
	// RecordUsageEvent itself takes.
	store.writeMu.Lock()
	writer := NewUsageWriter(store, "ws-1")
	start := time.Now()
	if err := writer.Record(context.Background(), usage.UsageRecord{
		Kind: "token_usage", SessionID: "sess-contended", TurnID: "turn:1", InputTokens: 5,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Record blocked for %v while writeMu was held by another writer", elapsed)
	}
	store.writeMu.Unlock()

	store.usageWriteWG.Wait()
	var kind string
	row := store.db.QueryRow(`SELECT kind FROM token_usage_events WHERE session_id = 'sess-contended'`)
	if err := row.Scan(&kind); err != nil {
		t.Fatalf("read back after the contended write released: %v", err)
	}
	if kind != "token_usage" {
		t.Fatalf("kind = %q, want token_usage", kind)
	}
}

// TestSQLiteCloseWaitsForInFlightUsageWrites pins the other half of the same
// fix: a one-shot process (mivia compact, a single non-interactive chat
// turn) or a test's t.TempDir() cleanup must not tear the store down while a
// dispatched usage write is still in flight or hasn't started running -
// Close blocks until it lands.
func TestSQLiteCloseWaitsForInFlightUsageWrites(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}

	store.writeMu.Lock()
	writer := NewUsageWriter(store, "ws-1")
	if err := writer.Record(context.Background(), usage.UsageRecord{
		Kind: "token_usage", SessionID: "sess-close", TurnID: "turn:1", InputTokens: 5,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	closeErr := make(chan error, 1)
	go func() { closeErr <- store.Close() }()
	select {
	case <-closeErr:
		t.Fatal("Close returned before the in-flight usage write's lock was released")
	case <-time.After(200 * time.Millisecond):
	}

	store.writeMu.Unlock()
	select {
	case err := <-closeErr:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close never returned after the contended write unblocked")
	}
}

// seedUsageRows records n token_usage rows whose input/estimated ratio is
// exactly ratio, so a seed query over them has an unambiguous answer.
func seedUsageRows(t *testing.T, store *SQLite, workspace, provider, model string, n int, estimated, input int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := store.RecordUsageEvent(context.Background(), workspace, usage.UsageRecord{
			Kind: "token_usage", SessionID: "sess", TurnID: "turn",
			Provider: provider, Model: model,
			InputTokens: input, EstimatedTokens: estimated,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestCalibrationSeedRecoversTheObservedRatio pins the fix for the deepest
// defect behind a real context-destroying compaction: the estimate/actual
// correction ratio was written to token_usage_events on every single turn and
// never read back, so every process started blind at 1.00 while the estimator
// undercounts real payloads by ~1.7x. The first request of every session was
// therefore planned ~42% low - it sailed past the compaction trigger, and the
// next request repaid the entire accumulated error in one violent compaction.
func TestCalibrationSeedRecoversTheObservedRatio(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedUsageRows(t, store, "ws-1", "llmgateway", "claude-sonnet-5", 5, 1000, 1730)

	ratio, ok, err := store.CalibrationSeed(context.Background(), "ws-1", "llmgateway", "claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("CalibrationSeed found no rows, want the seeded observations")
	}
	if ratio < 1.72 || ratio > 1.74 {
		t.Fatalf("ratio = %v, want ~1.73", ratio)
	}
}

// TestCalibrationSeedScopesToProviderAndModel: a ratio learned for one
// model's tokenizer is meaningless for another, so the query must never
// cross-contaminate.
func TestCalibrationSeedScopesToProviderAndModel(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedUsageRows(t, store, "ws-1", "llmgateway", "claude-sonnet-5", 5, 1000, 1730)
	seedUsageRows(t, store, "ws-1", "deepseek", "deepseek-v4-flash", 5, 1000, 1000)

	ratio, ok, err := store.CalibrationSeed(context.Background(), "ws-1", "deepseek", "deepseek-v4-flash")
	if err != nil || !ok {
		t.Fatalf("CalibrationSeed: ratio=%v ok=%v err=%v", ratio, ok, err)
	}
	if ratio < 0.99 || ratio > 1.01 {
		t.Fatalf("ratio = %v, want ~1.0 - the other model's 1.73 leaked in", ratio)
	}
	if _, ok, _ := store.CalibrationSeed(context.Background(), "ws-1", "llmgateway", "absent-model"); ok {
		t.Fatal("an unseen model reported a seed")
	}
	if _, ok, _ := store.CalibrationSeed(context.Background(), "other-ws", "llmgateway", "claude-sonnet-5"); ok {
		t.Fatal("a different workspace's rows leaked into the seed")
	}
}

// TestCalibrationSeedClampsToCalibrationBounds: a pathological row must not
// seed a ratio outside the range Calibration.Update itself enforces, or the
// seed could zero out or explode every first-request estimate.
func TestCalibrationSeedClampsToCalibrationBounds(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedUsageRows(t, store, "ws-1", "p", "huge", 3, 1, 100000)
	seedUsageRows(t, store, "ws-1", "p", "tiny", 3, 100000, 1)

	high, ok, err := store.CalibrationSeed(context.Background(), "ws-1", "p", "huge")
	if err != nil || !ok {
		t.Fatalf("huge: ok=%v err=%v", ok, err)
	}
	if high > 3.0 {
		t.Fatalf("ratio = %v, want clamped to at most 3.0", high)
	}
	low, ok, err := store.CalibrationSeed(context.Background(), "ws-1", "p", "tiny")
	if err != nil || !ok {
		t.Fatalf("tiny: ok=%v err=%v", ok, err)
	}
	if low < 0.2 {
		t.Fatalf("ratio = %v, want clamped to at least 0.2", low)
	}
}

// TestCalibrationSeedIgnoresRowsWithoutAnEstimate: compaction and cache_usage
// rows carry no estimate, and a zero estimate would divide by zero.
func TestCalibrationSeedIgnoresRowsWithoutAnEstimate(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	err = store.RecordUsageEvent(context.Background(), "ws-1", usage.UsageRecord{
		Kind: "compaction", SessionID: "s", TurnID: "t",
		Provider: "p", Model: "m", BeforeTokens: 900, AfterTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.CalibrationSeed(context.Background(), "ws-1", "p", "m"); ok {
		t.Fatal("a row with no estimate produced a seed")
	}
}
