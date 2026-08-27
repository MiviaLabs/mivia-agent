package cliorchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	gruntime "runtime"
	"runtime/pprof"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// This file holds adversarial integration probes for a live production
// symptom: an outer dispatch_tasks (wait=run) whose subagent tasks THEMSELVES
// call dispatch_tasks (nested fan-out) sat "running" for 21+ minutes with the
// outer Join never returning. Each probe mirrors the production wiring as
// closely as the harness allows: one dispatcher, one coordinator (the
// InitCoordinator singleton keyed on the dispatcher), one shared ledger
// repository, and the real dispatchTasksTool.Execute for both the outer and
// the nested dispatch. Every probe carries a hard deadline: a probe that
// WOULD hang fails fast with a goroutine dump instead of hanging the suite.

// dumpGoroutines writes the full goroutine stacks into the test log so a
// deadlock probe failure localizes the blocked frames.
func dumpGoroutines(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	t.Logf("goroutine dump at probe failure:\n%s", buf.String())
}

// ledgerStateDump renders every run and task status in repo for diagnostics.
func ledgerStateDump(t *testing.T, repo ledger.LedgerRepository) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runs, err := repo.ListRuns(ctx)
	if err != nil {
		return fmt.Sprintf("ListRuns failed: %v", err)
	}
	var b strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&b, "run %s status=%s\n", r.RunID, r.Status)
		tasks, err := repo.ListTasks(ctx, r.RunID)
		if err != nil {
			fmt.Fprintf(&b, "  ListTasks failed: %v\n", err)
			continue
		}
		for _, task := range tasks {
			fmt.Fprintf(&b, "  task %s status=%s\n", task.TaskID, task.Status)
		}
	}
	return b.String()
}

// isRunTerminal reports whether a run status is terminal in the ledger.
func isRunTerminal(s ledger.RunStatus) bool {
	switch s {
	case ledger.RunStatusCompleted, ledger.RunStatusFailed, ledger.RunStatusCanceled:
		return true
	default:
		return false
	}
}

// nonTerminalLedgerRows returns a description of every run or task that is
// not in a terminal status, or "" when the whole ledger settled.
func nonTerminalLedgerRows(repo ledger.LedgerRepository) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runs, err := repo.ListRuns(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, r := range runs {
		if !isRunTerminal(r.Status) {
			fmt.Fprintf(&b, "run %s status=%s; ", r.RunID, r.Status)
		}
		tasks, err := repo.ListTasks(ctx, r.RunID)
		if err != nil {
			return "", err
		}
		for _, task := range tasks {
			if !coordinator.IsTaskTerminal(string(task.Status)) {
				fmt.Fprintf(&b, "run %s task %s status=%s; ", r.RunID, task.TaskID, task.Status)
			}
		}
	}
	return b.String(), nil
}

// requireLedgerSettles polls until every run and task in repo is terminal, or
// fails with the leaked rows and a goroutine dump. A leaked "running" row
// with every caller already returned is exactly the UI symptom under audit.
func requireLedgerSettles(t *testing.T, repo ledger.LedgerRepository, within time.Duration, label string) {
	t.Helper()
	deadline := time.Now().Add(within)
	var leaked string
	for time.Now().Before(deadline) {
		rows, err := nonTerminalLedgerRows(repo)
		if err != nil {
			t.Fatalf("%s: walking ledger: %v", label, err)
		}
		if rows == "" {
			return
		}
		leaked = rows
		time.Sleep(50 * time.Millisecond)
	}
	dumpGoroutines(t)
	t.Fatalf("%s: PROBE FAILURE - ledger rows still non-terminal after %s (the finalize gap the UI shows): %s\nledger:\n%s",
		label, within, leaked, ledgerStateDump(t, repo))
}

// outerCallerCtx stamps a stable caller identity, the way one real chat
// session's turns do, so idempotency scoping and depth accounting match
// production instead of RunThroughCoordinator's synthesized fallback.
func outerCallerCtx(base context.Context) context.Context {
	return runtime.ContextWithCaller(base, runtime.Caller{SessionID: "nested-probe-session"})
}

// --- Probe 1: nested synchronous dispatch completes -------------------------

