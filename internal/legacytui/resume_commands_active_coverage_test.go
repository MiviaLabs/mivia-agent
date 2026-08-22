package legacytui

// resume_commands_active_coverage_test.go drives the
// active-coordinator branches of handleResumeSlash and
// handlePendingResumeInput by registering a real Coordinator via
// cliorchestrate.StoreTestCoordinator. The no-coordinator branch
// is covered by resume_commands_coverage_test.go; this file
// exercises the lines that require a live coordinator.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestHandleResumeSlashWithActiveCoordinator(t *testing.T) {
	// Register a real Coordinator and Dispatcher in cliorchestrate's
	// package-level map, drive the slash handler, then unregister.
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := ledger.NewBorrowedStorageLedgerRepository(store)
	pool := subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1})
	coord := coordinator.New(repo, pool)
	dispatcher := runtime.New(runtime.Policy{})

	cleanup := cliorchestrate.StoreTestCoordinator(dispatcher, coord, repo)
	defer cleanup()

	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, nil)
	m := newTUIModel(sess, &config.Resolved{Model: "m", ProviderName: "p"}, false)

	// With a coordinator registered but no interrupted runs, the
	// "list interrupted runs" branch returns an empty list and the
	// handler prints the formatted (empty) result. Lines 22-29.
	if !m.handleResumeSlash([]string{"/resume"}) {
		t.Fatal("handleResumeSlash(/resume) must return true")
	}
	// With a run id argument, falls into the "not found in
	// interrupted list, try direct resume" branch. Lines 49-58.
	if !m.handleResumeSlash([]string{"/resume", "run-x"}) {
		t.Fatal("handleResumeSlash(/resume run-x) must return true")
	}
	// And the handlePendingResumeInput path with the empty
	// pending state and a "no" input.
	_ = context.Background()
	if !m.handlePendingResumeInput("no") {
		t.Fatal("handlePendingResumeInput(no) must return true")
	}
}
