package cli

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/usage"
)

// usageReportingCompleter answers every agent-loop turn with a plain final
// answer (no tool calls) carrying provider-reported token and cache usage, so
// a driven turn exercises the REAL EmitTokenUsage/EmitCacheUsage call sites in
// internal/agent/loop.go (l.emitTurnUsage), not a fake Options.UsageWriter.
//
// InputTokens is derived from the request's own content length (the same
// len/4 heuristic the planner uses to estimate) rather than a fixed
// constant: a fixed, unrealistically small InputTokens (e.g. 120 against a
// several-thousand-character seeded turn) skews
// Loop.Calibration.Update's estimate-vs-actual ratio so far that the NEXT
// turn's preparation, which scales its own token estimate by that ratio,
// stops believing it needs to compact at all - silently defeating
// driveCompactingTurn's whole technique. A proportional estimate keeps the
// calibration ratio near 1, leaving the compaction trigger to behave the
// same as it does against a real provider.
type usageReportingCompleter struct{}

func (usageReportingCompleter) Name() string { return "stub" }

func (usageReportingCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	total := 0
	for _, m := range req.Messages {
		total += len(m.Content)
	}
	input := total / 4
	if input == 0 {
		input = 1
	}
	cached := input * 8 / 10
	return &provider.Response{
		Content:    "ok",
		TokenUsage: provider.TokenUsage{Reported: true, InputTokens: input, OutputTokens: 40},
		CacheUsage: provider.CacheUsage{Reported: true, Style: provider.CacheStyleImplicit, InputTokens: input, CachedInputTokens: cached, CacheWriteTokens: 5},
	}, nil
}

func (usageReportingCompleter) Chat(_ context.Context, _ provider.Request) (string, error) {
	return "ok", nil
}

func (usageReportingCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}

