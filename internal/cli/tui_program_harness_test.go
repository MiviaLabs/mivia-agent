package cli

import (
	"context"
	"io"
	"runtime"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// programProbeMsg runs fn on the live Program model inside Update (race-free).
type programProbeMsg struct {
	fn   func(*tuiModel)
	done chan struct{}
}

// installProgramProbe wraps updateMessageImpl so Program.Send(probe) can observe state.
func installProgramProbe(t *testing.T) {
	t.Helper()
	orig := updateMessageImpl
	updateMessageImpl = func(m *tuiModel, msg tea.Msg) (tea.Model, tea.Cmd) {
		if p, ok := msg.(programProbeMsg); ok {
			if p.fn != nil {
				p.fn(m)
			}
			if p.done != nil {
				select {
				case <-p.done:
				default:
					close(p.done)
				}
			}
			return m, nil
		}
		return orig(m, msg)
	}
	t.Cleanup(func() { updateMessageImpl = orig })
}

// scrollProgram is a short-lived Bubble Tea Program for scroll integration tests.
// Uses WithoutRenderer so CI needs no TTY; pollCmd still runs on the real event loop.
type scrollProgram struct {
	t      *testing.T
	p      *tea.Program
	cancel context.CancelFunc
	done   chan error
}

func startScrollProgram(t *testing.T, seed func(*tuiModel)) *scrollProgram {
	t.Helper()
	// Program event-loop tests must not run in parallel with other Program tests
	// sharing updateMessageImpl (package-level seam).
	installProgramProbe(t)

	m := tallScrollModel(t, 6, 50)
	if seed != nil {
		seed(m)
	}
	// Ensure hitMap/layout stable before Run.
	_ = m.View()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	p := tea.NewProgram(m,
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignals(),
		tea.WithContext(ctx),
	)
	sp := &scrollProgram{t: t, p: p, cancel: cancel, done: make(chan error, 1)}
	go func() {
		_, err := p.Run()
		sp.done <- err
	}()
	// Allow Init + first pollCmd to arm (deterministic condition wait).
	if !sp.waitUntil(2*time.Second, func(tm *tuiModel) bool {
		return tm.mode == modeChat && tm.ready
	}) {
		sp.stop()
		t.Fatal("program did not become ready")
	}
	t.Cleanup(sp.stop)
	return sp
}

func (sp *scrollProgram) stop() {
	if sp.p != nil {
		sp.p.Kill()
	}
	if sp.cancel != nil {
		sp.cancel()
	}
	select {
	case <-sp.done:
	case <-time.After(3 * time.Second):
		// best-effort; test is ending
	}
}

func (sp *scrollProgram) send(msg tea.Msg) {
	sp.t.Helper()
	sp.p.Send(msg)
}

func (sp *scrollProgram) probe(fn func(*tuiModel)) {
	sp.t.Helper()
	done := make(chan struct{})
	sp.p.Send(programProbeMsg{fn: fn, done: done})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		sp.t.Fatal("program probe timed out")
	}
}

// waitUntil polls the live model until cond is true or timeout.
// Yields via runtime.Gosched (no time.Sleep — blocked by project Semgrep).
func (sp *scrollProgram) waitUntil(timeout time.Duration, cond func(*tuiModel) bool) bool {
	sp.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok := false
		sp.probe(func(m *tuiModel) { ok = cond(m) })
		if ok {
			return true
		}
		runtime.Gosched()
	}
	return false
}
