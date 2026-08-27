package stream

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func loadFixture(t *testing.T) []uievent.Event {
	t.Helper()
	events, err := DefaultFixture()
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// TestRenderGolden pins the plain-renderer output for the recorded
// conversation fixture (wireframes-panes.md section 4). Regenerate with
// -update if the renderer's format intentionally changes.
func TestRenderGolden(t *testing.T) {
	events := loadFixture(t)
	var buf bytes.Buffer
	if err := Render(&buf, events); err != nil {
		t.Fatal(err)
	}

	goldenPath := filepath.Join("testdata", "golden", "conversation.txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Errorf("output does not match golden %s\n--- got ---\n%s\n--- want ---\n%s", goldenPath, buf.String(), want)
	}
}

func TestRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty event slice, got %q", buf.String())
	}
}

func TestRenderUnknownBodyErrors(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, []uievent.Event{{Kind: "bogus", Body: nil}})
	if err == nil {
		t.Fatal("expected error for unhandled body type")
	}
}

// errAtWriter fails on its Nth Write call and succeeds on every other.
// Sweeping failAt across the full range of Write calls a render makes
// exercises every "write, then propagate the error" branch in the
// renderer in one parametrized test, instead of one contrived test per
// call site.
type errAtWriter struct {
	failAt int
	calls  int
}

var errBoom = errors.New("boom")

func (w *errAtWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, errBoom
	}
	return len(p), nil
}

func TestRenderPropagatesWriteErrors(t *testing.T) {
	events := loadFixture(t)

	counter := &errAtWriter{failAt: -1}
	if err := Render(counter, events); err != nil {
		t.Fatalf("baseline render failed: %v", err)
	}
	total := counter.calls
	if total == 0 {
		t.Fatal("baseline render made no Write calls")
	}

	for n := 1; n <= total; n++ {
		w := &errAtWriter{failAt: n}
		if err := Render(w, events); err == nil {
			t.Errorf("expected Render to fail when write #%d of %d fails", n, total)
		}
	}
}

func TestRenderEveryKindNoPanic(t *testing.T) {
	// Every Kind in the fixture must be handled; a new Kind added to
	// uievent without a stream.go case should fail loudly, not panic or
	// silently no-op forever.
	events := loadFixture(t)
	seen := map[uievent.Kind]bool{}
	for _, ev := range events {
		seen[ev.Kind] = true
	}
	for _, k := range []uievent.Kind{
		uievent.KindTurnStart, uievent.KindTextDelta, uievent.KindTextEnd,
		uievent.KindReasoning, uievent.KindToolStart, uievent.KindToolOutput,
		uievent.KindToolEnd, uievent.KindPlan, uievent.KindNotice,
		uievent.KindError, uievent.KindUsage, uievent.KindTurnEnd,
	} {
		if !seen[k] {
			t.Errorf("fixture is missing coverage for Kind %s", k)
		}
	}
}

// TestRenderErrorEmptyTextIsSuppressed pins the defensive guard on
// renderError: an empty-text, non-fatal KindError produces no rendered
// line. A bug that ever emits a malformed empty-text error (a stray
// tool-start with Name="error", a runtime surface that fails closed,
// a translator that omits the body text) must NOT leak as a bare
// "  error" line in the transcript. The KindError event itself is
// preserved on the channel; only the renderer suppresses it. A fatal
// error with empty text still renders, because "fatal" alone carries
// the meaning the user needs.
func TestRenderErrorEmptyTextIsSuppressed(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, []uievent.Event{{
		Kind: uievent.KindError,
		Body: uievent.ErrorBody{Text: "", Fatal: false},
	}}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("empty-text non-fatal KindError must not render; got %q", buf.String())
	}
}

// TestRenderErrorEmptyTextFatalStillRenders pins the asymmetric case:
// a fatal error with empty text still renders so the user sees the
// terminal status, even though the body has no detail. A fatal with
// empty text is more likely a translator regression than a runtime
// noise; the user needs to see "fatal" regardless.
func TestRenderErrorEmptyTextFatalStillRenders(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, []uievent.Event{{
		Kind: uievent.KindError,
		Body: uievent.ErrorBody{Text: "", Fatal: true},
	}}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if buf.Len() == 0 {
		t.Errorf("empty-text fatal KindError must render; got empty buffer")
	}
	if !strings.Contains(buf.String(), "fatal") {
		t.Errorf("empty-text fatal KindError must mention 'fatal' in the rendered output; got %q", buf.String())
	}
}

// TestRenderToolStartNameErrorDoesNotMisformat pins the related
// risk: a KindToolStart with Name="error" formats as
// "v error ...". That is documented stream behaviour behaviour
// (see renderToolStart at line 51-53), and is the most plausible
// source of the user's "v error" screenshot when no producer of
// KindError was reachable. The regression test pins the format
// so a future change that conflates tool output and error events
// surfaces visibly.
func TestRenderToolStartNameErrorDoesNotMisformat(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, []uievent.Event{{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "x", Name: "error"},
	}}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "v error") {
		t.Errorf("expected 'v error' prefix from KindToolStart{Name:'error'}; got %q", buf.String())
	}
	// Sanity: a KindError with empty text on the SAME line must NOT
	// appear (i.e. the early-return guard from
	// TestRenderErrorEmptyTextIsSuppressed holds even when a tool-start
	// of the same name precedes it).
	if strings.Contains(buf.String(), "  error") {
		t.Errorf("empty-text KindError must not render even when a tool-start of the same name precedes it; got %q", buf.String())
	}
}

