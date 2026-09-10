package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// --- deadlineMap unit tests --------------------------------------------

func intervalSpec(id string, everySeconds int64) Spec {
	return Spec{
		ID:      id,
		Name:    id,
		Enabled: true,
		Trigger: TriggerSpec{Kind: TriggerScheduled, Schedule: &ScheduleSpec{Kind: ScheduleInterval, EverySeconds: everySeconds}},
		Steps:   []Step{{Kind: StepPrompt, Prompt: "x"}},
	}
}

func atExhaustedSpec(id string, now time.Time) Spec {
	past := now.Add(-time.Hour).Format(time.RFC3339)
	return Spec{
		ID:      id,
		Name:    id,
		Enabled: true,
		Trigger: TriggerSpec{Kind: TriggerScheduled, Schedule: &ScheduleSpec{Kind: ScheduleAt, AtTimes: []string{past}}},
		Steps:   []Step{{Kind: StepPrompt, Prompt: "x"}},
	}
}

// TestRefreshLeavesUnchangedDeadlineAlone is the starvation regression: a
// due-but-unfired deadline must survive a refresh call completely
// untouched - refresh recomputing an already-armed deadline is exactly
// the bug this design replaces.
func TestRefreshLeavesUnchangedDeadlineAlone(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	spec := intervalSpec("starve-me-not", 60)
	m := newDeadlineMap()
	due := now.Add(-time.Second) // already due
	m.deadlines[spec.ID] = due

	m.refresh([]Spec{spec}, now, nil)

	got, ok := m.deadlines[spec.ID]
	if !ok {
		t.Fatal("refresh removed an existing deadline entry, want it untouched")
	}
	if !got.Equal(due) {
		t.Fatalf("refresh recomputed an existing deadline: got %v, want unchanged %v", got, due)
	}
}

// TestRefreshNeverSeenExhaustedWritesNothing is zero-guard 1: a
// never-seen automation whose schedule is a fully-spent ScheduleAt list
// must leave the map with no entry at all - never a zero time.Time.
func TestRefreshNeverSeenExhaustedWritesNothing(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	spec := atExhaustedSpec("exhausted-never-seen", now)
	m := newDeadlineMap()

	m.refresh([]Spec{spec}, now, nil)

	if _, ok := m.deadlines[spec.ID]; ok {
		t.Fatalf("refresh wrote a deadline for an exhausted never-seen automation: %v", m.deadlines[spec.ID])
	}
}

// TestAdvanceOnExhaustedWritesNothing is zero-guard 2: advance on a
// spec whose recomputed NextFire is (zero, nil) must DELETE any
// existing entry, never assign the zero value into the map.
func TestAdvanceOnExhaustedWritesNothing(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	spec := atExhaustedSpec("exhausted-advance", now)
	m := newDeadlineMap()
	// Simulate a stale armed entry, as if it had somehow been set before
	// its schedule became exhausted.
	m.deadlines[spec.ID] = now.Add(-time.Minute)

	m.advance(spec, now.Add(-time.Minute), now, nil)

	if got, ok := m.deadlines[spec.ID]; ok {
		t.Fatalf("advance on an exhausted schedule left an entry: %v, want deleted", got)
	}
}

// TestExhaustedScheduleNeverFiresAcrossManyTicks is THE fire-storm
// regression: drive hundreds of simulated refresh+due cycles with
// advancing time and assert due() returns the exhausted automation's ID
// exactly ZERO times across every iteration. A stored zero time.Time{}
// would compare as "always due" (due()'s `!deadline.After(now)`) and
// fire every tick forever; this test is the guard against that.
func TestExhaustedScheduleNeverFiresAcrossManyTicks(t *testing.T) {
	base := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	spec := atExhaustedSpec("fire-storm-guard", base)
	m := newDeadlineMap()

	fireCount := 0
	for i := 0; i < 500; i++ {
		now := base.Add(time.Duration(i) * time.Minute)
		m.refresh([]Spec{spec}, now, nil)
		for _, id := range m.due(now) {
			if id == spec.ID {
				fireCount++
			}
		}
	}
	if fireCount != 0 {
		t.Fatalf("exhausted schedule fired %d times across 500 simulated ticks, want exactly 0 (fire-storm regression)", fireCount)
	}
}

