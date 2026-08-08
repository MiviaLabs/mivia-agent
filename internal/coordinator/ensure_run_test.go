package coordinator

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

type ensureObservedAuthority struct {
	caller     runtime.Caller
	permission string
}

type ensureAuthorityValidator struct {
	want subagents.Task
	seen chan ensureObservedAuthority
}

type ensureFailingRepository struct {
	ledger.LedgerRepository
	getErr, listErr, clearErr, claimErr, createTaskErr error
}

func (r *ensureFailingRepository) GetRunByIdempotencyKey(ctx context.Context, key string) (ledger.RunSnapshot, error) {
	if r.getErr != nil {
		return ledger.RunSnapshot{}, r.getErr
	}
	return r.LedgerRepository.GetRunByIdempotencyKey(ctx, key)
}

func (r *ensureFailingRepository) ListTasks(ctx context.Context, runID string) ([]ledger.TaskSnapshot, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.LedgerRepository.ListTasks(ctx, runID)
}

func (r *ensureFailingRepository) ClearRunClaim(ctx context.Context, runID string) error {
	if r.clearErr != nil {
		return r.clearErr
	}
	return r.LedgerRepository.ClearRunClaim(ctx, runID)
}

func (r *ensureFailingRepository) ClaimRun(ctx context.Context, runID, holder string) error {
	if r.claimErr != nil {
		return r.claimErr
	}
	return r.LedgerRepository.ClaimRun(ctx, runID, holder)
}

func (r *ensureFailingRepository) CreateTask(ctx context.Context, snap ledger.TaskSnapshot) error {
	if r.createTaskErr != nil {
		return r.createTaskErr
	}
	return r.LedgerRepository.CreateTask(ctx, snap)
}

func (h ensureAuthorityValidator) ValidateRequest(req runtime.Request) error {
	if req.SessionID != h.want.SessionID || req.TurnID != h.want.TurnID || req.Role != h.want.Role || req.Permission != h.want.Permission {
		return errors.New("live request authority is absent")
	}
	return nil
}

func (h ensureAuthorityValidator) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	caller, _ := runtime.CallerFrom(ctx)
	h.seen <- ensureObservedAuthority{caller: caller, permission: req.Permission}
	return json.RawMessage(`{"ok":true}`), nil
}

func TestCoordinatorExposesEnsureRun(t *testing.T) {
	if _, ok := reflect.TypeOf((*Coordinator)(nil)).Elem().MethodByName("EnsureRun"); !ok {
		t.Fatal("Coordinator has no EnsureRun method")
	}
}

func TestCoordinatorExposesSingleTaskEnsureOperations(t *testing.T) {
	typ := reflect.TypeOf((*Coordinator)(nil)).Elem()
	for _, name := range []string{"EnsureSingleTaskRun", "EnsureTerminalSingleTaskRun"} {
		if _, ok := typ.MethodByName(name); !ok {
			t.Fatalf("Coordinator has no %s method", name)
		}
	}
}

