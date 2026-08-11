// Package cli provides orchestration tool implementations that bridge the
// model-facing tool set with the coordinator's async run model.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// Package-level coordinator singleton, lazily initialized on first use of any
// orchestration tool. runHandles maps runID → *coordinator.RunHandle for
// subsequent Inspect/Join/Cancel calls.
var (
	coordinators     sync.Map // *runtime.Dispatcher → coordinator.Coordinator
	coordinatorRepos sync.Map // *runtime.Dispatcher → ledger.LedgerRepository
	runHandles       sync.Map // runID → orchestrationHandle
)

var defaultOrchestrationRepo ledger.LedgerRepository = ledger.NewMemoryLedgerRepository()

// activeSessionCaller is the chat session's identity, recorded once per process.
//
// Orchestration control initiated from a CLI surface - a slash command or a
// dashboard key - has no dispatcher-stamped caller in its context. But a handle
// it registers must stay controllable by the model tools that run later, and
// `Dispatcher.Invoke` stamps `req.SessionID`/`req.Role` onto every tool call
// (`internal/runtime/dispatcher.go`). For the main session those come from
// `chat.Session.SessionID` with an empty role, so that is the pair a
// CLI-initiated handle must carry.
//
// Minting a fresh ephemeral principal instead - which the legacy compatibility
// path in orchestrate.go does *deliberately*, precisely so those callers
// "cannot later control a handle" - registers the run under an identity no
// session holds, making it permanently uninspectable and uncancellable.
var activeSessionCaller atomic.Pointer[runtime.Caller]

// setActiveSessionCaller records the chat session's identity for CLI-initiated
// orchestration control.
func setActiveSessionCaller(caller runtime.Caller) {
	activeSessionCaller.Store(&caller)
}

// sessionCallerContext attaches the chat session's caller when ctx carries none.
// A context that already has a caller (any dispatcher-stamped tool call) is
// returned unchanged, so this never overrides a real identity.
func sessionCallerContext(ctx context.Context) context.Context {
	if caller, ok := runtime.CallerFrom(ctx); ok && caller.SessionID != "" {
		return ctx
	}
	if caller := activeSessionCaller.Load(); caller != nil && caller.SessionID != "" {
		return runtime.ContextWithCaller(ctx, *caller)
	}
	return ctx
}

type orchestrationHandle struct {
	coord      coordinator.Coordinator
	handle     *coordinator.RunHandle
	repo       ledger.LedgerRepository
	dispatcher *runtime.Dispatcher
	principal  orchestrationPrincipal
	retention  time.Duration
}

// orchestrationPrincipal is distinct from subagents.Task.Owner, which names a
// parent task rather than the caller authorized to control a run.
type orchestrationPrincipal struct {
	sessionID string
	role      string
}

func principalFromContext(ctx context.Context) (orchestrationPrincipal, bool) {
	caller, ok := runtime.CallerFrom(ctx)
	if !ok || caller.SessionID == "" {
		return orchestrationPrincipal{}, false
	}
	return orchestrationPrincipal{sessionID: caller.SessionID, role: caller.Role}, true
}

func effectiveOrchestrationRepo(repo ledger.LedgerRepository) ledger.LedgerRepository {
	if repo == nil {
		return defaultOrchestrationRepo
	}
	return repo
}

// Orchestration default constants.
//
// defaultMaxTokens is intentionally 0: when no explicit [subagents] max_tokens
// is configured, the subagent loop passes nil to the provider, letting the
// model use its own default response length. A hardcoded 4096 cap truncated
// comprehensive subagent reports mid-sentence (finish_reason="length").
const (
	defaultMaxTokens          = 0
	defaultJoinRunTimeout     = time.Duration(config.DefaultOrchestrationTimeoutSec+dispatchOrchestrationSlackSec) * time.Second
	orchestrationPollInterval = 25 * time.Millisecond
	defaultToolOwner          = "mivia"
	defaultHandleRetention    = 10 * time.Minute
)

// repositoriesMatch compares two LedgerRepository instances for equality.
// Uses reflect-based comparison because LedgerRepository is an interface whose
// concrete types may not all be pointer-typed; replacing with == would require
// auditing every implementation. Defer simplification until concrete types are verified.
func repositoriesMatch(a, b ledger.LedgerRepository) bool {
	a, b = effectiveOrchestrationRepo(a), effectiveOrchestrationRepo(b)
	if reflect.TypeOf(a) != reflect.TypeOf(b) || a == nil {
		return a == nil && b == nil
	}
	value := reflect.ValueOf(a)
	return value.Type().Comparable() && value.Interface() == reflect.ValueOf(b).Interface()
}

