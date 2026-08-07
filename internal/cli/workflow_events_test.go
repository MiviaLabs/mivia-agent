package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestWorkflowEventsContinuationHintWithForeignEvents: the continuation hint
// must appear as soon as a full page of DECODABLE events is printed, even when
// unknown events are interleaved in the raw stream. Raw paging used to shorten
// the first page (3 decodable events here) and suppress the hint, hiding the
// remaining audit trail.
func TestWorkflowEventsContinuationHintWithForeignEvents(t *testing.T) {
	root, store, repo, closeFn, ctx, run := openEventsFixture(t)
	seedInterleavedEvents(t, store, repo, ctx, run)
	closeFn() // release the seeding connection; the command opens its own

	var stdout strings.Builder
	err := executeWorkflowEvents(run, root, filepath.Join(root, "config.toml"), 4, 0, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("executeWorkflowEvents error = %v", err)
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("stdout = %d lines, want 4 event lines + the hint:\n%s", len(lines), stdout.String())
	}
	if !strings.Contains(stdout.String(), "showing 4 of at least 4 events") {
		t.Fatalf("stdout = %q, want the continuation hint (full decodable page with more events remaining)", stdout.String())
	}
}

// openEventsFixture builds a workspace config + sqlite store + a fresh run,
// mirroring how openWorkflowReportContext resolves them, and returns the store
// handles so the caller can seed events.
func openEventsFixture(t *testing.T) (root string, store *storage.SQLite, repo *workflowledger.StorageRepository, closeFn func(), ctx context.Context, run string) {
	t.Helper()
	root = t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	configBody := `[provider]
name = "openrouter"
[providers.openrouter]
base_url = "https://example.com"
api_key_env = "WORKFLOW_EVENTS_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
[subagents]
store_backend = "sqlite"
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	work, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, _, closeFn, err = openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	repo = workflowledger.NewStorageRepository(store)

	ctx = context.Background()
	run = "wfr-cli-events-hint"
	snap := workflowledger.RunSnapshot{
		RunID: run, WorkflowName: "test-wf", Status: workflowledger.RunStatusPending, ActiveStepID: "start",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, snap, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	return root, store, repo, closeFn, ctx, run
}

// seedInterleavedEvents appends a 15-event raw stream (10 decodable
// run_status_changed events interleaved with 5 foreign wf_unknown_kind events)
// in sequence order: D U D D U D D D U D U D D U D.
func seedInterleavedEvents(t *testing.T, store *storage.SQLite, repo *workflowledger.StorageRepository, ctx context.Context, run string) {
	t.Helper()
	// injectUnknown appends a foreign wf_unknown_kind event at the next free
	// sequence, interleaving it into the raw stream in sequence order.
	injectUnknown := func() {
		events, err := store.Events(ctx, run)
		if err != nil {
			t.Fatal(err)
		}
		seq := 0
		for _, ev := range events {
			if ev.Sequence > seq {
				seq = ev.Sequence
			}
		}
		if err := store.Append(ctx, storage.Event{
			ID: fmt.Sprintf("wfe:foreign:%d", seq+1), RunID: run, Sequence: seq + 1,
			Kind: "wf_unknown_kind", Payload: []byte("{}"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// oneStatusEvent appends exactly one run_status_changed event by stepping
	// the run through pending -> running -> waiting_approval -> running.
	status := workflowledger.RunStatusPending
	oneStatusEvent := func() {
		stored, err := repo.GetRun(ctx, run)
		if err != nil {
			t.Fatal(err)
		}
		switch status {
		case workflowledger.RunStatusPending:
			if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
				t.Fatal(err)
			}
			status = workflowledger.RunStatusRunning
		case workflowledger.RunStatusRunning:
			if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusWaitingApproval, nil); err != nil {
				t.Fatal(err)
			}
			status = workflowledger.RunStatusWaitingApproval
		case workflowledger.RunStatusWaitingApproval:
			if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
				t.Fatal(err)
			}
			status = workflowledger.RunStatusRunning
		}
	}

	injectUnknown()
	oneStatusEvent() // D
	oneStatusEvent() // D
	injectUnknown()
	oneStatusEvent() // D
	oneStatusEvent() // D
	oneStatusEvent() // D
	injectUnknown()
	oneStatusEvent() // D
	injectUnknown()
	oneStatusEvent() // D
	oneStatusEvent() // D
	injectUnknown()
	oneStatusEvent() // D
}
