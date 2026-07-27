//go:build linux

package cli

import (
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// PTY scroll tests: real Linux PTY master/slave + tea.Program input path.
// Writes CSI key bytes to the PTY master; Program reads the slave.
// Complements Program.Send tests (tui_scroll_program_test.go).

type ptyScrollHarness struct {
	t        *testing.T
	p        *tea.Program
	master   *os.File
	slave    *os.File
	oldState *term.State
	cancel   context.CancelFunc
	done     chan error
}

func startPTYScrollProgram(t *testing.T, seed func(*tuiModel)) *ptyScrollHarness {
	t.Helper()
	installProgramProbe(t)

	m := tallScrollModel(t, 6, 50)
	if seed != nil {
		seed(m)
	}
	_ = m.View()

	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("pty open: %v", err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 80}); err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Fatalf("setsize: %v", err)
	}
	oldState, err := term.MakeRaw(int(slave.Fd()))
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Skipf("MakeRaw on pty slave: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	p := tea.NewProgram(m,
		tea.WithInput(slave),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignals(),
		tea.WithContext(ctx),
	)
	h := &ptyScrollHarness{
		t: t, p: p, master: master, slave: slave, oldState: oldState,
		cancel: cancel, done: make(chan error, 1),
	}
	go func() {
		_, err := p.Run()
		h.done <- err
	}()

	if !h.wait(2*time.Second, func(tm *tuiModel) bool { return tm.mode == modeChat && tm.ready }) {
		h.cleanup()
		t.Fatal("program not ready")
	}
	p.Send(tea.WindowSizeMsg{Width: 80, Height: 40})
	t.Cleanup(h.cleanup)
	return h
}

func (h *ptyScrollHarness) cleanup() {
	if h.p != nil {
		h.p.Kill()
	}
	if h.cancel != nil {
		h.cancel()
	}
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
	}
	if h.slave != nil && h.oldState != nil {
		_ = term.Restore(int(h.slave.Fd()), h.oldState)
	}
	if h.master != nil {
		_ = h.master.Close()
	}
	if h.slave != nil {
		_ = h.slave.Close()
	}
}

func (h *ptyScrollHarness) wait(timeout time.Duration, cond func(*tuiModel) bool) bool {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok := false
		d := make(chan struct{})
		h.p.Send(programProbeMsg{fn: func(m *tuiModel) { ok = cond(m) }, done: d})
		select {
		case <-d:
		case <-time.After(time.Second):
			h.t.Fatal("probe timeout")
		}
		if ok {
			return true
		}
		runtime.Gosched()
	}
	return false
}

func (h *ptyScrollHarness) writeCSI(seqs ...string) {
	h.t.Helper()
	for _, s := range seqs {
		if _, err := h.master.Write([]byte(s)); err != nil {
			h.t.Fatalf("pty write %q: %v", s, err)
		}
		// Yield so the Program input goroutine can read CSI bytes.
		runtime.Gosched()
	}
}

func TestScrollPTY_EndKeyViaBytes(t *testing.T) {
	h := startPTYScrollProgram(t, func(m *tuiModel) {
		m.noteUserScrolledUp()
		m.viewport.YOffset = 4
		m.setFocus(focusScrollback)
	})
	// Try common End key encodings (xterm / application / vt).
	h.writeCSI("\x1b[F", "\x1b[4~", "\x1bOF")
	if !h.wait(2*time.Second, func(m *tuiModel) bool {
		return m.followOutput && m.viewport.AtBottom()
	}) {
		t.Fatal("PTY End CSI did not jump to latest")
	}
}

func TestScrollPTY_PgUpViaBytesUnfollows(t *testing.T) {
	h := startPTYScrollProgram(t, func(m *tuiModel) {
		m.setFocus(focusComposer)
	})
	h.writeCSI("\x1b[5~")
	if !h.wait(2*time.Second, func(m *tuiModel) bool {
		return m.focus == focusScrollback && !m.followOutput
	}) {
		t.Fatal("PTY PgUp CSI did not unfollow")
	}
}

func TestScrollPTY_ProgramSurvivesPTYInput(t *testing.T) {
	h := startPTYScrollProgram(t, nil)
	h.writeCSI("\x1b[A") // up arrow
	const marker = "pty-stream"
	d := make(chan struct{})
	h.p.Send(programProbeMsg{fn: func(m *tuiModel) {
		_, _ = m.bridge.Write([]byte(marker + "\n"))
	}, done: d})
	<-d
	if !h.wait(2*time.Second, func(m *tuiModel) bool {
		return m.mode == modeChat && strings.Contains(m.streamBuf.String(), marker)
	}) {
		t.Fatal("program died or stream not drained under PTY")
	}
}