// TestAdvanceUsesMaxOfFiredDeadlineAndNow pins D6 skip-not-catch-up:
// when the daemon is keeping up (firedAt == now), the re-armed deadline
// is exactly one interval past the ORIGINAL fired deadline (phase
// preserved). When the daemon overslept several intervals (now far past
// firedAt), the re-armed deadline is exactly one interval past NOW -
// never a backlog walk forward from the stale firedAt.
func TestAdvanceUsesMaxOfFiredDeadlineAndNow(t *testing.T) {
	spec := intervalSpec("catch-up", 60)
	m := newDeadlineMap()

	// Case 1: keeping up.
	firedAt := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	m.advance(spec, firedAt, firedAt, nil)
	wantKeepingUp := firedAt.Add(60 * time.Second)
	if got := m.deadlines[spec.ID]; !got.Equal(wantKeepingUp) {
		t.Fatalf("advance (keeping up) = %v, want %v (one interval past the original fired deadline)", got, wantKeepingUp)
	}

	// Case 2: overslept several intervals - now is far past firedAt.
	overslept := firedAt.Add(500 * time.Second) // >8 missed 60s intervals
	m.advance(spec, firedAt, overslept, nil)
	wantCatchUp := overslept.Add(60 * time.Second)
	got := m.deadlines[spec.ID]
	if !got.Equal(wantCatchUp) {
		t.Fatalf("advance (overslept) = %v, want %v (one interval past now, not firedAt)", got, wantCatchUp)
	}
	// Must not be a backlog-style value stacked from firedAt.
	backlogValue := firedAt.Add(60 * time.Second)
	if got.Equal(backlogValue) {
		t.Fatal("advance produced a backlog-style deadline stacked from the stale firedAt, want catch-up to now")
	}
}

// TestRefreshPrunesDisabledAndRemovedAutomations proves refresh deletes
// map entries for automations no longer present in specs, or now
// disabled.
func TestRefreshPrunesDisabledAndRemovedAutomations(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	m := newDeadlineMap()
	m.deadlines["removed"] = now.Add(time.Hour)
	m.deadlines["disabled"] = now.Add(time.Hour)
	m.deadlines["kept"] = now.Add(time.Hour)

	disabled := intervalSpec("disabled", 60)
	disabled.Enabled = false
	kept := intervalSpec("kept", 60)

	m.refresh([]Spec{disabled, kept}, now, nil)

	if _, ok := m.deadlines["removed"]; ok {
		t.Fatal("refresh did not prune an automation absent from specs")
	}
	if _, ok := m.deadlines["disabled"]; ok {
		t.Fatal("refresh did not prune a now-disabled automation")
	}
	if _, ok := m.deadlines["kept"]; !ok {
		t.Fatal("refresh pruned a still-enabled, still-present automation")
	}
}

// TestReEnabledAutomationGetsFreshDeadlineNotStale: disable, refresh
// (pruned), re-enable with a CHANGED schedule, refresh again - the new
// deadline reflects the new schedule, with no stale carryover from
// before the disable.
func TestReEnabledAutomationGetsFreshDeadlineNotStale(t *testing.T) {
	t0 := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	m := newDeadlineMap()

	original := intervalSpec("toggle-me", 60)
	m.refresh([]Spec{original}, t0, nil)
	if got := m.deadlines["toggle-me"]; !got.Equal(t0.Add(60 * time.Second)) {
		t.Fatalf("initial refresh deadline = %v, want %v", got, t0.Add(60*time.Second))
	}

	disabled := original
	disabled.Enabled = false
	t1 := t0.Add(30 * time.Second)
	m.refresh([]Spec{disabled}, t1, nil)
	if _, ok := m.deadlines["toggle-me"]; ok {
		t.Fatal("refresh while disabled left a stale deadline entry")
	}

	reEnabled := intervalSpec("toggle-me", 30) // changed schedule
	t2 := t0.Add(2 * time.Minute)
	m.refresh([]Spec{reEnabled}, t2, nil)
	want := t2.Add(30 * time.Second)
	got, ok := m.deadlines["toggle-me"]
	if !ok {
		t.Fatal("refresh after re-enable produced no deadline")
	}
	if !got.Equal(want) {
		t.Fatalf("refresh after re-enable = %v, want %v (fresh from new schedule, no stale carryover)", got, want)
	}
}

