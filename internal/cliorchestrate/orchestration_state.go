package cliorchestrate

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
// orchestration tool. runHandles maps runID → *orchestrationHandle for
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

// SetActiveSessionCaller records the chat session's identity for CLI-initiated
// orchestration control.
func SetActiveSessionCaller(caller runtime.Caller) {
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

// GetCoordinator returns the coordinator for this run handle. See RunAccess.
func (h *orchestrationHandle) GetCoordinator() coordinator.Coordinator { return h.coord }

// GetHandle returns the run handle. See RunAccess.
func (h *orchestrationHandle) GetHandle() *coordinator.RunHandle { return h.handle }

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

// EffectiveOrchestrationRepo returns repo if non-nil, otherwise the default.
func EffectiveOrchestrationRepo(repo ledger.LedgerRepository) ledger.LedgerRepository {
	if repo == nil {
		return defaultOrchestrationRepo
	}
	return repo
}

// Orchestration default constants.
//
// DefaultMaxTokens is intentionally 0: when no explicit [subagents] max_tokens
// is configured, the subagent loop passes nil to the provider, letting the
// model use its own default response length. A hardcoded 4096 cap truncated
// comprehensive subagent reports mid-sentence (finish_reason="length").
const (
	DefaultMaxTokens          = 0
	defaultJoinRunTimeout     = time.Duration(config.DefaultOrchestrationTimeoutSec+DispatchOrchestrationSlackSec) * time.Second
	orchestrationPollInterval = 25 * time.Millisecond
	// DefaultToolOwner is the default owner for orchestration tasks.
	DefaultToolOwner       = "mivia"
	defaultHandleRetention = 10 * time.Minute
)

// repositoriesMatch compares two LedgerRepository instances for equality.
// Uses reflect-based comparison because LedgerRepository is an interface whose
// concrete types may not all be pointer-typed; replacing with == would require
// auditing every implementation. Defer simplification until concrete types are verified.
func repositoriesMatch(a, b ledger.LedgerRepository) bool {
	a, b = EffectiveOrchestrationRepo(a), EffectiveOrchestrationRepo(b)
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

// ErrOrchestrationSwitchActive is OrchestrationSwitchGuard's refusal as a
// value, because internal/legacytui's /effort rewrites it for a surface where
// "model switching" names a command the user did not type. Matching that
// rewrite on the text would go quiet the first time someone copy-edits this
// sentence, and the notice would silently revert.
var ErrOrchestrationSwitchActive = errors.New("model switching is unavailable while orchestration is active")

// OrchestrationSwitchGuard returns a guard function that refuses model
// switching while an orchestration run is active for sessionID.
func OrchestrationSwitchGuard(sessionID string) func() error {
	return func() error {
		if activeOrchestrationForSession(sessionID) {
			return ErrOrchestrationSwitchActive
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
		return config.SaturatingSeconds(cfg.HandleRetentionSeconds)
	}
	return defaultHandleRetention
}

// OpenDurableLedgerRepo opens a SQLite-backed ledger repository when
// configured, runs startup recovery, reports interrupted runs, and returns the
// owned store (if any) so the caller can close it on shutdown. On any open
// failure it falls back to the in-memory default repo and writes a warning to
// w; it never returns an error for an open failure.
func OpenDurableLedgerRepo(cfg config.SubagentConfig, w io.Writer) (repo ledger.LedgerRepository, ownedStore *ledger.StorageLedgerRepository) {
	repo = defaultOrchestrationRepo
	if cfg.StoreBackend != "sqlite" {
		return repo, nil
	}
	// The config-layer default store (~/.cache tier, or its TempDir fallback)
	// is mivia-owned, never operator-managed: open it hardened. An operator
	// configured store_path keeps its modes.
	sqlStore, err := storage.OpenSQLiteWithOptions(cfg.StorePath, storage.Options{Harden: config.IsDefaultOrchestrationStorePath(cfg.StorePath)})
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
	ReportInterruptedRuns(w, recovered, recErr)
	return storageRepo, storageRepo
}

// OpenSharedSQLite opens the single caller-owned database used when a chat
// session shares context checkpoints with the ledger. The caller supplies the
// returned pointer to NewSessionDispatcher and closes it after that
// dispatcher, so adapters cannot accidentally create or close a second DB.
func OpenSharedSQLite(cfg config.SubagentConfig, w io.Writer) (*storage.SQLite, error) {
	if cfg.StoreBackend != "sqlite" {
		return nil, nil
	}
	// Same gate as OpenDurableLedgerRepo: harden the config-layer default
	// tier only; an operator store_path keeps its modes.
	store, err := storage.OpenSQLiteWithOptions(cfg.StorePath, storage.Options{Harden: config.IsDefaultOrchestrationStorePath(cfg.StorePath)})
	if err != nil {
		if w != nil {
			fmt.Fprintf(w, "warning: failed to open shared SQLite store \"%s\": %v\n", cfg.StorePath, err)
		}
		return nil, err
	}
	return store, nil
}

// CloseSharedSQLite is the explicit owner boundary. SQLite's Close is
// idempotent, while the function keeps shutdown ownership visible at the CLI
// layer and makes the borrowed ledger adapter's contract testable.
func CloseSharedSQLite(store *storage.SQLite) error {
	if store == nil {
		return nil
	}
	return store.Close()
}

// PoolLimitsFromConfig maps the [subagents] config contract onto pool
// limits. Config zero means unlimited (see resolveSubagentConfig and the
// mivia.toml comments); the pool primitive would otherwise substitute its
// safe defaults for zero and silently cap agent-planned DAGs.
func PoolLimitsFromConfig(cfg config.SubagentConfig) (maxDepth, maxFanout int) {
	maxDepth, maxFanout = cfg.MaxDepth, cfg.MaxFanout
	if maxDepth == 0 {
		maxDepth = subagents.Unlimited
	}
	if maxFanout == 0 {
		maxFanout = subagents.Unlimited
	}
	return maxDepth, maxFanout
}

// ActiveCoordinator returns the first Coordinator registered in the
// package-level coordinators map, if any. Used to find a running dispatch's
// Coordinator from a caller that does not hold the *runtime.Dispatcher key.
func ActiveCoordinator() (coordinator.Coordinator, bool) {
	var found coordinator.Coordinator
	var ok bool
	coordinators.Range(func(_, value any) bool {
		c, isCoordinator := value.(coordinator.Coordinator)
		if !isCoordinator {
			return true // continue
		}
		found, ok = c, true
		return false // stop after first
	})
	return found, ok
}

// InitCoordinator lazily creates the Coordinator singleton with an in-memory
// or durable ledger repository and a subagent pool backed by the given
// dispatcher. Safe for concurrent calls; only the first invocation initialises
// the singleton. Subsequent calls are no-ops.
func InitCoordinator(d *runtime.Dispatcher, cfg config.SubagentConfig, repos ...ledger.LedgerRepository) coordinator.Coordinator {
	if existing, ok := coordinators.Load(d); ok {
		return existing.(coordinator.Coordinator)
	}
	poolDepth, poolFanout := PoolLimitsFromConfig(cfg)
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
		repo = EffectiveOrchestrationRepo(repos[0])
	} else if cfg.StoreBackend == "sqlite" {
		// Shared open+recover+fallback. Close is wired below when this init
		// owns the coordinator for d.
		repo, ownedStore = OpenDurableLedgerRepo(cfg, os.Stderr)
	}
	pool := subagents.New(d, subagents.Policy{
		Workers:   cfg.MaxWorkers,
		MaxDepth:  poolDepth,
		MaxFanout: poolFanout,
		MaxBudget: cfg.DefaultBudget,
		Timeout:   config.SaturatingSeconds(cfg.DefaultTimeout),
		// Anti-thundering-herd: space batch task starts so concurrent
		// workers do not open their first provider call on the same instant.
		SpawnStagger: time.Duration(cfg.SpawnStaggerMs) * time.Millisecond,
	})
	c := coordinator.New(repo, pool).WithRetryPolicy(TaskRetryPolicyFromConfig(cfg.TaskRetry))
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

// maxTaskRetries and minTaskRetryBaseBackoff clamp [subagents.retry] against
// misconfiguration (bug-audit finding, security lens): TaskRetryConfig had no
// bound on max_retries and RetryPolicy.EffectiveBackoff only floors a
// base_backoff of exactly zero-or-negative to 100ms, so a typo'd value (an
// extra zero on max_retries, or base_backoff_seconds = 0.001) could retry-storm
// a provider with none of the caps internal/provider/retry.go's own
// HTTP-layer retry hardcodes for the same reason.
const (
	maxTaskRetries          = 20
	minTaskRetryBaseBackoff = 50 * time.Millisecond
)

// TaskRetryPolicyFromConfig converts the [subagents.retry] TOML surface into
// a coordinator.RetryPolicy. internal/config cannot import internal/coordinator
// (coordinator already imports config), so this CLI-layer seam does the
// conversion. An all-zero TaskRetryConfig (the default) converts to
// coordinator.NoRetry, identical to today's hardcoded behavior - a deployment
// must opt in via [subagents.retry] max_retries > 0 to enable retry.
func TaskRetryPolicyFromConfig(cfg config.TaskRetryConfig) coordinator.RetryPolicy {
	if cfg.MaxRetries <= 0 {
		return coordinator.NoRetry
	}
	maxRetries := cfg.MaxRetries
	if maxRetries > maxTaskRetries {
		maxRetries = maxTaskRetries
	}
	baseBackoff := time.Duration(cfg.BaseBackoffSeconds * float64(time.Second))
	if baseBackoff > 0 && baseBackoff < minTaskRetryBaseBackoff {
		baseBackoff = minTaskRetryBaseBackoff
	}
	// coordinator.RetryPolicy.EffectiveBackoff applies MaxBackoff as a hard
	// ceiling computed AFTER BaseBackoff (retry.go) - so a MaxBackoff smaller
	// than BaseBackoff silently clamps every backoff back down below the
	// floor just enforced above, defeating it entirely (bug-audit finding,
	// config-plumbing lens). 0 (unset) means "no cap" to EffectiveBackoff and
	// must stay 0, not be raised - only an explicit, too-small positive value
	// gets raised to meet the floor it would otherwise undercut.
	maxBackoff := time.Duration(cfg.MaxBackoffSeconds * float64(time.Second))
	if maxBackoff > 0 && maxBackoff < baseBackoff {
		maxBackoff = baseBackoff
	}
	return coordinator.RetryPolicy{
		MaxRetries:     maxRetries,
		BaseBackoff:    baseBackoff,
		MaxBackoff:     maxBackoff,
		BackoffFactor:  cfg.BackoffFactor,
		JitterFraction: cfg.JitterFraction,
	}
}

// OrchestrationRepoForDispatcher returns the ledger repository registered for d.
func OrchestrationRepoForDispatcher(d *runtime.Dispatcher) ledger.LedgerRepository {
	if d == nil {
		return nil
	}
	if repo, ok := coordinatorRepos.Load(d); ok {
		return repo.(ledger.LedgerRepository)
	}
	return nil
}
