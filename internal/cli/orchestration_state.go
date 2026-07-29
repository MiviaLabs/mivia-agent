// Package cli provides orchestration tool implementations that bridge the
// model-facing tool set with the coordinator's async run model.
package cli

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync"
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

// handleRetention controls how long completed orchestration run handles
// remain accessible. Default 10 minutes; may be overridden via config.
var handleRetentionDuration = 10 * time.Minute

type orchestrationHandle struct {
	coord      coordinator.Coordinator
	handle     *coordinator.RunHandle
	repo       ledger.LedgerRepository
	dispatcher *runtime.Dispatcher
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
	runHandles.Store(runID, record)
	go func() {
		<-record.handle.Done()
		timer := time.NewTimer(handleRetentionDuration)
		defer timer.Stop()
		<-timer.C
		if current, ok := runHandles.Load(runID); ok && current == record {
			runHandles.Delete(runID)
		}
	}()
}

func orchestrationHandleAccessible(record *orchestrationHandle, dispatcher *runtime.Dispatcher, repo ledger.LedgerRepository) bool {
	return record != nil && record.dispatcher == dispatcher && repositoriesMatch(record.repo, repo)
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
		// If the repo is a StorageLedgerRepository, advance run ID counter
		// past any stored runs to prevent collisions on process restart.
		if sr, ok := repo.(*ledger.StorageLedgerRepository); ok {
			if maxRun := sr.MaxRunIDNumber(); maxRun > 0 {
				coordinator.AdvanceRunIDCounter(maxRun)
			}
		}
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
			// Advance the run ID counter past any stored run IDs so new
			// spawns don't collide with replayed runs on process restart.
			if maxRun := storageRepo.MaxRunIDNumber(); maxRun > 0 {
				coordinator.AdvanceRunIDCounter(maxRun)
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
	// Apply handle retention from config if specified.
	if cfg.HandleRetentionSeconds > 0 {
		handleRetentionDuration = time.Duration(cfg.HandleRetentionSeconds) * time.Second
	}
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