// TestNextWakeEmptyMapReportsNotOK proves nextWake on an empty map
// reports the zero value and false.
func TestNextWakeEmptyMapReportsNotOK(t *testing.T) {
	m := newDeadlineMap()
	got, ok := m.nextWake()
	if ok {
		t.Fatalf("nextWake on empty map = (%v, true), want (_, false)", got)
	}
	if !got.IsZero() {
		t.Fatalf("nextWake on empty map returned non-zero time %v", got)
	}

	m.deadlines["a"] = time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	m.deadlines["b"] = time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	got, ok = m.nextWake()
	if !ok {
		t.Fatal("nextWake on non-empty map reported false")
	}
	if !got.Equal(m.deadlines["b"]) {
		t.Fatalf("nextWake = %v, want the earliest deadline %v", got, m.deadlines["b"])
	}
}

// --- Serve loop tests ----------------------------------------------------

// orderTrackingSpawner is serve_test.go's own SessionSpawner double: it
// records the strict order of CreateFreshInDir/CloseLastRun calls and
// the wall-clock interval each CreateFreshInDir call occupied, so a test
// can assert both the sequential-dispatch invariant (no two calls
// overlap in time) and the RunOnce->CloseLastRun call-order alternation
// Serve's doc comment promises.
type orderTrackingSpawner struct {
	mu          sync.Mutex
	events      []string
	intervals   []timeSpan
	conv        *recordingConversation
	closeErr    error
	createDelay time.Duration
}

type timeSpan struct{ start, end time.Time }

func (f *orderTrackingSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	start := time.Now()
	f.mu.Lock()
	f.events = append(f.events, "create")
	f.mu.Unlock()
	if f.createDelay > 0 {
		time.Sleep(f.createDelay)
	}
	if bind != nil {
		if _, err := bind(nil); err != nil {
			return nil, err
		}
	}
	end := time.Now()
	f.mu.Lock()
	f.intervals = append(f.intervals, timeSpan{start, end})
	f.mu.Unlock()
	return f.conv, nil
}

func (f *orderTrackingSpawner) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	return nil
}

func (f *orderTrackingSpawner) CloseLastRun() error {
	f.mu.Lock()
	f.events = append(f.events, "close")
	f.mu.Unlock()
	return f.closeErr
}

func (f *orderTrackingSpawner) snapshot() ([]string, []timeSpan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	events := make([]string, len(f.events))
	copy(events, f.events)
	intervals := make([]timeSpan, len(f.intervals))
	copy(intervals, f.intervals)
	return events, intervals
}

// seedServeAutomation writes automations.toml at root with one enabled,
// single-prompt-step automation under id, returning the saved Spec.
func seedServeAutomation(t *testing.T, root, id string) Spec {
	t.Helper()
	spec := Spec{ID: id, Name: id, Enabled: true, Steps: []Step{{Kind: StepPrompt, Prompt: "hi"}}}
	existing, err := LoadSpecs(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("LoadSpecs: %v", err)
	}
	existing = append(existing, spec)
	if err := SaveSpecs(ports.ScopeProject, root, existing); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}
	return spec
}