// TestNestedSyncDispatchCompletes reproduces the production shape at the
// smallest size that can deadlock: an outer wait=run dispatch of 3 tasks
// whose handler each performs its own synchronous dispatch_tasks of 2 leaf
// tasks through the SAME dispatcher (and therefore, via the InitCoordinator
// singleton, the same coordinator, pool, and ledger repo). If nested fan-out
// starves a shared bounded resource, the outer Execute never returns and this
// probe fails with a goroutine dump - localizing the production hang.
func TestNestedSyncDispatchCompletes(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func() config.SubagentConfig
	}{
		{
			// Production default: MaxWorkers=0 (pool spawns one worker per
			// task per batch, no shared bounded pool).
			name: "default_unbounded_workers",
			cfg:  func() config.SubagentConfig { return config.DefaultSubagentConfig },
		},
		{
			// Adversarial: a bounded worker setting. Pool workers are created
			// per execute() batch, so even Workers=1 must not starve nested
			// batches - if it does, any operator who sets max_workers has the
			// production deadlock.
			name: "bounded_single_worker",
			cfg: func() config.SubagentConfig {
				cfg := config.DefaultSubagentConfig
				cfg.MaxWorkers = 1
				return cfg
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runNestedSyncDispatchCase(t, tc.name, tc.cfg())
		})
	}
}

// runNestedSyncDispatchCase runs one TestNestedSyncDispatchCompletes case
// (an outer wait=run dispatch of 3 tasks, each nesting its own synchronous
// dispatch_tasks of 2 leaves through the same coordinator) to completion and
// asserts every leaf ran and the ledger settled. Split out of the test body
// to stay under the function-LOC gate.
func runNestedSyncDispatchCase(t *testing.T, name string, cfg config.SubagentConfig) {
	t.Helper()
	d := runtime.New(runtime.Policy{MaxDepth: 6})
	t.Cleanup(d.Close)
	repo := ledger.NewMemoryLedgerRepository()

	var leafRuns atomic.Int32
	if err := d.Register(runtime.Subagent, "leaf", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		leafRuns.Add(1)
		return json.RawMessage(`"leaf-done"`), nil
	})); err != nil {
		t.Fatal(err)
	}
	nested := NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "leaf"))
	if err := d.Register(runtime.Subagent, "mid", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		body, err := nested.Execute(ctx, json.RawMessage(
			`{"tasks":[{"id":"leaf-a","agent":"leaf","prompt":"a"},{"id":"leaf-b","agent":"leaf","prompt":"b"}]}`))
		if err != nil {
			return nil, fmt.Errorf("nested dispatch: %w", err)
		}
		if !strings.Contains(body, "completed") {
			return nil, fmt.Errorf("nested dispatch did not complete: %s", body)
		}
		return json.RawMessage(`"mid-done"`), nil
	})); err != nil {
		t.Fatal(err)
	}
	outer := NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "mid"))

	type execResult struct {
		body string
		err  error
	}
	done := make(chan execResult, 1)
	go func() {
		body, err := outer.Execute(outerCallerCtx(context.Background()),
			json.RawMessage(`{"tasks":[{"id":"m1","agent":"mid","prompt":"1"},{"id":"m2","agent":"mid","prompt":"2"},{"id":"m3","agent":"mid","prompt":"3"}]}`))
		done <- execResult{body: body, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("outer dispatch returned transport error: %v", res.err)
		}
		if strings.Count(res.body, `"completed"`) < 3 {
			t.Fatalf("outer dispatch finished but not all 3 mid tasks completed: %s", res.body)
		}
		if got := leafRuns.Load(); got != 6 {
			t.Fatalf("expected 6 leaf executions (3 mid x 2 leaves), got %d", got)
		}
	case <-time.After(20 * time.Second):
		dumpGoroutines(t)
		t.Fatalf("PROBE FAILURE: outer wait=run dispatch with nested synchronous dispatch never returned within 20s - nested fan-out deadlock (cfg=%s)\nledger:\n%s",
			name, ledgerStateDump(t, repo))
	}
	requireLedgerSettles(t, repo, 10*time.Second, "probe1/"+name)
}

// --- Probe 2: nested + concurrent siblings, ledger fully terminal -----------

