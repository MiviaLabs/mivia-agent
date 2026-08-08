package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestSQLiteSingleTaskTerminalAndRunnableAdmissionRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	firstStore, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := storage.OpenSQLite(path)
	if err != nil {
		_ = firstStore.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstStore.Close(); _ = secondStore.Close() })
	var calls atomic.Int32
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", invoker(func(context.Context, runtime.Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	first := New(ledger.NewStorageLedgerRepository(firstStore), subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	second := New(ledger.NewStorageLedgerRepository(secondStore), subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	req := EnsureRunRequest{RunID: NewRunID(), Tasks: []subagents.Task{{ID: "task-panel", Name: "worker", Input: json.RawMessage(`"work"`)}}, IdempotencyKey: "panel-admission"}

	results := make(chan *RunHandle, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		h, err := first.EnsureSingleTaskRun(context.Background(), req)
		if err != nil {
			errs <- err
			return
		}
		results <- h
	}()
	go func() {
		defer wg.Done()
		h, err := second.EnsureTerminalSingleTaskRun(context.Background(), req, ledger.TaskStatusCanceled)
		if err != nil {
			errs <- err
			return
		}
		results <- h
	}()
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for h := range results {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := h.owner.Join(ctx, h)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	observer := ledger.NewStorageLedgerRepository(firstStore)
	run, err := observer.GetRunByIdempotencyKey(context.Background(), scopedKey(context.Background(), req.IdempotencyKey))
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := observer.ListTasks(context.Background(), run.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %+v, err = %v", tasks, err)
	}
	if calls.Load() > 1 {
		t.Fatalf("handler calls = %d, want at most one", calls.Load())
	}
}