// TestServeFiresDueAutomationsSequentially asserts, via the fake
// spawner's own recorded time intervals, that RunOnce calls for two
// different due automations never overlap in wall-clock time - the
// concurrency invariant Serve's doc comment promises, tested rather than
// just documented.
func TestServeFiresDueAutomationsSequentially(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	seedServeAutomation(t, root, "seq-a")
	seedServeAutomation(t, root, "seq-b")
	spawn := &orderTrackingSpawner{conv: newRecordingConversation(), createDelay: 20 * time.Millisecond}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := time.Now().UTC()
	m := newDeadlineMap()
	m.deadlines["seq-a"] = now.Add(-time.Second)
	m.deadlines["seq-b"] = now.Add(-time.Second)

	svc.serveTick(context.Background(), m, nil)

	_, intervals := spawn.snapshot()
	if len(intervals) != 2 {
		t.Fatalf("recorded %d CreateFreshInDir intervals, want 2", len(intervals))
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	if intervals[1].start.Before(intervals[0].end) {
		t.Fatalf("two automations' RunOnce dispatch overlapped in time: %+v", intervals)
	}
}

// TestServeCallsCloseLastRunAfterEachRunOnce proves the strict
// RunOnce->CloseLastRun call-order alternation across several due
// automations in one tick.
func TestServeCallsCloseLastRunAfterEachRunOnce(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	seedServeAutomation(t, root, "order-a")
	seedServeAutomation(t, root, "order-b")
	spawn := &orderTrackingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	m := newDeadlineMap()
	m.deadlines["order-a"] = now.Add(-time.Second)
	m.deadlines["order-b"] = now.Add(-time.Second)

	svc.serveTick(context.Background(), m, nil)

	events, _ := spawn.snapshot()
	want := []string{"create", "close", "create", "close"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v (order mismatch at %d)", events, want, i)
		}
	}
}

// TestServeCallsCloseLastRunEvenWhenRunOnceFails proves CloseLastRun is
// called unconditionally even when RunOnce itself returns a real error
// (here: startRun's createRun fails because automation_runs was
// dropped).
func TestServeCallsCloseLastRunEvenWhenRunOnceFails(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	seedServeAutomation(t, root, "fail-me")
	spawn := &orderTrackingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("drop automation_runs table: %v", err)
	}
	now := time.Now().UTC()
	m := newDeadlineMap()
	m.deadlines["fail-me"] = now.Add(-time.Second)

	svc.serveTick(context.Background(), m, nil)

	events, _ := spawn.snapshot()
	if len(events) != 1 || events[0] != "close" {
		t.Fatalf("events = %v, want [close] (RunOnce failed before ever spawning a session, but close still ran)", events)
	}
}

// nonClosingSpawner satisfies ONLY automation.SessionSpawner - no
// CloseLastRun method at all - pinning the TUI spawner's safety: Serve
// must not panic when the failed type assertion misses.
type nonClosingSpawner struct {
	conv *recordingConversation
}

func (f *nonClosingSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	if bind != nil {
		if _, err := bind(nil); err != nil {
			return nil, err
		}
	}
	return f.conv, nil
}

func (f *nonClosingSpawner) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	return nil
}

// TestServeToleratesSpawnerWithoutCloseLastRun proves Serve's tick
// proceeds without panicking against a spawner implementing only the
// 2-method SessionSpawner interface - the exact shape
// internal/newtui's automationSessionSpawner has.
func TestServeToleratesSpawnerWithoutCloseLastRun(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	seedServeAutomation(t, root, "no-closer")
	spawn := &nonClosingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	m := newDeadlineMap()
	m.deadlines["no-closer"] = now.Add(-time.Second)

	// Must not panic.
	svc.serveTick(context.Background(), m, nil)

	got, ok := svc.getRunByAutomationHelper(t, "no-closer")
	if !ok {
		t.Fatal("serveTick against a spawner without CloseLastRun did not run the due automation")
	}
	if got.State != RunSucceeded {
		t.Fatalf("run state = %v, want RunSucceeded", got.State)
	}
}

// getRunByAutomationHelper is a small serveTick-test-only convenience:
// list the automation's runs and return the most recent one.
func (s *Service) getRunByAutomationHelper(t *testing.T, automationID string) (Run, bool) {
	t.Helper()
	runs, err := s.listRuns(context.Background(), automationID, 1)
	if err != nil || len(runs) == 0 {
		return Run{}, false
	}
	return runs[0], true
}