// TestNestedConcurrentSiblingsLedgerTerminal runs 3 outer tasks that each
// nest 3 leaf tasks, all concurrently through ONE coordinator and ONE shared
// memory ledger repo, then walks the ledger: every run and every task must
// reach a terminal status. A returned outer call with a leaked non-terminal
// row is the exact "UI panel row never resolves" symptom.
func TestNestedConcurrentSiblingsLedgerTerminal(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	d := runtime.New(runtime.Policy{MaxDepth: 6})
	t.Cleanup(d.Close)
	repo := ledger.NewMemoryLedgerRepository()

	// Leaves stall briefly on a shared gate so all nested runs overlap in
	// time - the concurrency shape of the production incident - rather than
	// completing serially by accident of scheduling.
	gate := make(chan struct{})
	var leafStartedCount atomic.Int32
	leafGateReady := make(chan struct{})
	if err := d.Register(runtime.Subagent, "leaf", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		if leafStartedCount.Add(1) == 9 {
			close(leafGateReady)
		}
		select {
		case <-gate:
			return json.RawMessage(`"leaf-done"`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})); err != nil {
		t.Fatal(err)
	}
	nested := NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "leaf"))
	if err := d.Register(runtime.Subagent, "mid", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		var prompt string
		_ = json.Unmarshal(req.Input, &prompt)
		args := fmt.Sprintf(`{"tasks":[{"id":"l-%s-1","agent":"leaf","prompt":"x"},{"id":"l-%s-2","agent":"leaf","prompt":"y"},{"id":"l-%s-3","agent":"leaf","prompt":"z"}]}`, prompt, prompt, prompt)
		body, err := nested.Execute(ctx, json.RawMessage(args))
		if err != nil {
			return nil, err
		}
		if !strings.Contains(body, "completed") {
			return nil, fmt.Errorf("nested dispatch did not complete: %s", body)
		}
		return json.RawMessage(`"mid-done"`), nil
	})); err != nil {
		t.Fatal(err)
	}
	outer := NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "mid"))

	type execResult struct {
		body string
		err  error
	}
	done := make(chan execResult, 1)
	go func() {
		body, err := outer.Execute(outerCallerCtx(context.Background()),
			json.RawMessage(`{"tasks":[{"id":"m1","agent":"mid","prompt":"m1"},{"id":"m2","agent":"mid","prompt":"m2"},{"id":"m3","agent":"mid","prompt":"m3"}]}`))
		done <- execResult{body: body, err: err}
	}()

	// All 9 leaves must be live at once before the gate opens: this is the
	// point where a shared-resource starvation deadlock would already have
	// bitten (a starved leaf never starts and this wait times out).
	select {
	case <-leafGateReady:
	case <-time.After(15 * time.Second):
		dumpGoroutines(t)
		t.Fatalf("PROBE FAILURE: not all 9 nested leaf tasks started within 15s - nested batches starve each other\nledger:\n%s", ledgerStateDump(t, repo))
	}
	close(gate)

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("outer dispatch returned transport error: %v", res.err)
		}
		if strings.Count(res.body, `"completed"`) < 3 {
			t.Fatalf("outer dispatch finished but not all 3 mid tasks completed: %s", res.body)
		}
	case <-time.After(20 * time.Second):
		dumpGoroutines(t)
		t.Fatalf("PROBE FAILURE: outer dispatch never returned after leaves were released\nledger:\n%s", ledgerStateDump(t, repo))
	}
	requireLedgerSettles(t, repo, 10*time.Second, "probe2")
}

// --- Probe 3: nested + caller cancel mid-flight -----------------------------

// TestNestedDispatchCallerCancelMidFlight cancels the OUTER caller context
// while nested runs are in progress (leaf handlers blocked). Per current
// semantics the outer synchronous dispatch must return promptly,
// cancelOrphanedRun must cancel the outer run, and the cancellation must
// CASCADE into the nested runs: outer pool ctx dies -> mid task ctx dies ->
// mid's nested Join returns -> nested cancelOrphanedRun fires -> leaf ctx
// dies. If nested runs survive as orphans holding "running" ledger rows
// forever, that is the leak matching the 21-minute production symptom.
// nestedCancelProbeHarness holds everything
// TestNestedDispatchCallerCancelMidFlight needs after wiring the coordinator,
// so the test body can stay focused on its 4 numbered assertion phases.
type nestedCancelProbeHarness struct {
	repo             ledger.LedgerRepository
	cancel           context.CancelFunc
	done             chan nestedCancelProbeResult
	leafReady        <-chan struct{}
	leafCanceled     chan struct{}
	leafCount        int
	goroutinesBefore int
}

type nestedCancelProbeResult struct {
	body string
	err  error
}

