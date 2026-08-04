package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func newPostMessageCoordinator(t *testing.T) (Coordinator, ledger.LedgerRepository) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	return New(repo, p), repo
}

// spawnJoinedRun creates one worker task and waits for completion.
func spawnJoinedRun(t *testing.T, c Coordinator) (runID, taskID string) {
	t.Helper()
	ctx := context.Background()
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker", Input: json.RawMessage(`"hi"`)}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tasks) == 0 {
		t.Fatal("expected at least one task")
	}
	return snap.RunID, snap.Tasks[0].TaskID
}

func TestPostTaskMessagePersistsAndAnnounces(t *testing.T) {
	c, repo := newPostMessageCoordinator(t)
	ctx := context.Background()
	runID, taskID := spawnJoinedRun(t, c)

	body := "finding: lock inversion at dispatcher"
	msg, err := agentmsg.NewMessage(
		runID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: taskID, Agent: "worker"},
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		body, nil,
		agentmsg.Options{ID: "msg-post-1", Now: func() time.Time {
			return time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	var announced []ledger.LifecycleEvent
	unsub := c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		if evt.Kind == LifecycleKindTaskMessage {
			announced = append(announced, evt)
		}
	})
	defer unsub()

	if err := c.PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		t.Fatalf("PostTaskMessage: %v", err)
	}
	if len(announced) != 1 {
		t.Fatalf("announced = %d, want 1", len(announced))
	}
	assertTaskMessageAnnouncement(t, announced[0], runID, taskID, msg, body)
	assertMessageContentReplay(t, repo, ctx, runID, announced[0], msg, body)
}

