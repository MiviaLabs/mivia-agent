package clichat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// store_note is the write side of the note vocabulary. A sub-agent (or the
// root agent) stores model-authored content and hands the returned
// "ref:note:<digest>" back in its report, so a long report can park overflow
// detail in the ledger without flooding the parent's context. The kind is
// deliberately distinct from ref:output:, which a coordinator attests as a
// task's recorded output; a note is unattested model-authored text.
//
// Validation fails closed with JSON error responses: a bad write must be a
// bounded answer the model can correct, never a panic and never a ref that
// does not resolve (INV-AG-10). The ref is surfaced only on a nil store error.

const (
	// defaultStoreNoteMaxBytes caps one note body. A note is context
	// relief, not a bulk export channel.
	defaultStoreNoteMaxBytes = 64 << 10
	// defaultStoreNoteMaxPerTask caps how many notes one task may store,
	// so a looping agent cannot flood the ledger.
	defaultStoreNoteMaxPerTask = 16
)

type storeNoteTool struct {
	repo       ledger.LedgerRepository
	maxBytes   int
	maxPerTask int

	// mu guards counts and watchers, keyed RunID+"/"+TaskID, so concurrent
	// sub-agent tool calls cannot race the per-task budget. Each key's
	// latest watcher goroutine deletes the entry when its writing task's
	// context ends - otherwise counts grows by one entry per distinct task
	// for the life of the process, which never shrinks in a long-running
	// session. A retry redispatch runs a fresh context for the same key, so
	// a stale watcher from the prior attempt must never delete the new
	// attempt's count; ownership comparison makes that stale watcher exit.
	mu       sync.Mutex
	counts   map[string]int
	watchers map[string]context.Context
}

// Name reports the model-facing tool name.
func (t *storeNoteTool) Name() string { return "store_note" }

// storeNoteTool intentionally does not implement tools.PrivilegedTool: it
// stores the caller's own authored bytes under a task-scoped budget and grants
// no control over a session, so sub-agents may call it.

func (t *storeNoteTool) Description() string {
	return "Store a short note of your own authoring for the current agent task and receive a content reference " +
		"of the form 'ref:note:<digest>' that you can put in your final report. " +
		"Use it to park overflow detail (long evidence, tables, transcripts) instead of inflating the report itself. " +
		"The note must be small text; very large or binary content is rejected. " +
		"Requires an active task context: with no task running, the call fails."
}

func (t *storeNoteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The note body: plain text you authored, stored verbatim under a content reference",
			},
		},
		"required":             []string{"content"},
		"additionalProperties": false,
	}
}

func (t *storeNoteTool) writeCap() int {
	if t.maxBytes <= 0 {
		return defaultStoreNoteMaxBytes
	}
	return t.maxBytes
}

func (t *storeNoteTool) taskCap() int {
	if t.maxPerTask <= 0 {
		return defaultStoreNoteMaxPerTask
	}
	return t.maxPerTask
}

func (t *storeNoteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("store_note: %w", err)
	}
	if params.Content == "" {
		return `{"error":"content is required and must be non-empty"}`, nil
	}
	if len(params.Content) > t.writeCap() {
		return jsonPayload(map[string]any{
			"error":     "content exceeds the maximum note size",
			"max_bytes": t.writeCap(),
		}), nil
	}
	identity, ok := runtime.TaskIdentityFrom(ctx)
	if !ok {
		return jsonPayload(map[string]any{
			"error": "no active task: a note can only be stored while a task is running",
		}), nil
	}
	key := identity.RunID + "/" + identity.TaskID
	if !t.admitNote(ctx, key, t.taskCap()) {
		return jsonPayload(map[string]any{
			"error":     "note budget exhausted for this task",
			"max_notes": t.taskCap(),
		}), nil
	}
	ref := ledger.Reference(ledger.RefKindNote, []byte(params.Content))
	if t.repo == nil {
		t.releaseNote(key)
		return "", fmt.Errorf("store_note: no execution history repository")
	}
	if err := t.repo.StoreContent(ctx, ref, []byte(params.Content)); err != nil {
		// Fail closed: the ref is content-addressed, so surfacing it on a
		// failed store would hand the model a pointer to phantom content.
		// The failed write also never consumed budget: transient store
		// errors must not starve the task's note allowance.
		t.releaseNote(key)
		return "", fmt.Errorf("store_note: %w", err)
	}
	return jsonPayload(map[string]any{
		"ref":   ref,
		"bytes": len(params.Content),
	}), nil
}

// admitNote records one pending note for key under the per-task cap. The
// key's latest admission arms a cleanup goroutine that removes the entry when
// that attempt's context ends, so counts stays bounded by concurrently active
// tasks. A retry redispatch of the same key runs a fresh context: the stale
// watcher from the ended attempt is replaced, the fresh attempt starts with a
// clean budget, and the stale goroutine exits without deleting anything.
func (t *storeNoteTool) admitNote(ctx context.Context, key string, cap int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = map[string]int{}
	}
	if old := t.watchers[key]; old != nil && old.Err() != nil {
		// The prior attempt ended but its goroutine has not run yet: retire
		// its budget and take over the watcher slot for the fresh attempt.
		// This runs before the cap check, so a retry of an attempt that
		// maxed out its allowance still starts clean.
		t.counts[key] = 1
		t.watchers[key] = ctx
		go t.releaseOnDone(ctx, key)
		return true
	}
	if t.counts[key] >= cap {
		return false
	}
	t.counts[key]++
	if t.watchers[key] == nil {
		if t.watchers == nil {
			t.watchers = map[string]context.Context{}
		}
		t.watchers[key] = ctx
		go t.releaseOnDone(ctx, key)
	}
	return true
}

// releaseNote refunds one budget slot whose store failed, so transient
// repository errors cannot starve a task's note allowance.
func (t *storeNoteTool) releaseNote(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts[key] > 0 {
		t.counts[key]--
	}
}

// releaseOnDone deletes key's budget entry once its owning attempt's context
// ends. It deletes only while still owning the key: if a retry redispatch
// replaced the watcher, this stale goroutine exits and leaves the fresh
// attempt's count alone.
func (t *storeNoteTool) releaseOnDone(ctx context.Context, key string) {
	<-ctx.Done()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.watchers[key] != ctx {
		return
	}
	delete(t.counts, key)
	delete(t.watchers, key)
}

// Capability declares a write. Capability cannot see the request context, so
// no per-task ResourceKey is derivable here; the registry's default
// workspace:mutation key serializes note writes, which is safe and rare.
func (t *storeNoteTool) Capability(args json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, MaxResultBytes: 4096}
}

// Ensure storeNoteTool implements required interfaces at compile time.
var (
	_ tools.Tool        = (*storeNoteTool)(nil)
	_ tools.CapableTool = (*storeNoteTool)(nil)
)
