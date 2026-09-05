package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// workflowStatusEvent builds one liveness event in the shape
// SessionPool.watchWorkflowProgressLocked publishes.
func workflowStatusEvent(run, step string, since time.Time, active bool) uievent.Event {
	return uievent.Event{
		Kind: uievent.KindWorkflowStatus,
		At:   fixedNow(),
		Body: uievent.WorkflowStatusBody{Run: run, Step: step, Since: since, Active: active},
	}
}

// statusLine renders the screen and returns its bottom status row.
func statusLine(t *testing.T, s Screen) string {
	t.Helper()
	return ansi.Strip(s.statusRow())
}

// TestQuietStepStaysVisibleOnTheStatusRow is the regression test for the gap
// the workflow notice stream could not close on its own.
//
// A step's start and its end are transitions and reach the transcript as
// notices. The hours BETWEEN them produce no transition at all, so a run
// whose current step is simply slow looked identical to a run that had
// wedged: the last line in the record was "step build started", however long
// ago that was. The row states what is running and for how long.
func TestQuietStepStaysVisibleOnTheStatusRow(t *testing.T) {
	s := newTestScreen(t)
	s.SetWorkflowStatus(make(chan uievent.Event, 1))

	started := fixedNow().Add(-47 * time.Minute)
	next, _ := s.handleWorkflowStatus(workflowStatusEvent("wfr-1", "build", started, true))
	got, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleNotice returned %T, want Screen", next)
	}
	sized, _ := got.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got, ok = sized.(Screen)
	if !ok {
		t.Fatalf("Update returned %T, want Screen", sized)
	}

	row := statusLine(t, got)
	if !strings.Contains(row, "wfr-1") {
		t.Fatalf("status row %q names no run; the operator needs the id workflow_status and workflow_cancel take", row)
	}
	if !strings.Contains(row, "on step build") {
		t.Fatalf("status row %q does not name the running step", row)
	}
	if !strings.Contains(row, "47m") {
		t.Fatalf("status row %q does not say how long the step has been running", row)
	}
}

// TestWorkflowStatusNeverEntersTheTranscript pins the split that makes the
// row worth having: liveness replaces itself on the row, and the record keeps
// only transitions. A status event reaching the transcript would restore the
// per-tick flood the heartbeat policy exists to prevent.
func TestWorkflowStatusNeverEntersTheTranscript(t *testing.T) {
	s := newTestScreen(t)
	s.SetWorkflowStatus(make(chan uievent.Event, 1))

	next, _ := s.handleWorkflowStatus(workflowStatusEvent("wfr-1", "build", fixedNow(), true))
	got, _ := next.(Screen)
	for i := 0; i < 20; i++ {
		next, _ = got.handleWorkflowStatus(workflowStatusEvent("wfr-1", "build", fixedNow(), true))
		got, _ = next.(Screen)
	}

	if n := len(got.transcript.Blocks()); n != 0 {
		t.Fatalf("21 liveness events produced %d transcript blocks, want 0", n)
	}
}

// TestFinishedRunClearsTheStatusRow pins the terminal case: a row that keeps
// naming a run that ended is worse than no row, because it reports work that
// is not happening.
func TestFinishedRunClearsTheStatusRow(t *testing.T) {
	s := newTestScreen(t)
	s.SetWorkflowStatus(make(chan uievent.Event, 1))

	next, _ := s.handleWorkflowStatus(workflowStatusEvent("wfr-1", "build", fixedNow().Add(-time.Hour), true))
	running, _ := next.(Screen)
	if statusLine(t, running) == "" {
		t.Fatal("an active run left the status row empty")
	}

	next, _ = running.handleWorkflowStatus(workflowStatusEvent("wfr-1", "", fixedNow(), false))
	done, _ := next.(Screen)
	if line := done.workflowStatusText(); line != "" {
		t.Fatalf("a finished run still claims the status row: %q", line)
	}
}

// TestWorkflowElapsedUsesTheCoarsestUsefulUnit pins the formatting contract.
// A second-resolution counter on a multi-hour run is motion without
// information, and a duration the clock cannot explain must not claim
// precision it does not have.
func TestWorkflowElapsedUsesTheCoarsestUsefulUnit(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want string
	}{
		{"sub-second says nothing", 400 * time.Millisecond, ""},
		{"negative says nothing", -5 * time.Minute, ""},
		{"seconds under a minute", 42 * time.Second, "42s"},
		{"minutes under an hour", 47 * time.Minute, "47m"},
		{"hours carry their minutes", 2*time.Hour + 5*time.Minute, "2h05m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workflowElapsed(tc.in); got != tc.want {
				t.Fatalf("workflowElapsed(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestWorkflowStatusReaderSurvivesAnUnreadableBody proves the re-arm is
// unconditional. A value this screen cannot read must be skipped, never
// treated as end-of-stream: dropping the re-arm there would freeze the status
// row on whatever it last showed, for the rest of the run, because of one
// malformed earlier value - a row that keeps reporting a step which finished
// hours ago is worse than no row.
func TestWorkflowStatusReaderSurvivesAnUnreadableBody(t *testing.T) {
	s := newTestScreen(t)
	s.SetWorkflowStatus(make(chan uievent.Event, 1))

	_, rearm := s.handleWorkflowStatus(uievent.Event{
		Kind: uievent.KindWorkflowStatus,
		Body: uievent.NoticeBody{Text: "wrong shape"},
	})
	if rearm == nil {
		t.Fatal("an unreadable status body stopped the reader: the row would freeze on its last value")
	}
}

// TestWorkflowStatusReaderDisabledWithoutAChannel pins the nil case: every
// embedded thread Screen, and any Screen a test builds without wiring one,
// must arm no goroutine at all.
func TestWorkflowStatusReaderDisabledWithoutAChannel(t *testing.T) {
	s := newTestScreen(t)
	if cmd := s.awaitWorkflowStatus(); cmd != nil {
		t.Fatal("awaitWorkflowStatus armed a reader with no channel set")
	}
}

// TestAwaitWorkflowStatusCmdDeliversAndSignalsClose exercises
// awaitWorkflowStatus's returned tea.Cmd directly - the prior tests only
// checked whether a Cmd was armed, never ran the closure that actually reads
// the channel.
func TestAwaitWorkflowStatusCmdDeliversAndSignalsClose(t *testing.T) {
	ch := make(chan uievent.Event, 1)
	s := newTestScreen(t)
	s.SetWorkflowStatus(ch)

	ev := workflowStatusEvent("wfr-1", "build", time.Now(), true)
	ch <- ev
	msg := s.awaitWorkflowStatus()()
	got, ok := msg.(workflowStatusMsg)
	if !ok {
		t.Fatalf("msg = %#v, want workflowStatusMsg", msg)
	}
	if got.event.Body != ev.Body {
		t.Fatalf("delivered event = %+v, want %+v", got.event, ev)
	}

	close(ch)
	if msg := s.awaitWorkflowStatus()(); msg != nil {
		t.Fatalf("msg from a closed channel = %#v, want nil", msg)
	}
}