func assertTaskMessageAnnouncement(t *testing.T, evt ledger.LifecycleEvent, runID, taskID string, msg agentmsg.Message, body string) {
	t.Helper()
	if evt.RunID != runID || evt.TaskID != taskID {
		t.Fatalf("event run/task = %s/%s, want %s/%s", evt.RunID, evt.TaskID, runID, taskID)
	}
	if strings.Contains(string(evt.Payload), `"body"`) {
		t.Fatalf("lifecycle payload must not contain body field: %s", evt.Payload)
	}
	var payload agentmsg.LifecyclePayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MessageID != msg.ID || payload.Kind != agentmsg.KindFinding {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Synopsis != body {
		t.Fatalf("synopsis = %q, want %q", payload.Synopsis, body)
	}
	if payload.ContentRef == "" || !strings.HasPrefix(payload.ContentRef, "ref:message:") {
		t.Fatalf("content_ref = %q", payload.ContentRef)
	}
}

func assertMessageContentReplay(t *testing.T, repo ledger.LedgerRepository, ctx context.Context, runID string, evt ledger.LifecycleEvent, msg agentmsg.Message, body string) {
	t.Helper()
	events, err := repo.ListEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range events {
		if events[i].Kind == LifecycleKindTaskMessage && events[i].ID == evt.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("task_message event not in ledger")
	}
	var payload agentmsg.LifecyclePayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	data, err := repo.LoadContent(ctx, payload.ContentRef)
	if err != nil {
		t.Fatalf("LoadContent: %v", err)
	}
	var stored agentmsg.Message
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ID != msg.ID || stored.Body != body || stored.Kind != agentmsg.KindFinding {
		t.Fatalf("stored message = %+v", stored)
	}
}

func TestPostTaskMessageRejectsInvalid(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	ctx := context.Background()
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID

	coord := c.(*coordinator)
	// Missing ID / invalid kind
	err = coord.PostTaskMessage(ctx, runID, taskID, agentmsg.Message{
		RunID: runID, Kind: "chat", Body: "x",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	// Unknown run (run_id on message must match arg so we reach GetRun).
	msg, err := agentmsg.NewMessage("missing-run", agentmsg.KindFinding,
		agentmsg.Party{TaskID: "t"}, agentmsg.Party{}, "b", nil, agentmsg.Options{ID: "msg-x"})
	if err != nil {
		t.Fatal(err)
	}
	err = coord.PostTaskMessage(ctx, "missing-run", taskID, msg)
	if err == nil {
		t.Fatal("expected missing run error")
	}
	if !strings.Contains(err.Error(), "get run") {
		t.Fatalf("want get run error, got %v", err)
	}
}

func TestPostTaskMessagePayloadIsIDAndSynopsisOnly(t *testing.T) {
	// Hostile body that looks like JSON with body field - must not appear
	// as a top-level payload key; content is only via content_ref.
	c, repo := newPostMessageCoordinator(t)
	ctx := context.Background()
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID

	secret := `{"body":"FORGED","password":"hunter2"}`
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{Role: agentmsg.ParentSentinel},
		secret, nil, agentmsg.Options{ID: "msg-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.(*coordinator).PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind != LifecycleKindTaskMessage {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["body"]; ok {
			t.Fatalf("payload has body field: %s", e.Payload)
		}
		// Synopsis is a bounded preview that is redacted under a configured
		// policy (see TestPostTaskMessageSynopsisRedacted); full body
		// retrieval is via content_ref only.
		if m["message_id"] != "msg-secret" {
			t.Fatalf("message_id = %v", m["message_id"])
		}
	}
}

func TestPostTaskMessageSynopsisRedacted(t *testing.T) {
	// Install a process-wide redaction policy for the duration of this test
	// and restore whatever was active before. Tests in this package are
	// sequential (no t.Parallel), so the global swap cannot race a sibling.
	policy, err := redact.Compile([]string{`(?i)secret-[0-9]+`}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	old := redact.Current()
	redact.SetPolicy(policy)
	defer redact.SetPolicy(old)

	c, repo := newPostMessageCoordinator(t)
	ctx := context.Background()
	runID, taskID := spawnJoinedRun(t, c)

	// The secret sits well inside the 256-byte synopsis window, so an
	// unredacted synopsis would carry it verbatim.
	body := "finding: token secret-1234 leaked into logs"
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: taskID, Agent: "worker"},
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		body, nil, agentmsg.Options{ID: "msg-redact"})
	if err != nil {
		t.Fatal(err)
	}

	var announced []ledger.LifecycleEvent
	unsub := c.SubscribeLifecycle(func(evt ledger.LifecycleEvent) {
		if evt.Kind == LifecycleKindTaskMessage {
			announced = append(announced, evt)
		}
	})
	defer unsub()

	if err := c.PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		t.Fatalf("PostTaskMessage: %v", err)
	}
	if len(announced) != 1 {
		t.Fatalf("announced = %d, want 1", len(announced))
	}
	if strings.Contains(string(announced[0].Payload), "secret-1234") {
		t.Fatalf("lifecycle payload leaks secret: %s", announced[0].Payload)
	}
	var payload agentmsg.LifecyclePayload
	if err := json.Unmarshal(announced[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Synopsis, "[redacted]") {
		t.Fatalf("synopsis = %q, want redacted placeholder", payload.Synopsis)
	}
	if payload.MessageID != msg.ID {
		t.Fatalf("message_id = %q, want %q", payload.MessageID, msg.ID)
	}
	if payload.Kind != agentmsg.KindFinding {
		t.Fatalf("kind = %q, want %q", payload.Kind, agentmsg.KindFinding)
	}
	if payload.ContentRef == "" || !strings.HasPrefix(payload.ContentRef, "ref:message:") {
		t.Fatalf("content_ref = %q", payload.ContentRef)
	}

	// The durable ledger copy behind content_ref keeps the raw body:
	// redaction applies to the announcement surface, never to stored content.
	data, err := repo.LoadContent(ctx, payload.ContentRef)
	if err != nil {
		t.Fatalf("LoadContent: %v", err)
	}
	if !strings.Contains(string(data), "secret-1234") {
		t.Fatalf("stored content lost the raw body: %s", data)
	}
}

func TestPostTaskMessageInputValidation(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	ctx := context.Background()
	coord := c.(*coordinator)
	msg, err := agentmsg.NewMessage("run-x", agentmsg.KindFinding,
		agentmsg.Party{TaskID: "t"}, agentmsg.Party{}, "b", nil, agentmsg.Options{ID: "msg-v"})
	if err != nil {
		t.Fatal(err)
	}
	if err := coord.PostTaskMessage(ctx, "", "t", msg); err == nil {
		t.Fatal("empty run_id")
	}
	if err := coord.PostTaskMessage(ctx, "run-x", "", msg); err == nil {
		t.Fatal("empty task_id")
	}
	// run_id mismatch between args and message
	msg.RunID = "other-run"
	if err := coord.PostTaskMessage(ctx, "run-x", "t", msg); err == nil {
		t.Fatal("run_id mismatch")
	}
}

func TestPostTaskMessageStampsEmptyProvenance(t *testing.T) {
	c, repo := newPostMessageCoordinator(t)
	ctx := context.Background()
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID

	// Empty RunID and From.TaskID - server stamps them.
	msg := agentmsg.Message{
		ID: "msg-stamp", Kind: agentmsg.KindFinding,
		From: agentmsg.Party{Agent: "worker"}, // no TaskID
		To:   agentmsg.Party{Role: agentmsg.ParentSentinel},
		Body: "stamped",
	}
	if err := c.(*coordinator).PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind != LifecycleKindTaskMessage {
			continue
		}
		var p agentmsg.LifecyclePayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		data, err := repo.LoadContent(ctx, p.ContentRef)
		if err != nil {
			t.Fatal(err)
		}
		var stored agentmsg.Message
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.RunID != runID {
			t.Fatalf("stored RunID = %q, want %q", stored.RunID, runID)
		}
		if stored.From.TaskID != taskID {
			t.Fatalf("stored From.TaskID = %q, want %q", stored.From.TaskID, taskID)
		}
		return
	}
	t.Fatal("no task_message event")
}

