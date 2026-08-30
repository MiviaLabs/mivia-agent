package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// recordingCompleter captures the provider.Request a plain turn sends, so a
// test can assert what the completer contract received - req.Timeout is the
// field every provider client arms its per-request deadline from.
type recordingCompleter struct {
	lastReq provider.Request
	out     string
	err     error
}

func (r *recordingCompleter) Name() string { return "recording" }

func (r *recordingCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return r.ChatStream(ctx, req, io.Discard)
}

func (r *recordingCompleter) ChatStream(_ context.Context, req provider.Request, w io.Writer) (string, error) {
	r.lastReq = req
	if w != nil && r.out != "" {
		_, _ = io.WriteString(w, r.out)
	}
	if r.err != nil {
		return "", r.err
	}
	return r.out, nil
}

func (r *recordingCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	out, err := r.ChatStream(ctx, req, req.StreamWriter)
	if err != nil {
		return nil, err
	}
	return &provider.Response{Content: out}, nil
}

// TestPlainTurnRequestCarriesConfiguredTimeout pins that a plain (tools-off)
// turn hands the configured [chat] request_timeout_seconds to the completer
// as req.Timeout - the field the provider clients arm their per-request
// deadline from. Before this pin, only agent turns carried the deadline and
// a plain turn's sole hard bound was the derived http.Client wall.
func TestPlainTurnRequestCarriesConfiguredTimeout(t *testing.T) {
	rec := &recordingCompleter{out: "answer"}
	sess := NewSession(&config.Resolved{
		Model:              "test-model",
		SystemPrompt:       "sys",
		ChatRequestTimeout: 1200 * time.Second,
	}, rec)

	var sink strings.Builder
	if _, err := sess.SendUser(context.Background(), "hello", &sink); err != nil {
		t.Fatal(err)
	}
	if rec.lastReq.Timeout != 1200*time.Second {
		t.Fatalf("plain turn req.Timeout = %v; want the configured 1200s", rec.lastReq.Timeout)
	}
}

// TestPlainTurnRequestTimeoutDefaultsWhenZero pins the zero-value fallback: a
// session built from a hand-built Resolved with no [chat] resolution still
// arms DefaultRequestTimeout, never zero (zero would leave the request
// unbounded up to the client wall).
func TestPlainTurnRequestTimeoutDefaultsWhenZero(t *testing.T) {
	rec := &recordingCompleter{out: "answer"}
	sess := NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "sys"}, rec)

	var sink strings.Builder
	if _, err := sess.SendUser(context.Background(), "hello", &sink); err != nil {
		t.Fatal(err)
	}
	if rec.lastReq.Timeout != DefaultRequestTimeout {
		t.Fatalf("plain turn req.Timeout = %v; want DefaultRequestTimeout %v", rec.lastReq.Timeout, DefaultRequestTimeout)
	}
}

// TestPlainContextTurnRequestCarriesConfiguredTimeout pins the same contract
// on the durable-context plain path (sendPlainContext), which builds its own
// provider.Request literal - the sibling site of the legacy path's.
func TestPlainContextTurnRequestCarriesConfiguredTimeout(t *testing.T) {
	rec := &recordingCompleter{out: "answer"}
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession(&config.Resolved{
		ProviderName:       "fake",
		Model:              "model",
		SystemPrompt:       "sys",
		ChatRequestTimeout: 1200 * time.Second,
	}, rec)
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("fake", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	sess.SetContextStore(store)

	var sink strings.Builder
	if _, err := sess.SendUser(context.Background(), "hello", &sink); err != nil {
		t.Fatal(err)
	}
	if rec.lastReq.Timeout != 1200*time.Second {
		t.Fatalf("context plain turn req.Timeout = %v; want the configured 1200s", rec.lastReq.Timeout)
	}
}

// TestPlainContextTurnDeadlineKeepsPartialAndSucceeds pins the surfacing
// contract for a fired request deadline on the context plain path: the error
// carries context.DeadlineExceeded identity, so the interrupt branch adopts
// the streamed partial, commits it durably, and hands it back as a quiet
// success (INV-AG-8). The nil error is also the precondition for the
// post-turn auto-compaction pass: unlike Ctrl+C, the outer ctx is live here,
// so compactAfterTurn may run - a deliberate, documented consequence (see
// compactAfterTurn's comment), bounded by the summarizer's own 20s budget.
func TestPlainContextTurnDeadlineKeepsPartialAndSucceeds(t *testing.T) {
	const partial = "the answer starts like th"
	rec := &recordingCompleter{
		out: partial,
		err: fmt.Errorf("stream read: %w", context.DeadlineExceeded),
	}
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, rec)
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("fake", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	sess.SetContextStore(store)

	var sink strings.Builder
	reply, err := sess.SendUser(context.Background(), "prove it", &sink)
	if err != nil {
		t.Fatalf("deadline-interrupted plain turn must return the partial quietly, got error: %v", err)
	}
	if !strings.Contains(reply, partial) {
		t.Fatalf("reply = %q, want it to contain the streamed partial %q", reply, partial)
	}
	if !errors.Is(rec.err, context.DeadlineExceeded) {
		t.Fatal("test wiring: the completer error must carry deadline identity")
	}
}