// TestRenderSmoke_RealisticOneUserInput pins the doubled-message
// regression at the renderer boundary. This is the smoke test
// surface for the new ui shipped in Phase 3 (commit de1d2e70):
// when `--demo=false` produced two `KindTurnStart` and two
// identical `KindTextEnd` events on a single user input, the bug
// appeared as the doubled assistant message in the rendered
// transcript. The offline unit tests in
// internal/uiadapter/conversation_test.go already pin the channel-
// side invariant (TestSend_FullTurn_ExactlyOneOfEach and
// TestSend_FullTurn_TextEndContentExact). This smoke test mirrors
// the same scenario from the renderer's perspective so a future
// regression that produces duplicates AT the renderer but not at
// the channel (a renderer-side dedup gone wrong, a single channel
// event rendered twice) surfaces here too.
//
// The fixture drives a realistic event sequence for one user input
// "hi": turn.start with the input, one text.delta chunk, one
// text.end with the assistant's reply, one notice (cache_usage),
// one notice (token_usage), one turn.end "completed". If any kind
// appears twice where it should appear once, the test fails
// because that is the exact doubled-message shape the user saw.
func TestRenderSmoke_RealisticOneUserInput(t *testing.T) {
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	events := []uievent.Event{
		{Kind: uievent.KindTurnStart, Seq: 1, At: at, Body: uievent.TurnStartBody{Input: "hi"}},
		{Kind: uievent.KindTextDelta, Seq: 2, At: at, Body: uievent.TextDeltaBody{Text: "Hi there! "}},
		{Kind: uievent.KindTextEnd, Seq: 3, At: at, Body: uievent.TextEndBody{Text: "Hi there! How can I help you today?"}},
		{Kind: uievent.KindNotice, Seq: 4, At: at, Body: uievent.NoticeBody{Text: "prompt cache: 2176/2231 tokens cached (97%)"}},
		{Kind: uievent.KindNotice, Seq: 5, At: at, Body: uievent.NoticeBody{Text: "estimate 1920 vs actual 2231 (ratio 1.16)"}},
		{Kind: uievent.KindTurnEnd, Seq: 6, At: at, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
	var buf bytes.Buffer
	if err := Render(&buf, events); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()

	// The user's assistant message ("Hi there! How can I help you
	// today?") must appear exactly once. A second occurrence
	// means a duplicated text.end reached the channel.
	if c := strings.Count(out, "Hi there! How can I help you today?"); c != 1 {
		t.Errorf("assistant message occurrence=%d, want 1 (the doubled-message bug surfaces as 2)", c)
	}

	// The user's input ("hi") must appear exactly once. A second
	// occurrence means a duplicated turn.start reached the channel.
	if c := strings.Count(out, "> hi"); c != 1 {
		t.Errorf("user input '> hi' occurrence=%d, want 1 (a duplicated turn.start surfaces as 2)", c)
	}

	// The notice lines must each appear exactly once. Duplicates
	// surface as doubled per-turn accounting rows.
	for _, line := range []string{
		"prompt cache: 2176/2231 tokens cached (97%)",
		"estimate 1920 vs actual 2231 (ratio 1.16)",
	} {
		if c := strings.Count(out, line); c != 1 {
			t.Errorf("notice line occurrence=%d for %q, want 1", c, line)
		}
	}

	// The terminal "(turn completed)" marker must appear exactly
	// once. A missing or duplicated terminal surfaces as a stuck
	// renderer or a duplicate terminator.
	if c := strings.Count(out, "(turn completed)"); c != 1 {
		t.Errorf("'(turn completed)' occurrence=%d, want 1", c)
	}
}

// TestRenderSmoke_IntermediateReasoningAndNoticesDuringStream pins the event deduplication
// invariant when reasoning deltas and intermediate usage events arrive during live text streaming.
func TestRenderSmoke_IntermediateReasoningAndNoticesDuringStream(t *testing.T) {
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	events := []uievent.Event{
		{Kind: uievent.KindTurnStart, Seq: 1, At: at, Body: uievent.TurnStartBody{Input: "summarize"}},
		{Kind: uievent.KindReasoning, Seq: 2, At: at, Body: uievent.ReasoningDeltaBody{Text: "thinking step 1"}},
		{Kind: uievent.KindReasoning, Seq: 3, At: at, Body: uievent.ReasoningDeltaBody{WordCount: 15}},
		{Kind: uievent.KindTextDelta, Seq: 4, At: at, Body: uievent.TextDeltaBody{Text: "Summary complete."}},
		{Kind: uievent.KindNotice, Seq: 5, At: at, Body: uievent.NoticeBody{Text: "tokens cached: 500"}},
		{Kind: uievent.KindUsage, Seq: 6, At: at, Body: uievent.UsageBody{InputTokens: 500, OutputTokens: 10, CachedTokens: 500, CostUSD: 0.001}},
		{Kind: uievent.KindTextEnd, Seq: 7, At: at, Body: uievent.TextEndBody{Text: "Summary complete."}},
		{Kind: uievent.KindTurnEnd, Seq: 8, At: at, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
	var buf bytes.Buffer
	if err := Render(&buf, events); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()

	if c := strings.Count(out, "Summary complete."); c != 1 {
		t.Errorf("assistant text occurrence=%d, want 1 in:\n%s", c, out)
	}
	if c := strings.Count(out, "> summarize"); c != 1 {
		t.Errorf("user input occurrence=%d, want 1", c)
	}
	if c := strings.Count(out, "reasoning 15 words hidden"); c != 1 {
		t.Errorf("reasoning header occurrence=%d, want 1", c)
	}
	if c := strings.Count(out, "tokens cached: 500"); c != 1 {
		t.Errorf("notice occurrence=%d, want 1", c)
	}
}
