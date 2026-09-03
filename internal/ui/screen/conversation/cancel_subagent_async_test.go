// cancel_subagent_async_test.go is the regression for the freeze both
// subagent cancel paths used to cause: they called through to the
// coordinator INLINE, from a handler bubbletea runs on its single Update
// goroutine. coordinator.CancelTask blocks for its whole wait budget
// (5 seconds) while it waits for the task to unwind, so pressing the key
// stalled rendering and every message - ctrl+c included.
//
// Both handlers now return a tea.Cmd and report their outcome as a message.
// These tests prove the key press returns while the seam is still blocked,
// and that the eventual outcome still reaches the screen.
package conversation

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// blockingCancelThreads is a ports.SubagentThreads whose cancels block until
// release is closed - the test double for a slow coordinator seam.
type blockingCancelThreads struct {
	stubThreads
	release chan struct{}

	mu    sync.Mutex
	calls int
}

func (b *blockingCancelThreads) entered() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func (b *blockingCancelThreads) block() {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	<-b.release
}

func (b *blockingCancelThreads) CancelSubagentTask(string) (bool, error) {
	b.block()
	return true, nil
}

func (b *blockingCancelThreads) CancelSubagentToolCall(string, string) (bool, error) {
	b.block()
	return true, nil
}

// keyPressResult is one handleKey return, carried off the goroutine that
// made the call.
type keyPressResult struct {
	cmd tea.Cmd
}

// pressKeyOffLoop presses "x" on s from another goroutine and waits up to
// budget for the call to return. A handler that still blocks inline never
// returns within the budget, which is the failure this file exists to
// catch. releaseOnce is called before failing so the blocked goroutine can
// unwind instead of leaking for the rest of the package run.
func pressKeyOffLoop(t *testing.T, s Screen, releaseOnce func(), budget time.Duration) keyPressResult {
	t.Helper()
	done := make(chan keyPressResult, 1)
	go func() {
		_, cmd := s.handleKey(tea.KeyPressMsg{Text: "x", Code: 'x'})
		done <- keyPressResult{cmd: cmd}
	}()
	select {
	case got := <-done:
		return got
	case <-budgetTimer(budget):
		releaseOnce()
		t.Fatal("handleKey did not return while the cancel seam was blocked: the update goroutine is stalled")
		return keyPressResult{}
	}
}

func budgetTimer(d time.Duration) <-chan time.Time { return time.After(d) }

// releaser returns a func that closes ch exactly once, however many times
// it is called.
func releaser(ch chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// TestCancelSubagentTaskKeyDoesNotBlockUpdate proves the files-panel cancel
// key returns immediately even while CancelSubagentTask is blocked, and
// that the outcome arrives later as a message the screen handles.
func TestCancelSubagentTaskKeyDoesNotBlockUpdate(t *testing.T) {
	release := make(chan struct{})
	releaseOnce := releaser(release)
	defer releaseOnce()

	threads := &blockingCancelThreads{stubThreads: stubThreads{}, release: release}
	s := selectSubagentRow(t, "agent-0")
	s.threads = threads

	got := pressKeyOffLoop(t, s, releaseOnce, 2*time.Second)
	if got.cmd == nil {
		t.Fatal("handleKey returned no Cmd; the cancel would never run")
	}
	if threads.entered() != 0 {
		t.Fatalf("CancelSubagentTask was entered %d times on the update goroutine, want 0", threads.entered())
	}

	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- got.cmd() }()
	releaseOnce()

	msg := <-msgCh
	result, ok := msg.(subagentTaskCancelResultMsg)
	if !ok {
		t.Fatalf("the Cmd reported %T, want subagentTaskCancelResultMsg", msg)
	}
	if !result.ok || result.err != nil {
		t.Fatalf("result = %+v, want ok with no error", result)
	}
	after, cmd := s.handleSubagentTaskCancelResult(result)
	if cmd != nil {
		t.Fatalf("handling the result returned an unexpected Cmd: %v", cmd)
	}
	assertNotice(t, after, "cancelling agent-0")
}

// assertNotice checks the screen's statusline is showing text containing
// want. The notice field itself is unexported in the statusline package, so
// the rendered row is the observable surface.
func assertNotice(t *testing.T, scr app.Screen, want string) {
	t.Helper()
	s, ok := scr.(Screen)
	if !ok {
		t.Fatalf("expected a conversation Screen, got %T", scr)
	}
	if !s.statusline.Active() {
		t.Fatalf("the statusline shows nothing; want a notice containing %q", want)
	}
	if got := s.statusline.View(time.Now()); !strings.Contains(got, want) {
		t.Fatalf("statusline = %q, want it to contain %q", got, want)
	}
}

// TestCancelThreadToolCallKeyDoesNotBlockUpdate is the same proof for the
// embedded thread dialog's per-tool-call cancel.
func TestCancelThreadToolCallKeyDoesNotBlockUpdate(t *testing.T) {
	release := make(chan struct{})
	releaseOnce := releaser(release)
	defer releaseOnce()

	threads := &blockingCancelThreads{
		stubThreads: stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}},
		release:     release,
	}
	s := openThreadDialogWithRunningTool(t, threads, "tc-1")

	got := pressKeyOffLoop(t, s, releaseOnce, 2*time.Second)
	if got.cmd == nil {
		t.Fatal("handleKey returned no Cmd; the cancel would never run")
	}
	if threads.entered() != 0 {
		t.Fatalf("CancelSubagentToolCall was entered %d times on the update goroutine, want 0", threads.entered())
	}

	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- got.cmd() }()
	releaseOnce()

	msg := <-msgCh
	result, ok := msg.(threadToolCallCancelResultMsg)
	if !ok {
		t.Fatalf("the Cmd reported %T, want threadToolCallCancelResultMsg", msg)
	}
	if !result.ok || result.err != nil {
		t.Fatalf("result = %+v, want ok with no error", result)
	}
	after, handled := s.handleThreadToolCallCancelResult(result)
	if handled != nil {
		t.Fatalf("handling the result returned an unexpected Cmd: %v", handled)
	}
	assertNotice(t, after, "cancelling run_command")
}

// TestSubagentTaskCancelResultErrorNotice isolates the error leg of
// handleSubagentTaskCancelResult: a failed cancel must surface, not be
// swallowed by the ok flag.
func TestSubagentTaskCancelResultErrorNotice(t *testing.T) {
	s := panelScreen(t, 100, 24)
	next, cmd := s.handleSubagentTaskCancelResult(subagentTaskCancelResultMsg{
		name: "agent-0", ok: false, err: errors.New("boom"),
	})
	if cmd != nil {
		t.Fatalf("unexpected Cmd: %v", cmd)
	}
	assertNotice(t, next, "cancel subagent task failed: boom")
}

// TestThreadToolCallCancelResultErrorNotice is the same isolation for the
// thread dialog's handler.
func TestThreadToolCallCancelResultErrorNotice(t *testing.T) {
	s := panelScreen(t, 100, 24)
	next, cmd := s.handleThreadToolCallCancelResult(threadToolCallCancelResultMsg{
		label: "run_command", ok: false, err: errors.New("boom"),
	})
	if cmd != nil {
		t.Fatalf("unexpected Cmd: %v", cmd)
	}
	assertNotice(t, next, "cancel tool call failed: boom")
}