func TestPostTaskMessageMissingTask(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	ctx := context.Background()
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	msg, _ := agentmsg.NewMessage(snap.RunID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: "nope"}, agentmsg.Party{}, "b", nil, agentmsg.Options{ID: "msg-m"})
	if err := c.(*coordinator).PostTaskMessage(ctx, snap.RunID, "no-such-task", msg); err == nil {
		t.Fatal("expected missing task error")
	}
}

func TestAssertPayloadIsAnnouncement(t *testing.T) {
	if err := assertPayloadIsAnnouncement([]byte(`not-json`)); err == nil {
		t.Fatal("non-json")
	}
	if err := assertPayloadIsAnnouncement([]byte(`{"body":"x","message_id":"m"}`)); err == nil {
		t.Fatal("body field")
	}
	if err := assertPayloadIsAnnouncement([]byte(`{"message_id":"m","extra":1}`)); err == nil {
		t.Fatal("unexpected field")
	}
	if err := assertPayloadIsAnnouncement([]byte(`{"message_id":"m","kind":"finding","synopsis":"s"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodeMessageForLedger(t *testing.T) {
	msg, err := agentmsg.NewMessage("run-1", agentmsg.KindFinding,
		agentmsg.Party{TaskID: "t"}, agentmsg.Party{}, "body", nil, agentmsg.Options{ID: "msg-enc"})
	if err != nil {
		t.Fatal(err)
	}
	data, ref := encodeMessageForLedger(msg)
	if len(data) == 0 || ref == "" || !strings.HasPrefix(ref, "ref:message:") {
		t.Fatalf("data/ref = %d / %q", len(data), ref)
	}
}

// Parent steers must persist as From parent (IsParent), never with child TaskID.
func TestPostTaskMessageParentFromNotStampedWithTaskID(t *testing.T) {
	c, repo := newPostMessageCoordinator(t)
	ctx := context.Background()
	runID, taskID := spawnJoinedRun(t, c)
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindSteer,
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		agentmsg.Party{TaskID: taskID},
		"parent steer body", nil, agentmsg.Options{ID: "msg-parent-from"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		t.Fatal(err)
	}
	list, err := c.ListRunMessages(ctx, runID, taskID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	full, err := c.LoadMessageBody(ctx, list[0].ContentRef)
	if err != nil {
		t.Fatal(err)
	}
	if !full.From.IsParent() {
		t.Fatalf("From must remain parent, got %+v", full.From)
	}
	if full.From.TaskID != "" {
		t.Fatalf("parent From must not carry task TaskID, got %q", full.From.TaskID)
	}
	_ = repo
}

func TestPostTaskMessageRespectsMaxBodyBytes(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	ctx := context.Background()
	runID, taskID := spawnJoinedRun(t, c)
	// Raise budget above default so a 3000-byte body is accepted.
	c = c.WithMessagingLimits(4096, 0)
	body := strings.Repeat("x", 3000)
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: taskID, Agent: "w"}, agentmsg.Party{Role: agentmsg.ParentSentinel},
		body, nil, agentmsg.Options{ID: "msg-big", MaxBodyBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		t.Fatalf("should accept body under raised max: %v", err)
	}
	// Shrink budget: same size body must fail.
	c = c.WithMessagingLimits(100, 0)
	msg2 := agentmsg.Message{
		ID: "msg-big2", RunID: runID, Kind: agentmsg.KindFinding,
		From: agentmsg.Party{TaskID: taskID, Agent: "w"},
		Body: body,
	}
	if err := c.PostTaskMessage(ctx, runID, taskID, msg2); err == nil {
		t.Fatal("expected reject under reduced maxBodyBytes")
	}
}

func TestPostTaskMessageDefaultBodyBudgetWhenUnset(t *testing.T) {
	// maxBodyBytes forced to 0 falls back to DefaultMaxBodyBytes.
	c, _ := newPostMessageCoordinator(t)
	coord := c.(*coordinator)
	coord.maxBodyBytes = 0
	ctx := context.Background()
	runID, taskID := spawnJoinedRun(t, c)
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: taskID, Agent: "w"}, agentmsg.Party{},
		"ok", nil, agentmsg.Options{ID: "msg-def"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		t.Fatal(err)
	}
	// Body over default still fails.
	msg2 := agentmsg.Message{
		ID: "msg-over", RunID: runID, Kind: agentmsg.KindFinding,
		From: agentmsg.Party{TaskID: taskID, Agent: "w"},
		Body: strings.Repeat("y", agentmsg.DefaultMaxBodyBytes+1),
	}
	if err := c.PostTaskMessage(ctx, runID, taskID, msg2); err == nil {
		t.Fatal("expected default budget rejection")
	}
}

// failingStoreRepo wraps MemoryLedgerRepository and fails StoreContent/AppendEvent.
type failingStoreRepo struct {
	ledger.LedgerRepository
	failStore  bool
	failAppend bool
}

func (f *failingStoreRepo) StoreContent(ctx context.Context, ref string, data []byte) error {
	if f.failStore {
		return errors.New("store boom")
	}
	return f.LedgerRepository.StoreContent(ctx, ref, data)
}

func (f *failingStoreRepo) AppendEvent(ctx context.Context, event ledger.LifecycleEvent) error {
	if f.failAppend {
		return errors.New("append boom")
	}
	return f.LedgerRepository.AppendEvent(ctx, event)
}

func TestPostTaskMessageStoreAndAppendFailures(t *testing.T) {
	ctx := context.Background()
	// Seed a real run/task in memory, then wrap the same repo for failures.
	base := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(base, p)
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID

	msg, err := agentmsg.NewMessage(runID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{}, "b", nil, agentmsg.Options{ID: "msg-f1"})
	if err != nil {
		t.Fatal(err)
	}

	// StoreContent failure
	failStore := &failingStoreRepo{LedgerRepository: base, failStore: true}
	coordStore := New(failStore, p).(*coordinator)
	if err := coordStore.PostTaskMessage(ctx, runID, taskID, msg); err == nil || !strings.Contains(err.Error(), "store content") {
		t.Fatalf("store fail: %v", err)
	}

	// AppendEvent failure
	msg2 := msg
	msg2.ID = "msg-f2"
	failAppend := &failingStoreRepo{LedgerRepository: base, failAppend: true}
	coordAppend := New(failAppend, p).(*coordinator)
	if err := coordAppend.PostTaskMessage(ctx, runID, taskID, msg2); err == nil || !strings.Contains(err.Error(), "append event") {
		t.Fatalf("append fail: %v", err)
	}
}
