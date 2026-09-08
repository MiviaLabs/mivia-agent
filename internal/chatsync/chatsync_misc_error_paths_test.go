package chatsync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestFlushOutcomeString covers all three named values plus the default
// branch; String() is never called on the hot path (only its int identity
// is compared), so nothing else exercises it.
func TestFlushOutcomeString(t *testing.T) {
	cases := map[flushOutcome]string{
		outcomeStop:     "stop",
		outcomeRecover:  "recover",
		outcomeRetry:    "retry",
		flushOutcome(9): "retry", // unknown value falls to the default branch
	}
	for outcome, want := range cases {
		if got := outcome.String(); got != want {
			t.Errorf("flushOutcome(%d).String() = %q, want %q", outcome, got, want)
		}
	}
}

// TestHeartbeatRunner_StartIsIdempotent pins Start's already-running guard:
// a second Start call while the runner is live must not spawn a second
// background goroutine.
func TestHeartbeatRunner_StartIsIdempotent(t *testing.T) {
	client, err := NewClient(testTokenProvider, ClientOptions{BaseURL: "http://unused.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHeartbeatRunner(client, "sess-1", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)
	h.Start(ctx) // must be a no-op, not a second goroutine
	h.Stop(ctx)
}

// TestProbeEndpoint_InvalidURLSurfaces pins the request-construction guard,
// distinct from TestProbeEndpointReportsWhatItReached's reachable/
// unreachable/timeout cases: a baseURL with a control character makes
// http.NewRequestWithContext itself fail before any network call.
func TestProbeEndpoint_InvalidURLSurfaces(t *testing.T) {
	ok, detail := ProbeEndpoint(context.Background(), "http://\x7f.invalid")
	if ok {
		t.Fatal("ProbeEndpoint reported reachable for an invalid URL")
	}
	if !strings.HasPrefix(detail, "invalid url") {
		t.Fatalf("detail = %q, want the invalid-url prefix", detail)
	}
}

// TestValidateRemoteInput_NilInputIsRejected pins the earliest guard,
// distinct from every other rejection test in remote_input_validate_test.go
// which all pass a real *SessionInput.
func TestValidateRemoteInput_NilInputIsRejected(t *testing.T) {
	p := &InputPoller{sessionID: "sess-1"}
	if _, reason := p.validateRemoteInput(context.Background(), nil); reason != "empty input" {
		t.Fatalf("reason = %q, want %q", reason, "empty input")
	}
}

// TestValidateRemoteInput_RejectsInvalidUTF8 pins the UTF-8 guard, distinct
// from TestInputPoller_RejectsControlCharsInBody (a valid-UTF8 body holding
// a disallowed control rune) and TestInputPoller_RejectsBidiOverrideInBody.
func TestValidateRemoteInput_RejectsInvalidUTF8(t *testing.T) {
	p := &InputPoller{sessionID: "sess-1", authorUserID: fixedAuthorUserIDProvider("user-1")}
	in := &SessionInput{SessionID: "sess-1", Kind: "message", Body: "hi\xff\xfe", AuthorUserID: "user-1"}
	if _, reason := p.validateRemoteInput(context.Background(), in); reason != "body is not valid UTF-8" {
		t.Fatalf("reason = %q, want %q", reason, "body is not valid UTF-8")
	}
}

// TestHasDisallowedControlChar_AllowsOrdinaryWhitespace pins the allowed
// set directly: tab, newline, and carriage return must never be flagged,
// only genuinely disallowed control runes are.
func TestHasDisallowedControlChar_AllowsOrdinaryWhitespace(t *testing.T) {
	if hasDisallowedControlChar("line one\nline two\ttabbed\rcr") {
		t.Error("tab, newline, and carriage return must be allowed")
	}
	if !hasDisallowedControlChar("bad\x01char") {
		t.Error("a genuine control character must still be rejected")
	}
}

// TestProjectSubagentAssistantAndThinking_EmptyContentProjectsNothing pins
// the empty-content early return in both projectSubagentAssistant and
// projectSubagentThinking: an event with no text produces no wire event,
// distinct from every other subagent-prose test which always carries text.
func TestProjectSubagentAssistantAndThinking_EmptyContentProjectsNothing(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	if got := p.Project(subagentEvent(events.KindAssistant, "task-1", "", "")); len(got) != 0 {
		t.Errorf("empty-content subagent assistant projected %v, want nothing", got)
	}
	if got := p.Project(subagentEvent(events.KindThinking, "task-1", "", "")); len(got) != 0 {
		t.Errorf("empty-content subagent thinking projected %v, want nothing", got)
	}
}

// TestOpenSession_OutboxOpenErrorSurfaces pins OpenSession's outbox-open
// guard: a directory OpenOutbox cannot use (a file occupying the path)
// must fail OpenSession before any network call, rather than opening a
// session with no durable backing.
func TestOpenSession_OutboxOpenErrorSurfaces(t *testing.T) {
	blocker := t.TempDir()
	blockerFile := blocker + "/outbox-is-a-file"
	if err := writeFileDurably(blocker, "outbox-is-a-file", []byte("x")); err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	_, err := OpenSession(context.Background(), bus, "sess-1", SessionOptions{
		TokenProvider: testTokenProvider,
		ClientOptions: ClientOptions{BaseURL: "http://unused.invalid"},
		OutboxDir:     blockerFile + "/sub",
	})
	if err == nil {
		t.Fatal("OpenSession accepted an outbox dir it cannot open")
	}
}

// TestSyncSession_StopReasonAndInputsDefaults pins the two zero-value
// branches: StopReason before anything ever calls stopTerminally, and
// Inputs() on a session with no poller wired.
func TestSyncSession_StopReasonAndInputsDefaults(t *testing.T) {
	s := &SyncSession{}
	if got := s.StopReason(); got != "" {
		t.Errorf("StopReason on a fresh session = %q, want empty", got)
	}
	if got := s.Inputs(); got != nil {
		t.Errorf("Inputs with no poller = %v, want nil", got)
	}
}