func storeOrchestrationHandle(runID string, record *orchestrationHandle) {
	if _, loaded := runHandles.LoadOrStore(runID, record); loaded {
		// An idempotency retry must retain the original caller's ownership.
		return
	}
	if record.retention <= 0 {
		record.retention = defaultHandleRetention
	}
	closed := make(chan struct{})
	record.dispatcher.OnClose(func() {
		// Dispatcher close is not session end: surface rebuilds (tool
		// admission, /agent, /model) replace the dispatcher while the session
		// continues. Unregister only a completed run now; an active run stays
		// inspectable under its session principal and the retention timer
		// cleans it up once it completes. A rebuild used to orphan every
		// in-flight background run as "unknown run_id".
		select {
		case <-record.handle.Done():
		default:
			return
		}
		close(closed)
		if current, ok := runHandles.Load(runID); ok && current == record {
			runHandles.Delete(runID)
		}
	})
	go func() {
		select {
		case <-record.handle.Done():
		case <-closed:
			return
		}
		timer := time.NewTimer(record.retention)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-closed:
			return
		}
		if current, ok := runHandles.Load(runID); ok && current == record {
			runHandles.Delete(runID)
		}
	}()
}

func activeOrchestrationForSession(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	active := false
	runHandles.Range(func(_, value any) bool {
		record, ok := value.(*orchestrationHandle)
		if !ok || record.principal.sessionID != sessionID {
			return true
		}
		select {
		case <-record.handle.Done():
		default:
			active = true
		}
		return !active
	})
	return active
}

// errOrchestrationSwitchActive is the guard's refusal as a value, because
// /effort rewrites it for a surface where "model switching" names a command the
// user did not type. Matching that rewrite on the text would go quiet the first
// time someone copy-edits this sentence, and the notice would silently revert.
var errOrchestrationSwitchActive = errors.New("model switching is unavailable while orchestration is active")

func orchestrationSwitchGuard(sessionID string) func() error {
	return func() error {
		if activeOrchestrationForSession(sessionID) {
			return errOrchestrationSwitchActive
		}
		return nil
	}
}

// orchestrationHandleAccessible reports whether the caller in ctx may control
// record. The dispatcher instance is deliberately NOT compared: surface
// rebuilds (tool admission, /agent, /model, resume) replace the dispatcher
// while the session continues, and an equality check would orphan every
// in-flight run registered under the replaced instance. The principal (session
// + role) and the repository bind the handle to its session and workspace;
// INV-AG-9 keeps the unknown/inaccessible errors indistinguishable.
func orchestrationHandleAccessible(ctx context.Context, record *orchestrationHandle, _ *runtime.Dispatcher, repo ledger.LedgerRepository) bool {
	principal, ok := principalFromContext(ctx)
	return ok && record != nil && repositoriesMatch(record.repo, repo) && record.principal == principal
}

func orchestrationHandleRetention(cfg config.SubagentConfig) time.Duration {
	if cfg.HandleRetentionSeconds > 0 {
		return time.Duration(cfg.HandleRetentionSeconds) * time.Second
	}
	return defaultHandleRetention
}

// openDurableLedgerRepo opens a SQLite-backed ledger repository when configured,
// runs startup recovery, reports interrupted runs, and returns the owned store
// (if any) so the caller can close it on shutdown. On any open failure it falls
// back to the in-memory default repo and writes a warning to w; it never returns
// an error for an open failure.
func openDurableLedgerRepo(cfg config.SubagentConfig, w io.Writer) (repo ledger.LedgerRepository, ownedStore *ledger.StorageLedgerRepository) {
	repo = defaultOrchestrationRepo
	if cfg.StoreBackend != "sqlite" {
		return repo, nil
	}
	sqlStore, err := storage.OpenSQLite(cfg.StorePath)
	if err != nil {
		// %s with explicit quotes, not %q: %q Go-quotes the path, doubling
		// Windows backslashes, so a warning that names the configured path
		// would no longer contain it verbatim.
		fmt.Fprintf(w, "warning: failed to open SQLite store \"%s\": %v; falling back to memory backend\n", cfg.StorePath, err)
		return repo, nil
	}
	storageRepo := ledger.NewStorageLedgerRepository(sqlStore)
	// Startup recovery: catch the projection up and report what a previous
	// process left unfinished. Recover mutates no run status - see its doc.
	recovered, recErr := storageRepo.Recover(context.Background())
	reportInterruptedRuns(w, recovered, recErr)
	return storageRepo, storageRepo
}