// TestServeIgnoresCloseLastRunError proves a CloseLastRun error is
// discarded: the tick proceeds to the next due automation rather than
// aborting.
func TestServeIgnoresCloseLastRunError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	seedServeAutomation(t, root, "close-err-a")
	seedServeAutomation(t, root, "close-err-b")
	spawn := &orderTrackingSpawner{conv: newRecordingConversation(), closeErr: fmt.Errorf("close boom")}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	m := newDeadlineMap()
	m.deadlines["close-err-a"] = now.Add(-time.Second)
	m.deadlines["close-err-b"] = now.Add(-time.Second)

	svc.serveTick(context.Background(), m, nil)

	events, _ := spawn.snapshot()
	want := []string{"create", "close", "create", "close"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v (a close error must not abort the tick)", events, want)
	}
}

// TestServeContinuesAfterOneAutomationsRunOnceError proves a broken
// automation (here: disabled, so RunOnce returns ErrAutomationDisabled)
// does not wedge a sibling due automation in the same tick.
func TestServeContinuesAfterOneAutomationsRunOnceError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	bad := Spec{ID: "broken-one", Name: "broken-one", Enabled: false, Steps: []Step{{Kind: StepPrompt, Prompt: "x"}}}
	good := Spec{ID: "good-one", Name: "good-one", Enabled: true, Steps: []Step{{Kind: StepPrompt, Prompt: "x"}}}
	if err := SaveSpecs(ports.ScopeProject, root, []Spec{bad, good}); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}
	spawn := &orderTrackingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	m := newDeadlineMap()
	m.deadlines["broken-one"] = now.Add(-time.Second)
	m.deadlines["good-one"] = now.Add(-time.Second)

	svc.serveTick(context.Background(), m, nil)

	goodRun, ok := svc.getRunByAutomationHelper(t, "good-one")
	if !ok || goodRun.State != RunSucceeded {
		t.Fatalf("good-one run = (%+v, %v), want a succeeded run despite broken-one's failure", goodRun, ok)
	}
	brokenRuns, err := svc.listRuns(context.Background(), "broken-one", 10)
	if err != nil {
		t.Fatalf("listRuns broken-one: %v", err)
	}
	if len(brokenRuns) != 0 {
		t.Fatalf("broken-one (disabled) got a run record %+v, want none (RunOnce refused before any run row)", brokenRuns)
	}
}

// TestServeReturnsCtxErrOnCancel proves Serve returns ctx.Err() promptly
// once ctx is cancelled.
func TestServeReturnsCtxErrOnCancel(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawn := &orderTrackingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = svc.Serve(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve(cancelled ctx) = %v, want context.Canceled", err)
	}
}

// TestServeRunsOwnLoopBody exercises the REAL Serve(ctx) function's own
// loop body (as opposed to calling serveTick directly, which every
// other test above does) - lines 244-247/260/268-275 of serve.go are
// only reachable through Serve's own for-loop-plus-select, never
// through a direct serveTick call, because serveTickInterval's select
// and the initial call before it live only inside Serve itself. Shrinks
// serveTickInterval so the test observes a real tick within a bounded
// wall-clock window, then cancels and asserts a clean return.
func TestServeRunsOwnLoopBody(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	seedServeAutomation(t, root, "loop-body")
	spawn := &orderTrackingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	prevInterval := serveTickInterval
	serveTickInterval = 10 * time.Millisecond
	t.Cleanup(func() { serveTickInterval = prevInterval })

	// Seed the automation as immediately due by writing a schedule whose
	// NextFire is already in the past relative to Serve's own internal
	// deadlineMap (built fresh inside Serve, not injectable) - an
	// interval schedule with every_seconds=1 fires on its FIRST refresh
	// pass only once its own deadline (now+1s) elapses, so instead seed
	// a manual-trigger-shaped spec is not enough (never scheduled at
	// all); use a very short interval and wait past two tick intervals
	// for the deadline to both arm and elapse.
	specs, err := LoadSpecs(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("LoadSpecs: %v", err)
	}
	for i := range specs {
		specs[i].Trigger = TriggerSpec{Kind: TriggerScheduled, Schedule: &ScheduleSpec{Kind: ScheduleInterval, EverySeconds: 1}}
	}
	if err := SaveSpecs(ports.ScopeProject, root, specs); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- svc.Serve(ctx) }()

	deadline := time.After(2500 * time.Millisecond)
	fired := false
	for !fired {
		select {
		case <-deadline:
			cancel()
			<-errCh
			t.Fatal("Serve's own loop never dispatched the due automation within 2.5s")
		case <-time.After(50 * time.Millisecond):
			if _, ok := svc.getRunByAutomationHelper(t, "loop-body"); ok {
				fired = true
			}
		}
	}
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve returned %v after cancel, want context.Canceled", err)
	}
}