// setupNestedCancelProbe wires an outer wait=run dispatch of 3 tasks, each
// nesting its own synchronous dispatch_tasks of 2 leaves, and starts the
// outer Execute in the background under a cancelable caller context. Split
// out of the test body to stay under the function-LOC gate.
func setupNestedCancelProbe(t *testing.T) *nestedCancelProbeHarness {
	t.Helper()
	cfg := config.DefaultSubagentConfig
	d := runtime.New(runtime.Policy{MaxDepth: 6})
	t.Cleanup(d.Close)
	repo := ledger.NewMemoryLedgerRepository()

	goroutinesBefore := gruntime.NumGoroutine()

	const leafCount = 6 // 3 mid x 2 leaves
	var leafStartedCount atomic.Int32
	leafReady := make(chan struct{})
	leafCanceled := make(chan struct{}, leafCount*2)
	if err := d.Register(runtime.Subagent, "leaf", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		if leafStartedCount.Add(1) == leafCount {
			close(leafReady)
		}
		<-ctx.Done()
		leafCanceled <- struct{}{}
		return nil, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	nested := NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "leaf"))
	if err := d.Register(runtime.Subagent, "mid", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		var prompt string
		_ = json.Unmarshal(req.Input, &prompt)
		args := fmt.Sprintf(`{"tasks":[{"id":"l-%s-1","agent":"leaf","prompt":"x"},{"id":"l-%s-2","agent":"leaf","prompt":"y"}]}`, prompt, prompt)
		body, err := nested.Execute(ctx, json.RawMessage(args))
		if err != nil {
			return nil, err
		}
		_ = body // canceled mid-flight: the envelope carries canceled statuses
		return json.RawMessage(`"mid-done"`), nil
	})); err != nil {
		t.Fatal(err)
	}
	outer := NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "mid"))

	ctx, cancel := context.WithCancel(outerCallerCtx(context.Background()))
	done := make(chan nestedCancelProbeResult, 1)
	go func() {
		body, err := outer.Execute(ctx, json.RawMessage(
			`{"tasks":[{"id":"m1","agent":"mid","prompt":"m1"},{"id":"m2","agent":"mid","prompt":"m2"},{"id":"m3","agent":"mid","prompt":"m3"}]}`))
		done <- nestedCancelProbeResult{body: body, err: err}
	}()

	return &nestedCancelProbeHarness{
		repo: repo, cancel: cancel, done: done,
		leafReady: leafReady, leafCanceled: leafCanceled,
		leafCount: leafCount, goroutinesBefore: goroutinesBefore,
	}
}

func TestNestedDispatchCallerCancelMidFlight(t *testing.T) {
	h := setupNestedCancelProbe(t)
	defer h.cancel()
	repo, done, leafReady, leafCanceled, leafCount, goroutinesBefore :=
		h.repo, h.done, h.leafReady, h.leafCanceled, h.leafCount, h.goroutinesBefore

	// Wait for every nested leaf to actually be running before canceling,
	// so the cancel truly lands mid-flight on live nested runs.
	select {
	case <-leafReady:
	case <-time.After(15 * time.Second):
		dumpGoroutines(t)
		t.Fatalf("PROBE FAILURE: nested leaves never all started\nledger:\n%s", ledgerStateDump(t, repo))
	}
	h.cancel()

	// 1. The outer synchronous dispatch must return promptly after cancel.
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("outer Execute must not return a transport error on cancel (envelope expected): %v", res.err)
		}
	case <-time.After(10 * time.Second):
		dumpGoroutines(t)
		t.Fatalf("PROBE FAILURE: outer dispatch did not return within 10s of caller cancel\nledger:\n%s", ledgerStateDump(t, repo))
	}

	// 2. The cancellation must cascade into every nested leaf handler. A leaf
	// still blocked here is an orphaned nested run: invisible, unkillable, and
	// holding a "running" ledger row - the production leak.
	for i := 0; i < leafCount; i++ {
		select {
		case <-leafCanceled:
		case <-time.After(15 * time.Second):
			dumpGoroutines(t)
			t.Fatalf("PROBE FAILURE: only %d of %d nested leaf handlers observed cancellation within 15s - nested runs orphaned after outer caller cancel\nledger:\n%s",
				i, leafCount, ledgerStateDump(t, repo))
		}
	}

	// 3. Ledger: nothing may be left non-terminal. cancelOrphanedRun's own
	// budget is 30s, so grant a window past the cascade, not past the budget.
	requireLedgerSettles(t, repo, 20*time.Second, "probe3")

	// 4. Goroutine sanity band (goleak is not vendored). evictHandleAfterTerminal
	// intentionally holds one goroutine per run for the 10-minute handle
	// retention (4 runs here), so the band is loose; it exists to catch
	// wholesale leaks (a blocked pool worker per task), not the retention
	// goroutines.
	deadline := time.Now().Add(10 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		gruntime.GC()
		after = gruntime.NumGoroutine()
		if after <= goroutinesBefore+15 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if after > goroutinesBefore+30 {
		dumpGoroutines(t)
		t.Fatalf("PROBE FAILURE: goroutines grew from %d to %d after cancel settled - leaked workers or joins", goroutinesBefore, after)
	}
}