func TestEnsureRequestFingerprintIncludesNonDefaultPolicy(t *testing.T) {
	task := subagents.Task{ID: "task", Name: "worker"}
	base, err := requestFingerprintWithPolicy([]subagents.Task{task}, ledger.RunPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	panel, err := requestFingerprintWithPolicy([]subagents.Task{task}, ledger.RunPolicy{NoRetry: true, FailInterrupted: true})
	if err != nil {
		t.Fatal(err)
	}
	if base == panel {
		t.Fatal("non-default policy must change the request fingerprint")
	}
	withRetry, err := requestFingerprintWithPolicy([]subagents.Task{task}, ledger.RunPolicy{RetryMaxRetries: 2, RetryBaseBackoff: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if withRetry == base || withRetry == panel {
		t.Fatal("retry policy must change the request fingerprint")
	}
}

func TestEnsureRestoresStoredPolicyWhenRequestOmitsIt(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	first := newIdempotencyCoordinator(repo)
	first.WithRetryPolicy(RetryPolicy{MaxRetries: 1, BaseBackoff: time.Millisecond})
	req := EnsureRunRequest{RunID: NewRunID(), Tasks: []subagents.Task{idempotencyTask()}, IdempotencyKey: "policy-restore"}
	h, err := first.EnsureRun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	second := newIdempotencyCoordinator(repo)
	second.WithRetryPolicy(RetryPolicy{MaxRetries: 9, BaseBackoff: time.Second})
	restored, err := second.EnsureRun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if policy := restored.policy(); policy.MaxRetries != 1 || policy.BaseBackoff != time.Millisecond {
		t.Fatalf("restored policy = %+v", policy)
	}
}

func TestEnsureTerminalSingleTaskRunNeverDispatches(t *testing.T) {
	var calls atomic.Int32
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", invoker(func(context.Context, runtime.Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"unexpected":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	coord := New(ledger.NewMemoryLedgerRepository(), subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	task := subagents.Task{ID: "task-tombstone", Name: "worker", Input: json.RawMessage(`"work"`)}
	h, err := coord.EnsureTerminalSingleTaskRun(context.Background(), EnsureRunRequest{RunID: NewRunID(), Tasks: []subagents.Task{task}, IdempotencyKey: "tombstone"}, ledger.TaskStatusCanceled)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coord.Join(context.Background(), h)
	if err != nil || result.Snapshot.Status != ledger.RunStatusCanceled {
		t.Fatalf("join = %+v, %v", result, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", calls.Load())
	}
}

func TestNewRunIDContains128RandomBits(t *testing.T) {
	first, second := NewRunID(), NewRunID()
	if first == second {
		t.Fatal("NewRunID returned a duplicate")
	}
	for _, runID := range []string{first, second} {
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(runID[len("run-"):])
		if err != nil || len(decoded) != 16 {
			t.Fatalf("NewRunID() = %q, want a 128-bit token", runID)
		}
	}
}

func TestEnsureRunHelperBoundaries(t *testing.T) {
	valid := NewRunID()
	if !validRunID(valid) {
		t.Fatalf("validRunID rejects %q", valid)
	}
	for _, runID := range []string{"", "other-ABC", strings.ToLower(valid), valid + "A"} {
		if validRunID(runID) {
			t.Fatalf("validRunID accepts %q", runID)
		}
	}
	tasks := []ledger.TaskSnapshot{
		{TaskID: "empty"},
		{TaskID: "ordered", Attempts: []ledger.AttemptSnapshot{{AttemptID: "two", AttemptNum: 2}, {AttemptID: "one", AttemptNum: 1}, {AttemptID: "three", AttemptNum: 3}}},
	}
	got := latestAttempts(tasks)
	if len(got) != 1 || got["ordered"] != "three" {
		t.Fatalf("latestAttempts = %+v", got)
	}
}

func TestEnsureRunCreatesExactRequestedIDAndPreservesCaller(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	seen := make(chan runtime.Caller, 1)
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		caller, _ := runtime.CallerFrom(ctx)
		seen <- caller
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	runID := NewRunID()
	task := subagents.Task{ID: "task-1", Name: "worker", Input: json.RawMessage(`"work"`), SessionID: "session-a", TurnID: "turn-a", Role: "role-a"}
	h, err := c.EnsureRun(callerContext("session-a", "turn-a", "role-a"), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "workflow-step"})
	if err != nil {
		t.Fatal(err)
	}
	if h.RunID() != runID {
		t.Fatalf("run ID = %q, want %q", h.RunID(), runID)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if caller := <-seen; caller.SessionID != "session-a" || caller.TurnID != "turn-a" || caller.Role != "role-a" {
		t.Fatalf("caller = %+v, want preserved task caller", caller)
	}
}

func TestEnsureRunValidatesAdmissionBeforeMutation(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	c := newIdempotencyCoordinator(repo)
	task := idempotencyTask()
	for _, tc := range []struct {
		name string
		req  EnsureRunRequest
	}{
		{"empty key", EnsureRunRequest{RunID: NewRunID(), Tasks: []subagents.Task{task}}},
		{"invalid run ID", EnsureRunRequest{RunID: "run-small", Tasks: []subagents.Task{task}, IdempotencyKey: "key"}},
		{"invalid tasks", EnsureRunRequest{RunID: NewRunID(), IdempotencyKey: "key"}},
		{"empty task ID", EnsureRunRequest{RunID: NewRunID(), Tasks: []subagents.Task{func() subagents.Task { task := idempotencyTask(); task.ID = ""; return task }()}, IdempotencyKey: "key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.EnsureRun(context.Background(), tc.req); err == nil {
				t.Fatal("EnsureRun succeeded")
			}
		})
	}
	runs, err := repo.ListRuns(context.Background())
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs after rejected admission = %d, err = %v", len(runs), err)
	}
}

func TestEnsureRunSurfacesRepositoryFailures(t *testing.T) {
	want := errors.New("injected repository failure")
	task := idempotencyTask()
	request := func(runID string, force bool) EnsureRunRequest {
		return EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step", ForceResume: force}
	}
	t.Run("lookup", func(t *testing.T) {
		repo := &ensureFailingRepository{LedgerRepository: ledger.NewMemoryLedgerRepository(), getErr: want}
		_, err := newIdempotencyCoordinator(repo).EnsureRun(context.Background(), request(NewRunID(), false))
		if !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
	})
	for _, tc := range []struct {
		name     string
		admitted int
		force    bool
		set      func(*ensureFailingRepository, string)
	}{
		{"list tasks", 1, false, func(r *ensureFailingRepository, _ string) { r.listErr = want }},
		// Clear is only attempted when a claim is actually held (probe-first
		// discipline); seed a stale claim so the clear path is exercised.
		{"clear full claim", 1, true, func(r *ensureFailingRepository, runID string) {
			_ = r.ClaimRun(context.Background(), runID, "stale-holder")
			r.clearErr = want
		}},
		{"clear empty claim", 0, true, func(r *ensureFailingRepository, runID string) {
			_ = r.ClaimRun(context.Background(), runID, "stale-holder")
			r.clearErr = want
		}},
		{"claim empty run", 0, false, func(r *ensureFailingRepository, _ string) { r.claimErr = want }},
		{"repair task", 0, false, func(r *ensureFailingRepository, _ string) { r.createTaskErr = want }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := ledger.NewMemoryLedgerRepository()
			runID := NewRunID()
			seedEnsuredRun(t, base, context.Background(), runID, "step", []subagents.Task{task}, tc.admitted)
			repo := &ensureFailingRepository{LedgerRepository: base}
			tc.set(repo, runID)
			_, err := newIdempotencyCoordinator(repo).EnsureRun(context.Background(), request(runID, tc.force))
			if !errors.Is(err, want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEnsureRunRejectsUnfingerprintableTask(t *testing.T) {
	task := idempotencyTask()
	task.OutputSchema = map[string]any{"limit": math.Inf(1)}
	_, err := newIdempotencyCoordinator(ledger.NewMemoryLedgerRepository()).EnsureRun(context.Background(), EnsureRunRequest{
		RunID: NewRunID(), Tasks: []subagents.Task{task}, IdempotencyKey: "step",
	})
	if err == nil || !strings.Contains(err.Error(), "fingerprint request") {
		t.Fatalf("error = %v, want fingerprint failure", err)
	}
}

func TestEnsureRunReturnsRegisteredHandles(t *testing.T) {
	task := idempotencyTask()
	repo := ledger.NewMemoryLedgerRepository()
	c := newIdempotencyCoordinator(repo).(*coordinator)
	runID := NewRunID()
	req := EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"}
	h, err := c.EnsureRun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.EnsureRun(context.Background(), req)
	if err != nil || again != h {
		t.Fatalf("same request handle = %p, error = %v; want %p", again, err, h)
	}
	conflict := req
	conflict.RunID = NewRunID()
	if _, err := c.EnsureRun(context.Background(), conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	otherRun := NewRunID()
	seedEnsuredRun(t, repo, context.Background(), otherRun, "other-step", []subagents.Task{task}, 1)
	fingerprint, _ := requestFingerprint([]subagents.Task{task})
	live := c.newRunHandle(otherRun, "", nil, fingerprint, false)
	defer live.cancel()
	got, err := c.EnsureRun(context.Background(), EnsureRunRequest{RunID: otherRun, Tasks: []subagents.Task{task}, IdempotencyKey: "other-step"})
	if err != nil || got != live {
		t.Fatalf("run handle = %p, error = %v; want %p", got, err, live)
	}
}

func TestEnsureRunResumesFullNonterminalRun(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	task := idempotencyTask()
	ctx := callerContext("session-a", "turn-a", "role-a")
	runID := NewRunID()
	seedEnsuredRun(t, repo, ctx, runID, "step", []subagents.Task{task}, 1)
	c := newIdempotencyCoordinator(repo)
	h, err := c.EnsureRun(ctx, EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != ledger.RunStatusCompleted {
		t.Fatalf("status = %q, want completed", result.Snapshot.Status)
	}
}

func TestEnsureRunResumeUsesLiveRequestAuthority(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	task := idempotencyTask()
	task.SessionID = "live-session"
	task.TurnID = "live-turn"
	task.Role = "live-role"
	task.Permission = "live-permission"
	runID := NewRunID()
	seedEnsuredRun(t, repo, context.Background(), runID, "step", []subagents.Task{task}, 1)

	seen := make(chan ensureObservedAuthority, 1)
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", ensureAuthorityValidator{want: task, seen: seen}); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	got := <-seen
	if got.caller.SessionID != task.SessionID || got.caller.TurnID != task.TurnID || got.caller.Role != task.Role || got.permission != task.Permission {
		t.Fatalf("authority = caller %+v permission %q, want request authority", got.caller, got.permission)
	}
}

func TestEnsureRunRepairsZeroTaskAdmission(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	task := idempotencyTask()
	ctx := context.Background()
	runID := NewRunID()
	seedEnsuredRun(t, repo, ctx, runID, "step", []subagents.Task{task}, 0)
	c := newIdempotencyCoordinator(repo)
	h, err := c.EnsureRun(ctx, EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	tasks, err := repo.ListTasks(ctx, runID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %d, err = %v", len(tasks), err)
	}
}

func TestEnsureRunEmptyAdmissionClaimPolicy(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "held", true: "forced"}[force], func(t *testing.T) {
			repo := ledger.NewMemoryLedgerRepository()
			task := idempotencyTask()
			runID := NewRunID()
			seedEnsuredRun(t, repo, context.Background(), runID, "step", []subagents.Task{task}, 0)
			if err := repo.ClaimRun(context.Background(), runID, "other"); err != nil {
				t.Fatal(err)
			}
			c := newIdempotencyCoordinator(repo)
			h, err := c.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step", ForceResume: force})
			if !force {
				if !errors.Is(err, ErrRunHeldByAnotherExecutor) {
					t.Fatalf("error = %v, want ErrRunHeldByAnotherExecutor", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Join(context.Background(), h); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnsureRunFailsClosedOnPartialTaskAdmission(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	first := idempotencyTask()
	second := first
	second.ID = "task-2"
	runID := NewRunID()
	seedEnsuredRun(t, repo, context.Background(), runID, "step", []subagents.Task{first, second}, 1)
	c := newIdempotencyCoordinator(repo)
	_, err := c.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{first, second}, IdempotencyKey: "step"})
	if err == nil {
		t.Fatal("EnsureRun accepted partial task admission")
	}
	snap, getErr := repo.GetRun(context.Background(), runID)
	if getErr != nil || snap.Status != ledger.RunStatusQueued {
		t.Fatalf("run changed after rejection: status = %q, err = %v", snap.Status, getErr)
	}
}

func TestEnsureRunRefusesHeldRunWithoutForce(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	task := idempotencyTask()
	runID := NewRunID()
	seedEnsuredRun(t, repo, context.Background(), runID, "step", []subagents.Task{task}, 1)
	if err := repo.ClaimRun(context.Background(), runID, "other"); err != nil {
		t.Fatal(err)
	}
	c := newIdempotencyCoordinator(repo)
	_, err := c.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"})
	if !errors.Is(err, ErrRunHeldByAnotherExecutor) {
		t.Fatalf("error = %v, want ErrRunHeldByAnotherExecutor", err)
	}
	if err := repo.ReleaseRun(context.Background(), runID, "other"); err != nil {
		t.Fatalf("held claim changed: %v", err)
	}
}

func TestEnsureRunForceClearsClaimOnlyAfterTupleValidation(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	task := idempotencyTask()
	runID := NewRunID()
	seedEnsuredRun(t, repo, context.Background(), runID, "step", []subagents.Task{task}, 1)
	if err := repo.ClaimRun(context.Background(), runID, "other"); err != nil {
		t.Fatal(err)
	}
	c := newIdempotencyCoordinator(repo)
	if _, err := c.EnsureRun(context.Background(), EnsureRunRequest{RunID: NewRunID(), Tasks: []subagents.Task{task}, IdempotencyKey: "step", ForceResume: true}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("mismatched run ID error = %v, want ErrIdempotencyConflict", err)
	}
	changed := task
	changed.Input = json.RawMessage(`"changed"`)
	if _, err := c.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{changed}, IdempotencyKey: "step", ForceResume: true}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("mismatched tuple error = %v, want ErrIdempotencyConflict", err)
	}
	if err := repo.ReleaseRun(context.Background(), runID, "other"); err != nil {
		t.Fatalf("claim changed before tuple validation: %v", err)
	}
	if err := repo.ClaimRun(context.Background(), runID, "other"); err != nil {
		t.Fatal(err)
	}
	h, err := c.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step", ForceResume: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRunUsesCallerScopedIdempotencyKey(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	task := idempotencyTask()
	runID := NewRunID()
	owner := callerContext("session-a", "turn-a", "role-a")
	seedEnsuredRun(t, repo, owner, runID, "step", []subagents.Task{task}, 1)
	c := newIdempotencyCoordinator(repo)
	other := callerContext("session-b", "turn-b", "role-a")
	if _, err := c.EnsureRun(other, EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"}); err == nil {
		t.Fatal("EnsureRun crossed caller key scope")
	}
	h, err := c.EnsureRun(owner, EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRunValidatesStoredTasksBeforeForceOrTerminalReturn(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "force", true: "terminal"}[terminal], func(t *testing.T) {
			repo := ledger.NewMemoryLedgerRepository()
			task := idempotencyTask()
			runID := NewRunID()
			seedEnsuredRun(t, repo, context.Background(), runID, "step", []subagents.Task{task}, 1)
			snap, err := repo.GetTask(context.Background(), runID, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			snap.Input = json.RawMessage(`"tampered"`)
			if terminal {
				snap.Status = string(ledger.TaskStatusCompleted)
				snap.Attempts[0].Status = string(ledger.TaskStatusCompleted)
			}
			// Replace the in-memory fixture before the coordinator sees it.
			if err := repo.DeleteRun(context.Background(), runID); err != nil {
				t.Fatal(err)
			}
			fingerprint, _ := requestFingerprint([]subagents.Task{task})
			status := ledger.RunStatusCreated
			if terminal {
				status = ledger.RunStatusCompleted
			}
			if err := repo.CreateRun(context.Background(), "step", ledger.RunSnapshot{RunID: runID, Status: status, RequestFingerprint: fingerprint}); err != nil {
				t.Fatal(err)
			}
			if err := repo.CreateTask(context.Background(), snap); err != nil {
				t.Fatal(err)
			}
			if !terminal {
				if err := repo.ClaimRun(context.Background(), runID, "other"); err != nil {
					t.Fatal(err)
				}
			}
			c := newIdempotencyCoordinator(repo)
			_, err = c.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step", ForceResume: true})
			if err == nil {
				t.Fatal("EnsureRun accepted altered stored work")
			}
			if !terminal {
				if err := repo.ReleaseRun(context.Background(), runID, "other"); err != nil {
					t.Fatalf("claim changed before stored task validation: %v", err)
				}
			}
		})
	}
}

func TestEnsureRunReturnsTerminalExactTuple(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	task := idempotencyTask()
	runID := NewRunID()
	creator := newIdempotencyCoordinator(repo)
	h, err := creator.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	recovered := newIdempotencyCoordinator(repo)
	h, err = recovered.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recovered.Join(context.Background(), h)
	if err != nil || result.Snapshot.Status != ledger.RunStatusCompleted {
		t.Fatalf("terminal result status = %q, err = %v", result.Snapshot.Status, err)
	}
}

func seedEnsuredRun(t *testing.T, repo ledger.LedgerRepository, ctx context.Context, runID, key string, tasks []subagents.Task, admitted int) {
	t.Helper()
	policy := policyWithRetry(ledger.RunPolicy{}, DefaultRetryPolicy)
	fingerprint, err := requestFingerprintWithPolicy(tasks, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, scopedKey(ctx, key), ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusCreated, RequestFingerprint: fingerprint, CreatedAt: time.Now(), Policy: policy}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < admitted; i++ {
		task := tasks[i]
		snap := ledger.TaskSnapshot{RunID: runID, TaskID: task.ID, DisplayName: task.Name, HandlerName: task.Name,
			AgentName: task.AgentName, AgentDigest: task.AgentDigest, Skill: task.Skill, ProviderName: task.ProviderName,
			Model: task.Model, Scope: task.Scope, OutputSchema: task.OutputSchema, InputSchema: task.InputSchema, Input: task.Input, Timeout: task.Timeout,
			Budget: task.Budget, Depth: task.Depth, Status: string(ledger.TaskStatusQueued), DependsOn: task.DependsOn,
			CreatedAt: time.Now(), Version: 1, Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-seed-" + task.ID, TaskID: task.ID, RunID: runID, AttemptNum: 1, Status: string(ledger.TaskStatusQueued)}}}
		if err := repo.CreateTask(ctx, snap); err != nil {
			t.Fatal(err)
		}
	}
}