// TestServeTickLogsLoadSpecsErrorAndReturns covers serveTick's
// LoadSpecs-error branch: malformed automations.toml must not panic the
// tick, must not advance any deadline, and must simply return so the
// NEXT tick (once the file is fixed) can proceed normally.
func TestServeTickLogsLoadSpecsErrorAndReturns(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawn := &orderTrackingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mivia", "automations.toml"), []byte("not = [valid toml"), 0o600); err != nil {
		t.Fatalf("write malformed automations.toml: %v", err)
	}

	var loggedErr error
	logf := func(format string, args ...any) { loggedErr = fmt.Errorf(format, args...) }
	m := newDeadlineMap()

	// Must not panic.
	svc.serveTick(context.Background(), m, logf)

	if loggedErr == nil {
		t.Fatal("serveTick against malformed automations.toml logged nothing, want a load-specs error logged")
	}
	if len(m.deadlines) != 0 {
		t.Fatalf("serveTick against malformed automations.toml populated deadlines: %v, want none", m.deadlines)
	}
}

// TestNoteBrokenLogsOncePerTransition covers noteBroken's own
// once-per-transition dedup, exercised directly (bypassing
// refresh/advance's own armed-deadline short-circuit, which is a
// separate concern already covered by TestRefreshLeavesUnchangedDeadlineAlone):
// a broken schedule logs on its first noteBroken call, does NOT log
// again on a second noteBroken call while still broken, and logs again
// after clearBroken (a fix) followed by a re-break.
func TestNoteBrokenLogsOncePerTransition(t *testing.T) {
	m := newDeadlineMap()
	logCount := 0
	logf := func(format string, args ...any) { logCount++ }
	boom := fmt.Errorf("boom")

	m.noteBroken("broken-cron", boom, logf)
	if logCount != 1 {
		t.Fatalf("first noteBroken: logCount = %d, want 1", logCount)
	}

	m.noteBroken("broken-cron", boom, logf)
	if logCount != 1 {
		t.Fatalf("second noteBroken while still broken: logCount = %d, want 1 (no re-log while unchanged)", logCount)
	}

	m.clearBroken("broken-cron")
	m.noteBroken("broken-cron", boom, logf)
	if logCount != 2 {
		t.Fatalf("noteBroken after clearBroken (a fix, then re-break): logCount = %d, want 2", logCount)
	}
}

// TestNoteBrokenNeverLogsErrNoSchedule proves ErrNoSchedule (a manual
// trigger has no schedule at all) is never logged, unlike a genuinely
// broken schedule - manual triggers are not a fault.
func TestNoteBrokenNeverLogsErrNoSchedule(t *testing.T) {
	m := newDeadlineMap()
	logCount := 0
	logf := func(format string, args ...any) { logCount++ }

	m.noteBroken("manual-one", ErrNoSchedule, logf)
	if logCount != 0 {
		t.Fatalf("noteBroken(ErrNoSchedule): logCount = %d, want 0", logCount)
	}
}