// --- Probe 5: timeout layering ----------------------------------------------

// TestNestedDispatchShortTimeoutDoesNotPoisonOuter gives the NESTED dispatch
// an explicit 1s timeout_seconds while its leaf handler would block 10s. The
// nested run must resolve as timed_out within a small multiple of its budget,
// the OUTER run must then complete normally (a nested timeout must not poison
// the outer Join), and no ledger row may stay running.
func TestNestedDispatchShortTimeoutDoesNotPoisonOuter(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	d := runtime.New(runtime.Policy{MaxDepth: 6})
	t.Cleanup(d.Close)
	repo := ledger.NewMemoryLedgerRepository()

	if err := d.Register(runtime.Subagent, "leaf", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return json.RawMessage(`"leaf outlived its budget"`), nil
		}
	})); err != nil {
		t.Fatal(err)
	}
	nested := NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "leaf"))

	type nestedObservation struct {
		body    string
		elapsed time.Duration
	}
	nestedSeen := make(chan nestedObservation, 1)
	if err := d.Register(runtime.Subagent, "mid", handlerFunc(func(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
		start := time.Now()
		body, err := nested.Execute(ctx, json.RawMessage(
			`{"tasks":[{"id":"slow-leaf","agent":"leaf","prompt":"block"}],"timeout_seconds":1}`))
		if err != nil {
			return nil, fmt.Errorf("nested dispatch transport error: %w", err)
		}
		nestedSeen <- nestedObservation{body: body, elapsed: time.Since(start)}
		return json.RawMessage(`"mid-done-after-nested-timeout"`), nil
	})); err != nil {
		t.Fatal(err)
	}
	outer := NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "mid"))

	type execResult struct {
		body string
		err  error
	}
	done := make(chan execResult, 1)
	go func() {
		body, err := outer.Execute(outerCallerCtx(context.Background()),
			json.RawMessage(`{"tasks":[{"id":"m1","agent":"mid","prompt":"m1"}]}`))
		done <- execResult{body: body, err: err}
	}()

	// The nested run must resolve near its own 1s budget, long before the
	// leaf's 10s block and enormously before the 12h orchestration default.
	select {
	case obs := <-nestedSeen:
		if !strings.Contains(obs.body, "timed_out") {
			t.Fatalf("nested run with 1s budget did not resolve timed_out: %s", obs.body)
		}
		if obs.elapsed > 4*time.Second {
			t.Fatalf("PROBE FAILURE: nested 1s-budget dispatch took %s to resolve - explicit timeout_seconds not honored on the nested layer", obs.elapsed)
		}
	case <-time.After(8 * time.Second):
		dumpGoroutines(t)
		t.Fatalf("PROBE FAILURE: nested dispatch with 1s timeout never resolved within 8s\nledger:\n%s", ledgerStateDump(t, repo))
	}

	// The outer run must then complete normally: the nested timed_out result
	// is data, not poison.
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("outer dispatch returned transport error after nested timeout: %v", res.err)
		}
		if !strings.Contains(res.body, `"completed"`) {
			t.Fatalf("outer task must complete despite the nested timeout, got: %s", res.body)
		}
	case <-time.After(10 * time.Second):
		dumpGoroutines(t)
		t.Fatalf("PROBE FAILURE: outer dispatch never completed after the nested run timed out - nested timeout poisoned the outer Join\nledger:\n%s", ledgerStateDump(t, repo))
	}
	requireLedgerSettles(t, repo, 10*time.Second, "probe5")
}
