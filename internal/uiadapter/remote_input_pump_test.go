package uiadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// newPumpTestServer builds the minimal mock chat-sync API pumpRemoteInputs'
// TestPumpRemoteInputs_AckReceivedMarksSyncSessionPoller test needs: session
// create/heartbeat/events endpoints, plus one served-once remote input and
// its consume handshake.
func newPumpTestServer(t *testing.T, remoteID, inputID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: remoteID, Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.AppendResult{InsertedCount: 0, LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatsync.Session{ID: r.PathValue("id"), Status: "running"})
	})
	served := false
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !served {
			served = true
			_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: &chatsync.SessionInput{
				ID: inputID, SessionID: remoteID, Kind: "message", Body: "hi", AuthorUserID: "auth-1",
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(chatsync.NextInput{Input: nil})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().Format(time.RFC3339)
		_ = json.NewEncoder(w).Encode(chatsync.SessionInput{
			ID: inputID, SessionID: remoteID, Kind: "message", Body: "hi",
			AuthorUserID: "auth-1", ConsumedAt: &now,
		})
	})
	return httptest.NewServer(mux)
}

// openPumpTestSession opens a real *chatsync.SyncSession with polling
// enabled against srv, then publishes one bus event to arm the deferred
// attach (session_attach.go) that starts the poller.
func openPumpTestSession(t *testing.T, ctx context.Context, srv *httptest.Server, remoteID, outboxDir string) (*events.Bus, *chatsync.SyncSession) {
	t.Helper()
	bus := events.New()
	opts := chatsync.SessionOptions{
		TokenProvider:        func(ctx context.Context, refresh bool) (string, error) { return "test-token", nil },
		ClientOptions:        chatsync.ClientOptions{BaseURL: srv.URL},
		OutboxDir:            outboxDir,
		CreateTitle:          "pump test",
		EnablePolling:        true,
		PollWaitSeconds:      1,
		AuthorUserIDProvider: func(ctx context.Context) (string, error) { return "auth-1", nil },
	}
	syncSess, err := chatsync.OpenSession(ctx, bus, remoteID, opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: remoteID,
		TurnID:    "turn:1",
		Detail:    "the first message",
		Timestamp: time.Now(),
	})
	return bus, syncSess
}

// assertReceivedLedgerContains polls outboxDir's received-ids ledger file
// until it appears (or a deadline elapses) and asserts it contains id.
func assertReceivedLedgerContains(t *testing.T, outboxDir, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	var err error
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(filepath.Join(outboxDir, "received_input_ids.json"))
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("read received-ids ledger under %s: %v", outboxDir, err)
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		t.Fatalf("decode received-ids ledger: %v", err)
	}
	for _, got := range ids {
		if got == id {
			return
		}
	}
	t.Fatalf("AckReceived did not record %q in this session's own poller ledger: %v", id, ids)
}

// TestPumpRemoteInputs_AckReceivedMarksSyncSessionPoller drives
// pumpRemoteInputs against a real *chatsync.SyncSession with polling
// enabled, and asserts the constructed ports.RemoteInputEvent's
// AckReceived, when called, results in MarkReceived on that session's own
// underlying poller (visible via its received-ids ledger file).
func TestPumpRemoteInputs_AckReceivedMarksSyncSessionPoller(t *testing.T) {
	const remoteID = "sess-pump-1"
	const inputID = "inp-pump-1"

	srv := newPumpTestServer(t, remoteID, inputID)
	defer srv.Close()

	outboxDir := filepath.Join(t.TempDir(), "outbox")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, syncSess := openPumpTestSession(t, ctx, srv, remoteID, outboxDir)
	defer func() { _ = syncSess.Stop(ctx) }()

	pool := &SessionPool{remoteInputs: make(chan ports.RemoteInputEvent, remoteInputBuffer)}
	go pool.pumpRemoteInputs(remoteID, syncSess)

	select {
	case ev := <-pool.remoteInputs:
		if ev.ID != inputID {
			t.Fatalf("event ID = %q, want %q", ev.ID, inputID)
		}
		if ev.AckReceived == nil {
			t.Fatal("event carried a nil AckReceived")
		}
		ev.AckReceived()
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pumpRemoteInputs to forward the input")
	}

	// AckReceived closes over syncSess.MarkInputReceived, which delegates
	// to the underlying poller's stateDir. The poller's own outboxDir IS
	// its stateDir (chatsync.NewInputPoller's stateDir parameter mirrors
	// OutboxDir here) - confirm the write landed on disk.
	assertReceivedLedgerContains(t, outboxDir, inputID)
}
