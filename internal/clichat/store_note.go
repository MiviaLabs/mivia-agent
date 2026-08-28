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

	// mu guards counts, keyed RunID+"/"+TaskID, so concurrent sub-agent
	// tool calls cannot race the per-task budget. Every key gets exactly
	// one cleanup goroutine, armed on the key's first write, that deletes
	// the entry when the writing task's context ends - otherwise counts
	// grows by one entry per distinct task for the life of the process,
	// which never shrinks in a long-running session.
	mu       sync.Mutex
	counts   map[string]int
	watching map[string]bool
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
		return "", fmt.Errorf("store_note: no execution history repository")
	}
	if err := t.repo.StoreContent(ctx, ref, []byte(params.Content)); err != nil {
		// Fail closed: the ref is content-addressed, so surfacing it on a
		// failed store would hand the model a pointer to phantom content.
		return "", fmt.Errorf("store_note: %w", err)
	}
	return jsonPayload(map[string]any{
		"ref":   ref,
		"bytes": len(params.Content),
	}), nil
}

// admitNote records one pending note for key under the per-task cap. The
// key's first admission arms a cleanup goroutine that removes it from
// counts when ctx ends, so a finished task's budget entry does not
// outlive the task.
func (t *storeNoteTool) admitNote(ctx context.Context, key string, cap int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = map[string]int{}
	}
	if t.counts[key] >= cap {
		return false
	}
	t.counts[key]++
	if !t.watching[key] {
		if t.watching == nil {
			t.watching = map[string]bool{}
		}
		t.watching[key] = true
		go t.releaseOnDone(ctx, key)
	}
	return true
}

// releaseOnDone deletes key's budget entry once its owning task's context
// ends, so counts stays bounded by concurrently active tasks rather than
// growing by one entry per task for the life of the process.
func (t *storeNoteTool) releaseOnDone(ctx context.Context, key string) {
	<-ctx.Done()
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counts, key)
	delete(t.watching, key)
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