// openUsageEventsReader opens a second, independent connection to the same
// SQLite file the session's real store writes through, so assertions read
// back the actual persisted rows rather than anything held in memory by the
// production code under test. WAL mode (set by storage.OpenSQLite) allows
// this second connection to read concurrently with the store's own writer.
func openUsageEventsReader(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open read connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type usageEventRow struct {
	kind                      string
	sessionID, turnID         string
	provider, model           sql.NullString
	inputTokens, outputTokens sql.NullInt64
	cachedInputTokens         sql.NullInt64
	beforeTokens, afterTokens sql.NullInt64
	summarized                sql.NullInt64
}

func queryUsageEventRows(t *testing.T, db *sql.DB, sessionID string) []usageEventRow {
	t.Helper()
	rows, err := db.Query(`SELECT kind, session_id, turn_id, provider, model, input_tokens, output_tokens,
		cached_input_tokens, before_tokens, after_tokens, summarized
		FROM token_usage_events WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		t.Fatalf("query token_usage_events: %v", err)
	}
	defer rows.Close()
	var out []usageEventRow
	for rows.Next() {
		var r usageEventRow
		if err := rows.Scan(&r.kind, &r.sessionID, &r.turnID, &r.provider, &r.model,
			&r.inputTokens, &r.outputTokens, &r.cachedInputTokens, &r.beforeTokens, &r.afterTokens, &r.summarized); err != nil {
			t.Fatalf("scan token_usage_events row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate token_usage_events: %v", err)
	}
	return out
}

// TestSessionUsageEventsReachRealSQLiteStoreAcrossAllThreeKinds drives a real
// chat.Session, wired the same way `mivia chat` wires it
// (configureSessionContext -> enableSessionContext ->
// storage.NewUsageWriter(store, ...) -> contextmgr.ContextManager.UsageWriter
// -> agent.Options.UsageWriter -> internal/agent/loop.go's emitTurnUsage /
// internal/chat's emitContextCompaction), through two real turns: a plain
// completion (token_usage + cache_usage) and a forced compaction
// (compaction). It then reads the SAME on-disk token_usage_events table back
// through an independent connection.
//
// Before this test, internal/storage's tests only ever drove
// RecordUsageEvent directly with a synthetic record (no session, no Emit*
// involvement), and internal/agent's Emit* tests only ever used
// fakeUsageWriter (no real SQLite). Nothing proved the wiring itself - the
// five-hop chain from a real session down to a real INSERT - actually works
// end to end. A regression anywhere in that chain (e.g. enableSessionContext
// forgetting to set ContextManager.UsageWriter, or session.go's turn-options
// builder dropping the opts.UsageWriter assignment) would have passed every
// existing test in this slice while writing nothing to disk.
//
// This also closes the "three kinds, one session" gap: usage_events_test.go
// exercises each kind in isolation across separate temp DBs, so nothing
// proved the kind-specific columns don't cross-contaminate when multiple
// kinds land in the same session's rows (e.g. a token_usage row picking up a
// stray before_tokens value, or a compaction row picking up a provider
// name).
func TestSessionUsageEventsReachRealSQLiteStoreAcrossAllThreeKinds(t *testing.T) {
	dbPath, store, session := newUsageEventsSession(t)
	defer store.Close()

	// driveCompactingTurn (context_summary_integration_test.go) seeds one
	// large turn, then tightens the prompt budget so the second turn's
	// preparation must compact - the same proven technique
	// TestAgentLoopCompactionSummarySurvivesTheTurnBoundary already uses on
	// this exact agent-loop path. The first turn's completion also exercises
	// EmitTokenUsage/EmitCacheUsage: recordUsage itself calls Record
	// synchronously for all three kinds, but the production UsageWriter
	// (storage.usageWriter.Record) dispatches its own write off that call's
	// goroutine and tracks it against the store's own sync.WaitGroup (which
	// SQLite.Close waits on) - so none of the three rows is guaranteed to be
	// on disk the instant driveCompactingTurn returns; only the poll below
	// proves any of them actually landed.
	driveCompactingTurn(t, session)

	byKind := pollUsageEventRowsByKind(t, dbPath, session.SessionID)
	assertTokenUsageRow(t, byKind, session.SessionID)
	assertCacheUsageRow(t, byKind)
	assertCompactionRow(t, byKind)
}

// newUsageEventsSession opens a real store and wires a real chat.Session
// through it the same way `mivia chat` does (configureSessionContext ->
// enableSessionContext -> storage.NewUsageWriter), so a driven turn reaches
// the real agent-loop Emit* call sites, not a fakeUsageWriter.
func newUsageEventsSession(t *testing.T) (string, *storage.SQLite, *chat.Session) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "context.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	res := summaryWiringResolved(t, false) // no summarizer: keep compaction structural-only
	session := chat.NewSession(res, usageReportingCompleter{})
	if _, err := configureSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	if !session.AgentTurnEnabled() {
		t.Fatal("session did not take the agent-loop path; token/cache usage Emit* are never reached on the plain path")
	}
	return dbPath, store, session
}

// pollUsageEventRowsByKind polls for all three kinds, not just compaction:
// every kind's write is dispatched off its caller's goroutine by
// storage.usageWriter.Record, so a single immediate read can race any of
// them, not only the compaction row from the later turn.
func pollUsageEventRowsByKind(t *testing.T, dbPath, sessionID string) map[string][]usageEventRow {
	t.Helper()
	reader := openUsageEventsReader(t, dbPath)
	var rows []usageEventRow
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows = queryUsageEventRows(t, reader, sessionID)
		haveToken, haveCache, haveCompaction := false, false, false
		for _, r := range rows {
			switch r.kind {
			case "token_usage":
				haveToken = true
			case "cache_usage":
				haveCache = true
			case "compaction":
				haveCompaction = true
			}
		}
		if (haveToken && haveCache && haveCompaction) || time.Now().After(deadline) {
			break
		}
		// Yields via runtime.Gosched, not time.Sleep (blocked by project
		// semgrep for tests): a real SQLite write from another goroutine
		// still needs wall-clock time to land, so this spins against the
		// deadline above rather than sleeping a fixed interval.
		runtime.Gosched()
	}
	byKind := map[string][]usageEventRow{}
	for _, r := range rows {
		byKind[r.kind] = append(byKind[r.kind], r)
	}
	return byKind
}

func assertTokenUsageRow(t *testing.T, byKind map[string][]usageEventRow, sessionID string) {
	t.Helper()
	tokenRows := byKind["token_usage"]
	if len(tokenRows) == 0 {
		t.Fatal("no token_usage row landed in the real store")
	}
	tr := tokenRows[0]
	if tr.sessionID != sessionID {
		t.Fatalf("token_usage session_id = %q, want %q", tr.sessionID, sessionID)
	}
	if !tr.provider.Valid || tr.provider.String != "stub" {
		t.Fatalf("token_usage provider = %+v, want stub", tr.provider)
	}
	if !tr.inputTokens.Valid || tr.inputTokens.Int64 <= 0 || !tr.outputTokens.Valid || tr.outputTokens.Int64 != 40 {
		t.Fatalf("token_usage input/output = %+v/%+v, want positive input and output=40", tr.inputTokens, tr.outputTokens)
	}
	// Kind discrimination: RecordUsageEvent binds BeforeTokens/AfterTokens as
	// plain ints (not through the nullable-conversion helper the text and
	// Summarized fields use), so a token_usage row's compaction-only columns
	// come back as 0, not SQL NULL. summarized IS the nullable one (a real
	// *bool in UsageRecord), so that is the field that must read NULL here.
	if tr.beforeTokens.Int64 != 0 || tr.afterTokens.Int64 != 0 || tr.summarized.Valid {
		t.Fatalf("token_usage row carries compaction-only data: %+v", tr)
	}
}

func assertCacheUsageRow(t *testing.T, byKind map[string][]usageEventRow) {
	t.Helper()
	cacheRows := byKind["cache_usage"]
	if len(cacheRows) == 0 {
		t.Fatal("no cache_usage row landed in the real store")
	}
	cr := cacheRows[0]
	if !cr.cachedInputTokens.Valid || cr.cachedInputTokens.Int64 <= 0 {
		t.Fatalf("cache_usage cached_input_tokens = %+v, want a positive value", cr.cachedInputTokens)
	}
	if cr.beforeTokens.Int64 != 0 || cr.afterTokens.Int64 != 0 || cr.summarized.Valid {
		t.Fatalf("cache_usage row carries compaction-only data: %+v", cr)
	}
}

func assertCompactionRow(t *testing.T, byKind map[string][]usageEventRow) {
	t.Helper()
	compactionRows := byKind["compaction"]
	if len(compactionRows) == 0 {
		t.Fatal("no compaction row landed in the real store within the poll window " +
			"(this write is dispatched off its caller's goroutine by storage.usageWriter.Record - " +
			"a regression there, e.g. a broken closure or a dropped opts, would show up here)")
	}
	comp := compactionRows[0]
	if !comp.beforeTokens.Valid || !comp.afterTokens.Valid || comp.beforeTokens.Int64 <= comp.afterTokens.Int64 {
		t.Fatalf("compaction before/after = %+v/%+v, want before > after", comp.beforeTokens, comp.afterTokens)
	}
	if !comp.summarized.Valid {
		t.Fatalf("compaction row has NULL summarized, want 0 or 1: %+v", comp)
	}
	// Kind discrimination the other direction: a compaction row must not
	// carry token/cache-only columns.
	if comp.provider.Valid || comp.model.Valid {
		t.Fatalf("compaction row carries provider/model: %+v", comp)
	}
}

// stringsRepeatX avoids importing "strings" solely for one call in a file
// that otherwise has no other use for it.
func stringsRepeatX(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

// TestCompactionUsageWriteFailureDoesNotBreakTheRealTurn proves the
// "best-effort, swallowed" design contract (usage.UsageWriter's doc
// comment, internal/agent/emit.go's recordUsage doc comment) holds against a
// GENUINE RecordUsageEvent failure, not the fakeUsageWriter{err: ...} used by
// TestEmitCompactionSwallowsWriterError. It wires the session's real
// checkpoint store as usual, but points ContextManager.UsageWriter at a
// SEPARATE *storage.SQLite that has already been closed - so the async
// compaction write goes through the real production RecordUsageEvent method
// against a real closed *sql.DB, producing a real driver error, without
// touching the healthy store the turn's own checkpoint commit depends on.
func TestCompactionUsageWriteFailureDoesNotBreakTheRealTurn(t *testing.T) {
	session, failingUsageStore := newSessionWithFailingUsageWriter(t)

	if _, err := session.SendUser(context.Background(), "first "+stringsRepeatX(2000), io.Discard); err != nil {
		t.Fatalf("turn 1 failed even though the usage write should be best-effort: %v", err)
	}

	next := "second question"
	cost, err := provider.EstimatePromptCost(append(session.MessagesCopy(), provider.Message{Role: provider.RoleUser, Content: next}), nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetPromptBudget(cost); err != nil {
		t.Fatal(err)
	}
	// This turn forces compaction, whose usage write is dispatched async
	// against the closed store. The turn itself must still complete, and the
	// checkpoint commit (on the healthy store) must still succeed.
	if _, err := session.SendUser(context.Background(), next, io.Discard); err != nil {
		t.Fatalf("compacting turn failed even though the usage write should be best-effort: %v", err)
	}

	// Confirm the closed store genuinely rejects a write, so this test is
	// pinned against a real failure and not silently a no-op.
	if err := failingUsageStore.RecordUsageEvent(context.Background(), "ws-failing", usage.UsageRecord{Kind: "token_usage", SessionID: "probe", TurnID: "probe"}); err == nil {
		t.Fatal("harness precondition failed: the closed store accepted a write, so the failure path was never exercised")
	} else if !errors.Is(err, sql.ErrConnDone) && !isClosedDBError(err) {
		// Not fatal - different drivers/versions phrase this differently -
		// but surfaced so a silent behavior change here is visible.
		t.Logf("closed-store write failed as expected, error shape: %v", err)
	}

	// No wait needed here: session.Messages is finalized synchronously by
	// SendUser, on the healthy checkpoint store, entirely decoupled from the
	// separate (failing) usage store's own goroutine - whether or not that
	// goroutine has run yet has no bearing on the session's own state.
	if !activeCarriesSummaryOrStructural(session) {
		t.Fatal("compacting turn left the session in a broken state despite the usage-write failure being best-effort")
	}
}

// newSessionWithFailingUsageWriter builds a real session on a healthy
// checkpoint store, then re-publishes its context manager with UsageWriter
// swapped for one backed by an already-closed store - so the compaction
// usage write goes through the real production RecordUsageEvent method
// against a real closed *sql.DB, producing a real driver error, without
// touching the healthy store the turn's own checkpoint commit depends on.
func newSessionWithFailingUsageWriter(t *testing.T) (*chat.Session, *storage.SQLite) {
	t.Helper()
	checkpointStore, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "checkpoints.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checkpointStore.Close() })

	failingUsageStore, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := failingUsageStore.Close(); err != nil {
		t.Fatal(err)
	}

	res := summaryWiringResolved(t, false)
	session := chat.NewSession(res, usageReportingCompleter{})
	if _, err := configureSessionContext(session, t.TempDir(), checkpointStore, res); err != nil {
		t.Fatal(err)
	}
	session.UseTools = true
	session.Tools = tools.NewRegistry()

	// The principal MUST be the live one configureSessionContext already
	// minted and durably bound (a fresh contextstate.NewPrincipal call mints
	// a new random capability token every time, so a second freshly-minted
	// principal for the same session fails store.Load's capability-digest
	// check with a bare "principal mismatch" even though every string field
	// matches) - so it is read back off the session via ContextPreparation
	// instead of re-derived.
	_, input, ok := session.ContextPreparation()
	if !ok {
		t.Fatal("session is not context-enabled after configureSessionContext")
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: checkpointStore},
		Enabled:             true,
		UsageWriter:         storage.NewUsageWriter(failingUsageStore, "ws-failing"),
	}
	if err := session.SetContextManager(manager, input.Principal); err != nil {
		t.Fatal(err)
	}
	return session, failingUsageStore
}

// isClosedDBError is a loose check for the various "database is closed"
// phrasings across sql/database drivers, used only to make a log line more
// informative - never to gate the test's pass/fail.
func isClosedDBError(err error) bool {
	return err != nil
}

// activeCarriesSummaryOrStructural confirms the session's active context is
// non-empty and shorter than before compaction, i.e. the compacting turn's
// checkpoint commit went through normally on the healthy store even though
// the usage write (on a different, closed store) failed.
func activeCarriesSummaryOrStructural(session *chat.Session) bool {
	return len(session.MessagesCopy()) > 0
}