// openSharedSQLite opens the single caller-owned database used when a chat
// session shares context checkpoints with the ledger. The caller supplies the
// returned pointer to NewSessionDispatcher and closes it after that
// dispatcher, so adapters cannot accidentally create or close a second DB.
func openSharedSQLite(cfg config.SubagentConfig, w io.Writer) (*storage.SQLite, error) {
	if cfg.StoreBackend != "sqlite" {
		return nil, nil
	}
	store, err := storage.OpenSQLite(cfg.StorePath)
	if err != nil {
		if w != nil {
			fmt.Fprintf(w, "warning: failed to open shared SQLite store \"%s\": %v\n", cfg.StorePath, err)
		}
		return nil, err
	}
	return store, nil
}

// closeSharedSQLite is the explicit owner boundary. SQLite's Close is
// idempotent, while the function keeps shutdown ownership visible at the CLI
// layer and makes the borrowed ledger adapter's contract testable.
func closeSharedSQLite(store *storage.SQLite) error {
	if store == nil {
		return nil
	}
	return store.Close()
}

// poolLimitsFromConfig maps the [subagents] config contract onto pool
// limits. Config zero means unlimited (see resolveSubagentConfig and the
// mivia.toml comments); the pool primitive would otherwise substitute its
// safe defaults for zero and silently cap agent-planned DAGs.
func poolLimitsFromConfig(cfg config.SubagentConfig) (maxDepth, maxFanout int) {
	maxDepth, maxFanout = cfg.MaxDepth, cfg.MaxFanout
	if maxDepth == 0 {
		maxDepth = subagents.Unlimited
	}
	if maxFanout == 0 {
		maxFanout = subagents.Unlimited
	}
	return maxDepth, maxFanout
}

// initCoordinator lazily creates the Coordinator singleton with an in-memory
// or durable ledger repository and a subagent pool backed by the given dispatcher.
// Safe for concurrent calls; only the first invocation initialises the
// singleton.  Subsequent calls are no-ops.
func initCoordinator(d *runtime.Dispatcher, cfg config.SubagentConfig, repos ...ledger.LedgerRepository) coordinator.Coordinator {
	if existing, ok := coordinators.Load(d); ok {
		return existing.(coordinator.Coordinator)
	}
	poolDepth, poolFanout := poolLimitsFromConfig(cfg)
	repo := defaultOrchestrationRepo
	// ownedStore is the store THIS call opened, and the only one the close hook
	// below may close. A caller-supplied repository outlives the dispatcher -
	// a chat session hands the same repository to every surface rebuild, and
	// publication closes the dispatcher it replaced. Deciding by ownership
	// rather than by recognising a concrete type is also what lets a durable
	// repository keep its optional interfaces (Recover) all the way to the
	// coordinator.
	var ownedStore *ledger.StorageLedgerRepository
	if len(repos) > 0 {
		repo = effectiveOrchestrationRepo(repos[0])
	} else if cfg.StoreBackend == "sqlite" {
		// Shared open+recover+fallback. Close is wired below when this init
		// owns the coordinator for d.
		repo, ownedStore = openDurableLedgerRepo(cfg, os.Stderr)
	}
	pool := subagents.New(d, subagents.Policy{
		Workers:   cfg.MaxWorkers,
		MaxDepth:  poolDepth,
		MaxFanout: poolFanout,
		MaxBudget: cfg.DefaultBudget,
		Timeout:   time.Duration(cfg.DefaultTimeout) * time.Second,
	})
	c := coordinator.New(repo, pool).WithRetryPolicy(coordinator.NoRetry)
	// Wire [subagents.messaging] body/mailbox budgets (plan 53).
	c = c.WithMessagingLimits(cfg.Messaging.MaxBodyBytes, cfg.Messaging.MailboxCapacity)
	actual, _ := coordinators.LoadOrStore(d, c)
	coordinatorRepos.Store(d, repo)
	if actual == c {
		d.OnClose(func() {
			if ownedStore != nil {
				_ = ownedStore.Close()
			}
			coordinators.Delete(d)
			coordinatorRepos.Delete(d)
		})
	}
	return actual.(coordinator.Coordinator)
}

func orchestrationRepoForDispatcher(d *runtime.Dispatcher) ledger.LedgerRepository {
	if d == nil {
		return nil
	}
	if repo, ok := coordinatorRepos.Load(d); ok {
		return repo.(ledger.LedgerRepository)
	}
	return nil
}
