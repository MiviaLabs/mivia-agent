// Package cli provides orchestration tool implementations that bridge the
// model-facing tool set with the coordinator's async run model.
package cli

import (
	"context"
	"fmt"
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
	coordinators sync.Map // *runtime.Dispatcher → coordinator.Coordinator
	runHandles   sync.Map // runID → orchestrationHandle
)

var defaultOrchestrationRepo ledger.LedgerRepository = ledger.NewMemoryLedgerRepository()

// activeSessionCaller is the chat session's identity, recorded once per process.
//
// Orchestration control initiated from a CLI surface — a slash command or a
// dashboard key — has no dispatcher-stamped caller in its context. But a handle
// it registers must stay controllable by the model tools that run later, and
// `Dispatcher.Invoke` stamps `req.SessionID`/`req.Role` onto every tool call
// (`internal/runtime/dispatcher.go`). For the main session those come from
// `chat.Session.SessionID` with an empty role, so that is the pair a
// CLI-initiated handle must carry.
//
// Minting a fresh ephemeral principal instead — which the legacy compatibility
// path in orchestrate.go does *deliberately*, precisely so those callers
// "cannot later control a handle" — registers the run under an identity no
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
		record.retention = 10 * time.Minute
	}
	closed := make(chan struct{})
	record.dispatcher.OnClose(func() {
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

func orchestrationHandleAccessible(ctx context.Context, record *orchestrationHandle, dispatcher *runtime.Dispatcher, repo ledger.LedgerRepository) bool {
	principal, ok := principalFromContext(ctx)
	return ok && record != nil && record.dispatcher == dispatcher && repositoriesMatch(record.repo, repo) && record.principal == principal
}

func orchestrationHandleRetention(cfg config.SubagentConfig) time.Duration {
	if cfg.HandleRetentionSeconds > 0 {
		return time.Duration(cfg.HandleRetentionSeconds) * time.Second
	}
	return 10 * time.Minute
}

// initCoordinator lazily creates the Coordinator singleton with an in-memory
// or durable ledger repository and a subagent pool backed by the given dispatcher.
// Safe for concurrent calls; only the first invocation initialises the
// singleton.  Subsequent calls are no-ops.
func initCoordinator(d *runtime.Dispatcher, cfg config.SubagentConfig, repos ...ledger.LedgerRepository) coordinator.Coordinator {
	if existing, ok := coordinators.Load(d); ok {
		return existing.(coordinator.Coordinator)
	}
	repo := defaultOrchestrationRepo
	if len(repos) > 0 {
		repo = effectiveOrchestrationRepo(repos[0])
	} else if cfg.StoreBackend == "sqlite" {
		// Create durable StorageLedgerRepository backed by SQLite.
		sqlStore, err := storage.OpenSQLite(cfg.StorePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to open SQLite store %q: %v; falling back to memory backend\n", cfg.StorePath, err)
		} else {
			storageRepo := ledger.NewStorageLedgerRepository(sqlStore)
			// Run startup recovery: mark orphaned active runs as interrupted.
			recovered, recErr := storageRepo.Recover(context.Background())
			if recErr != nil {
				fmt.Fprintf(os.Stderr, "warning: orchestration recovery error: %v\n", recErr)
			} else if len(recovered) > 0 {
				for _, r := range recovered {
					if r.WasInterrupted {
						fmt.Fprintf(os.Stderr, "info: recovered interrupted run %s (%s)\n", r.RunID, r.DisplayName)
					}
				}
			}
			repo = storageRepo
		}
	}
	pool := subagents.New(d, subagents.Policy{
		Workers:   cfg.MaxWorkers,
		MaxDepth:  cfg.MaxDepth,
		MaxFanout: cfg.MaxFanout,
		MaxBudget: cfg.DefaultBudget,
		Timeout:   time.Duration(cfg.DefaultTimeout) * time.Second,
		Partial:   cfg.PartialResults,
	})
	c := coordinator.New(repo, pool)
	actual, _ := coordinators.LoadOrStore(d, c)
	if actual == c {
		d.OnClose(func() {
			// Close durable store if applicable.
			if sr, ok := repo.(*ledger.StorageLedgerRepository); ok {
				_ = sr.Close()
			}
			coordinators.Delete(d)
		})
	}
	return actual.(coordinator.Coordinator)
}
