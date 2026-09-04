package chatsync

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestSessionSyncObservabilityFlow proves the production-shaped path from a
// bus event to the durable API append. It uses the real session, projector,
// outbox, uploader, HTTP client, and authenticated fake server together.
// This is intentionally stronger than a client-header unit test: a change in
// the asynchronous handoff can make correlation disappear even when the HTTP
// helper itself remains correct.
func TestSessionSyncObservabilityFlow(t *testing.T) {
	f := newFakeAPI(t)
	remoteID := f.NewSession("observability")
	var logs synchronizedBuffer
	telemetry := NewSyncTelemetry(slog.New(slog.NewTextHandler(&logs, nil)))
	bus := events.New()

	s, err := OpenSession(context.Background(), bus, remoteID, SessionOptions{
		TokenProvider:    testTokenProvider,
		ClientOptions:    ClientOptions{BaseURL: f.URL()},
		ProjectorOptions: ProjectorOptions{WriterID: "writer-integration"},
		OutboxDir:        t.TempDir(),
		RemoteSessionID:  remoteID,
		Telemetry:        telemetry,
		HeartbeatPeriod:  time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), RecommendedStopTimeout)
		defer cancel()
		_ = s.Stop(stopCtx)
	}()

	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: remoteID,
		TurnID:    "turn-integration",
		Detail:    "integration event",
		Timestamp: time.Now(),
	})

	waitUntil(t, "event to reach the fake API", func() bool { return f.LastSeq(remoteID) >= 1 })

	var appendRequest *recordedRequest
	for _, request := range f.Requests() {
		if request.Method == "POST" && request.Target == "/v1/chat-sessions/"+remoteID+"/events" {
			copy := request
			appendRequest = &copy
			break
		}
	}
	if appendRequest == nil {
		t.Fatal("no append request reached the fake API")
	}
	if appendRequest.UploadBatchID == "" {
		t.Fatal("append request has no upload batch id")
	}
	if appendRequest.WriterID != "writer-integration" {
		t.Fatalf("writer id = %q, want writer-integration", appendRequest.WriterID)
	}

	snapshot := telemetry.Snapshot()
	if snapshot.Produced != 1 || snapshot.Projected != 1 || snapshot.Appended != 1 || snapshot.Uploaded != 1 {
		t.Fatalf("unexpected sync telemetry: %+v", snapshot)
	}
	if snapshot.LastAckSeq != 1 || snapshot.OutboxDepth != 0 || snapshot.LastSuccessAt.IsZero() {
		t.Fatalf("upload telemetry did not record the acknowledged append: %+v", snapshot)
	}
	if !bytes.Contains([]byte(logs.String()), []byte("upload_batch_id="+appendRequest.UploadBatchID)) {
		t.Fatalf("telemetry logs do not contain upload batch id %q: %s", appendRequest.UploadBatchID, logs.String())
	}
}
