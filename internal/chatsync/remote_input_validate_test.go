package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRejectionPoller builds a poller against a server that unconditionally
// offers the given SessionInput and consumes it successfully, wired to a
// fixed author id and an onRejected recorder. Tests use it to drive
// validateRemoteInput's refusal paths end to end through pollOnce.
func newRejectionPoller(t *testing.T, sessionID string, input SessionInput, expectedAuthor string) (*InputPoller, *[]string) {
	t.Helper()
	var rejections []string
	mux := http.NewServeMux()
	served := false
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if served {
			_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
			return
		}
		served = true
		in := input
		_ = json.NewEncoder(w).Encode(NextInput{Input: &in})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().Format(time.RFC3339)
		out := input
		out.ConsumedAt = &now
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	poller := NewInputPoller(client, sessionID, 1, fixedAuthorUserIDProvider(expectedAuthor), t.TempDir())
	poller.SetOnRejected(func(id, sessID, reason string) {
		rejections = append(rejections, reason)
	})
	return poller, &rejections
}

func TestInputPoller_RejectsSessionIDMismatch(t *testing.T) {
	poller, rejections := newRejectionPoller(t, "sess-mine", SessionInput{
		ID: "inp-1", SessionID: "sess-other", AuthorUserID: "user-1", Kind: "message", Body: "hi",
	}, "user-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())
	assertNeverDelivered(t, poller, rejections, "session id mismatch")
}

func TestInputPoller_RejectsUnsupportedKind(t *testing.T) {
	poller, rejections := newRejectionPoller(t, "sess-1", SessionInput{
		ID: "inp-1", SessionID: "sess-1", AuthorUserID: "user-1", Kind: "system", Body: "hi",
	}, "user-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())
	assertNeverDelivered(t, poller, rejections, "unsupported kind")
}

func TestInputPoller_RejectsOversizedBody(t *testing.T) {
	poller, rejections := newRejectionPoller(t, "sess-1", SessionInput{
		ID: "inp-1", SessionID: "sess-1", AuthorUserID: "user-1", Kind: "message",
		Body: strings.Repeat("a", maxRemoteInputBodyBytes+1),
	}, "user-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())
	assertNeverDelivered(t, poller, rejections, "exceeds")
}

func TestInputPoller_RejectsControlCharsInBody(t *testing.T) {
	poller, rejections := newRejectionPoller(t, "sess-1", SessionInput{
		ID: "inp-1", SessionID: "sess-1", AuthorUserID: "user-1", Kind: "message",
		Body: "hello\x00world",
	}, "user-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())
	assertNeverDelivered(t, poller, rejections, "control character")
}

// TestInputPoller_RejectsBidiOverrideInBody guards against the "Trojan
// Source" class (CVE-2021-42574): a body built entirely from ordinary
// printable runes can still DISPLAY completely differently than it reads
// once a bidi override/isolate character is inserted. This body becomes
// real model input under whatever approval policy is already bound (very
// likely auto-approve), so what a person reviewing the transcript sees must
// match what the model actually receives.
func TestInputPoller_RejectsBidiOverrideInBody(t *testing.T) {
	poller, rejections := newRejectionPoller(t, "sess-1", SessionInput{
		ID: "inp-1", SessionID: "sess-1", AuthorUserID: "user-1", Kind: "message",
		Body: "run tests‮noop⁩ --harmless",
	}, "user-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())
	assertNeverDelivered(t, poller, rejections, "control character")
}

// TestInputPoller_RejectsAuthorMismatch is the DC-13 successor: an input
// authored by anyone other than the CLI's own verified principal must never
// reach Inputs(), even though the server already consumed it. This replaces
// TestSessionPool_DoesNotExecuteRemoteInput's blanket "polling never runs"
// assertion (uiadapter no longer disables polling outright - see
// internal/uiadapter/session_pool.go) with the actual safety property: an
// unverified author's instruction is still refused at the source.
func TestInputPoller_RejectsAuthorMismatch(t *testing.T) {
	poller, rejections := newRejectionPoller(t, "sess-1", SessionInput{
		ID: "inp-attacker", SessionID: "sess-1", AuthorUserID: "attacker", Kind: "message",
		Body: "rm -rf /",
	}, "user-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())
	assertNeverDelivered(t, poller, rejections, "does not match")
}

// TestInputPoller_RejectsWhenNoAuthorProviderConfigured pins the fail-closed
// default: a nil AuthorUserIDProvider (no verified identity available at
// all) must refuse every input rather than silently trust it.
func TestInputPoller_RejectsWhenNoAuthorProviderConfigured(t *testing.T) {
	var rejections []string
	mux := http.NewServeMux()
	served := false
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if served {
			_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
			return
		}
		served = true
		_ = json.NewEncoder(w).Encode(NextInput{Input: &SessionInput{
			ID: "inp-1", SessionID: "sess-1", AuthorUserID: "user-1", Kind: "message", Body: "hi",
		}})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().Format(time.RFC3339)
		_ = json.NewEncoder(w).Encode(SessionInput{
			ID: "inp-1", SessionID: "sess-1", AuthorUserID: "user-1", Kind: "message", Body: "hi", ConsumedAt: &now,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	poller := NewInputPoller(client, "sess-1", 1, nil, t.TempDir())
	poller.SetOnRejected(func(id, sessID, reason string) { rejections = append(rejections, reason) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())
	assertNeverDelivered(t, poller, &rejections, "unverifiable")
}

func assertNeverDelivered(t *testing.T, poller *InputPoller, rejections *[]string, wantReasonSubstr string) {
	t.Helper()
	select {
	case ri := <-poller.Inputs():
		t.Fatalf("unexpected delivery of an input that should have been rejected: %+v", ri)
	case <-time.After(300 * time.Millisecond):
	}
	if len(*rejections) == 0 {
		t.Fatal("onRejected was never called")
	}
	if !strings.Contains((*rejections)[0], wantReasonSubstr) {
		t.Errorf("rejection reason = %q, want substring %q", (*rejections)[0], wantReasonSubstr)
	}
}

// TestInputPoller_LedgerPreventsRedeliveryAfterCrash pins the item-2 ledger:
// a pending_input.json left behind AFTER the delivered-ids ledger recorded
// its id (the crash window between recordDelivered and clearPendingInput)
// must not be redelivered on the next Start - the UI almost certainly
// already ran it once.
func TestInputPoller_LedgerPreventsRedeliveryAfterCrash(t *testing.T) {
	stateDir := t.TempDir()

	pendingData, err := json.Marshal(pendingInputState{
		Input: &SessionInput{
			ID: "inp-already-delivered", SessionID: "sess-ledger", AuthorUserID: "user-1",
			Kind: "message", Body: "already ran once",
		},
		Consumed: true,
	})
	if err != nil {
		t.Fatalf("marshal pending state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, pendingInputFileName), pendingData, 0o600); err != nil {
		t.Fatalf("write pending file: %v", err)
	}
	ledgerData, err := json.Marshal([]string{"inp-already-delivered"})
	if err != nil {
		t.Fatalf("marshal ledger: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, deliveredIDsFileName), ledgerData, 0o600); err != nil {
		t.Fatalf("write ledger file: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	poller := NewInputPoller(client, "sess-ledger", 1, fixedAuthorUserIDProvider("user-1"), stateDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller.Start(ctx)
	defer poller.Stop(context.Background())

	select {
	case ri := <-poller.Inputs():
		t.Fatalf("unexpected redelivery of an already-delivered input: %+v", ri)
	case <-time.After(300 * time.Millisecond):
	}
}