// TestRefreshBrokenScheduleNeverSeenBranch covers refresh's own
// never-seen broken-schedule branch (line 114-116: NextFire errors,
// noteBroken is called, the loop continues without arming a deadline) -
// distinct from TestNoteBrokenLogsOncePerTransition, which calls
// noteBroken directly rather than through refresh.
func TestRefreshBrokenScheduleNeverSeenBranch(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	broken := Spec{
		ID: "broken-cron-refresh", Name: "broken-cron-refresh", Enabled: true,
		Trigger: TriggerSpec{Kind: TriggerScheduled, Schedule: &ScheduleSpec{Kind: ScheduleRecurring, Cron: "not a cron", TZ: "UTC"}},
		Steps:   []Step{{Kind: StepPrompt, Prompt: "x"}},
	}
	m := newDeadlineMap()
	logged := false
	logf := func(format string, args ...any) { logged = true }

	m.refresh([]Spec{broken}, now, logf)

	if !logged {
		t.Fatal("refresh with a broken never-seen schedule never logged")
	}
	if _, ok := m.deadlines[broken.ID]; ok {
		t.Fatal("refresh armed a deadline for a broken schedule, want none")
	}

	// Prune-cleanup path: brokenLogged entries for IDs no longer seen
	// must also be pruned (lines 130-133).
	m.refresh(nil, now.Add(time.Minute), logf)
	if m.brokenLogged[broken.ID] {
		t.Fatal("refresh did not prune brokenLogged for a removed automation")
	}
}

// TestServeTickSkipsIDMissingFromSpecByID covers serveTick's own
// defensive continue (line 259-260): an ID present in m.due(now) but
// absent from the freshly-loaded specByID map (a spec deleted between
// the deadline being armed and this tick running) must be skipped, not
// dereferenced. Simulated by seeding a deadline for an ID that was
// never saved to the store at all.
func TestServeTickSkipsIDMissingFromSpecByID(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawn := &orderTrackingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	m := newDeadlineMap()
	m.deadlines["ghost-automation"] = now.Add(-time.Second)

	// Must not panic despite "ghost-automation" never having been saved.
	svc.serveTick(context.Background(), m, nil)

	events, _ := spawn.snapshot()
	if len(events) != 0 {
		t.Fatalf("serveTick dispatched a spec-less ghost ID: events = %v, want none", events)
	}
}

// TestServeTickLogsRunSkippedOnLostClaim covers serveTick's
// D7-lost-claim log branch (line 270-275): a fire that loses the fenced
// claim (another holder already owns it) returns a RunSkipped run with
// a nil error, and serveTick must log a distinct "skipped" message
// rather than the "run failed" message the err!=nil branch logs -
// pre-acquiring the claim exactly as TestRunOnceDedupConcurrent
// (executor_test.go) does forces this deterministically.
func TestServeTickLogsRunSkippedOnLostClaim(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	seedServeAutomation(t, root, "claim-holder-blocks-serve")
	spawn := &orderTrackingSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// Pre-acquire the claim exactly as a genuinely-concurrent fire would
	// hold it, forcing serveTick's own RunOnce call to lose D7's dedup.
	_, ok, err := svc.admitFire(ctx, "claim-holder-blocks-serve")
	if err != nil || !ok {
		t.Fatalf("pre-acquire admitFire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	var logged string
	logf := func(format string, args ...any) { logged = fmt.Sprintf(format, args...) }
	now := time.Now().UTC()
	m := newDeadlineMap()
	m.deadlines["claim-holder-blocks-serve"] = now.Add(-time.Second)

	svc.serveTick(ctx, m, logf)

	if !strings.Contains(logged, "skipped") {
		t.Fatalf("serveTick against a lost claim logged %q, want it to mention \"skipped\"", logged)
	}
	events, _ := spawn.snapshot()
	if len(events) != 1 || events[0] != "close" {
		t.Fatalf("serveTick against a lost claim spawned a session: events = %v, want [close] only (RunOnce never reaches spawnRunSession on a lost claim)", events)
	}
}
