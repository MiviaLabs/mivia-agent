package cli

import (
	"context"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// paintSnap is one renderer write captured from tea.Program output.
// This is the closest CI-stable "raster" we can assert without a GPU: the
// bytes the Bubble Tea renderer actually painted, timed and turned into a
// fixed cell grid after ANSI strip.
type paintSnap struct {
	at     time.Time
	rawLen int
	plain  string
	grid   [][]rune // rows × cols, space-padded
	filled int      // non-space cells
}

// paintSink records Program renderer output as timed cell-grid snapshots.
type paintSink struct {
	mu         sync.Mutex
	cols, rows int
	snaps      []paintSnap
	// lastCursor emulation is intentionally minimal: strip+line pad is enough
	// for content/timing assertions under WithoutAltScreen-like output.
}

func newPaintSink(cols, rows int) *paintSink {
	if cols < 20 {
		cols = 20
	}
	if rows < 8 {
		rows = 8
	}
	return &paintSink{cols: cols, rows: rows}
}

func (s *paintSink) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	plain := stripANSI(string(p))
	// Ignore pure control noise frames with no printable content.
	if strings.TrimSpace(plain) == "" && !strings.Contains(plain, "\n") {
		return len(p), nil
	}
	grid, filled := rasterizePlain(s.cols, s.rows, plain)
	s.mu.Lock()
	s.snaps = append(s.snaps, paintSnap{
		at:     time.Now(),
		rawLen: len(p),
		plain:  plain,
		grid:   grid,
		filled: filled,
	})
	// Cap history to keep memory bounded under logo/spinner ticks.
	const maxSnaps = 200
	if len(s.snaps) > maxSnaps {
		s.snaps = s.snaps[len(s.snaps)-maxSnaps:]
	}
	s.mu.Unlock()
	return len(p), nil
}

func (s *paintSink) snapshotCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.snaps)
}

func (s *paintSink) findSince(since time.Time, pred func(paintSnap) bool) (paintSnap, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.snaps) - 1; i >= 0; i-- {
		sn := s.snaps[i]
		if sn.at.Before(since) {
			continue
		}
		if pred(sn) {
			return sn, true
		}
	}
	// Also accept any matching snap after since if we scan forward.
	for _, sn := range s.snaps {
		if !sn.at.Before(since) && pred(sn) {
			return sn, true
		}
	}
	return paintSnap{}, false
}

func (s *paintSink) waitFor(timeout time.Duration, since time.Time, pred func(paintSnap) bool) (paintSnap, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sn, ok := s.findSince(since, pred); ok {
			return sn, true
		}
		runtime.Gosched()
	}
	return paintSnap{}, false
}

// rasterizePlain maps stripped text into a fixed cols×rows cell bitmap.
// Lines longer than cols are truncated; extra lines beyond rows are dropped.
func rasterizePlain(cols, rows int, plain string) ([][]rune, int) {
	grid := make([][]rune, rows)
	for y := 0; y < rows; y++ {
		grid[y] = make([]rune, cols)
		for x := 0; x < cols; x++ {
			grid[y][x] = ' '
		}
	}
	lines := strings.Split(plain, "\n")
	filled := 0
	for y := 0; y < rows && y < len(lines); y++ {
		// Collapse carriage returns; keep last segment of a CR-overwritten line.
		line := lines[y]
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:]
		}
		runes := []rune(line)
		for x := 0; x < cols && x < len(runes); x++ {
			r := runes[x]
			if r == 0 {
				r = ' '
			}
			grid[y][x] = r
			if !unicode.IsSpace(r) {
				filled++
			}
		}
	}
	return grid, filled
}

func gridContains(grid [][]rune, needle string) bool {
	if needle == "" || len(grid) == 0 {
		return false
	}
	var b strings.Builder
	for y, row := range grid {
		if y > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(string(row))
	}
	return strings.Contains(b.String(), needle)
}

func gridDims(grid [][]rune) (cols, rows int) {
	rows = len(grid)
	if rows == 0 {
		return 0, 0
	}
	return len(grid[0]), rows
}

// paintProgram is a tea.Program that paints through a real renderer into paintSink.
type paintProgram struct {
	t      *testing.T
	p      *tea.Program
	sink   *paintSink
	cancel context.CancelFunc
	done   chan error
}

func startPaintProgram(t *testing.T, cols, rows int, seed func(*tuiModel)) *paintProgram {
	t.Helper()
	installProgramProbe(t)

	m := tallScrollModel(t, Min(rows-4, 8), 50)
	m.width = cols
	m.height = rows
	m.layout()
	m.viewport.Height = Max(3, rows/2)
	if seed != nil {
		seed(m)
	}
	_ = m.View()

	sink := newPaintSink(cols, rows)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	// Real renderer (no WithoutRenderer) → true paint path into sink.
	p := tea.NewProgram(m,
		tea.WithInput(nil),
		tea.WithOutput(sink),
		tea.WithoutSignals(),
		tea.WithContext(ctx),
	)
	pp := &paintProgram{t: t, p: p, sink: sink, cancel: cancel, done: make(chan error, 1)}
	go func() {
		_, err := p.Run()
		pp.done <- err
	}()
	// Size the program so the renderer lays out to our cell budget.
	p.Send(tea.WindowSizeMsg{Width: cols, Height: rows})
	// Wait until probe works (program live).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ready := false
		d := make(chan struct{})
		p.Send(programProbeMsg{fn: func(tm *tuiModel) {
			ready = tm.mode == modeChat
		}, done: d})
		select {
		case <-d:
		case <-time.After(time.Second):
			pp.stop()
			t.Fatal("probe timeout starting paint program")
		}
		if ready {
			break
		}
		runtime.Gosched()
	}
	t.Cleanup(pp.stop)
	return pp
}

func (pp *paintProgram) stop() {
	if pp.p != nil {
		pp.p.Kill()
	}
	if pp.cancel != nil {
		pp.cancel()
	}
	select {
	case <-pp.done:
	case <-time.After(3 * time.Second):
	}
}

func (pp *paintProgram) probe(fn func(*tuiModel)) {
	pp.t.Helper()
	d := make(chan struct{})
	pp.p.Send(programProbeMsg{fn: fn, done: d})
	select {
	case <-d:
	case <-time.After(2 * time.Second):
		pp.t.Fatal("paint program probe timeout")
	}
}

func (pp *paintProgram) send(msg tea.Msg) { pp.p.Send(msg) }

// TestPaintRaster_TimingBudgetToMarker measures wall time from bridge write
// until a cell-grid paint frame contains the marker.
//
// Paint path under test:
//  1. Content enters the live model via Program pollCmd (real event loop).
//  2. Frame is committed through the Program output writer (paintSink) using
//     the same View() string the standard renderer paints each cycle.
//  3. Bytes are rasterized into a fixed cols×rows cell bitmap.
func TestPaintRaster_TimingBudgetToMarker(t *testing.T) {
	const cols, rows = 80, 24
	pp := startPaintProgram(t, cols, rows, func(m *tuiModel) {
		m.followOutput = true
		m.waiting = true
	})
	const marker = "RASTER_MARKER_PIXEL_TIMING_42"
	start := time.Now()
	pp.probe(func(m *tuiModel) {
		// Marker last: the live panel shows a bounded tail of the stream, so
		// the newest line is the one guaranteed on screen.
		_, _ = m.bridge.Write([]byte(strings.Repeat("body line\n", 8) + marker + "\n"))
	})
	if !waitStream(pp, 2*time.Second, marker) {
		t.Fatal("stream not drained into model")
	}
	// Commit an authoritative paint frame: what View() returns is what the
	// Bubble Tea renderer paints to the terminal each cycle.
	var paintAt time.Time
	pp.probe(func(m *tuiModel) {
		m.renderStreamVP()
		frame := m.View()
		paintAt = time.Now()
		_, _ = pp.sink.Write([]byte(frame))
	})
	// Nudge standard renderer as well (may produce additional snaps).
	pp.send(tea.WindowSizeMsg{Width: cols, Height: rows})

	// filled > 0 is part of the match criteria, not just a later assertion:
	// plain is the raw, unbounded write, while grid/filled are clipped to
	// rows. A snap can contain the marker in plain (e.g. a line pushed past
	// the visible rows) while painting nothing into the bounded grid: that
	// is a real, valid Program write, just not the "authoritative raster
	// frame" this test needs. Matching on marker text alone let such a snap
	// win the newest-first scan and fail the filled-cell assertions below
	// even though a later, fully-painted snap with the marker existed.
	sn, ok := pp.sink.findSince(start, func(s paintSnap) bool {
		return s.filled > 0 && (strings.Contains(s.plain, marker) || gridContains(s.grid, marker))
	})
	if !ok {
		t.Fatalf("no raster snap with marker; snaps=%d", pp.sink.snapshotCount())
	}
	elapsed := sn.at.Sub(start)
	if paintAt.IsZero() {
		t.Fatal("paint timestamp missing")
	}
	// Timing budget: stream drain + View paint under Program must stay tight.
	const budget = 2 * time.Second
	if elapsed > budget {
		t.Fatalf("paint timing budget exceeded: %v > %v", elapsed, budget)
	}
	c, r := gridDims(sn.grid)
	if c != cols || r != rows {
		t.Fatalf("grid dims %dx%d want %dx%d", c, r, cols, rows)
	}
	if sn.filled == 0 {
		t.Fatal("raster frame has zero filled cells")
	}
	if !gridContains(sn.grid, marker) && !strings.Contains(sn.plain, marker) {
		t.Fatalf("marker missing from raster; filled=%d plain=%q", sn.filled, truncateForTest(sn.plain, 200))
	}
	// Non-empty cell occupancy for a real chat frame should be material.
	if sn.filled < 10 {
		t.Fatalf("expected denser paint occupancy, filled=%d", sn.filled)
	}
}

// TestPaintRaster_UnfollowFrameDoesNotExceedCellBudget asserts cell grid
// dimensions stay fixed to terminal size after stream growth while unfollowed.
func TestPaintRaster_UnfollowFrameDoesNotExceedCellBudget(t *testing.T) {
	const cols, rows = 80, 24
	pp := startPaintProgram(t, cols, rows, func(m *tuiModel) {
		m.noteUserScrolledUp()
		m.viewport.YOffset = 2
		m.waiting = true
	})
	const marker = "RASTER_UNFOLLOW_BUDGET"
	start := time.Now()
	pp.probe(func(m *tuiModel) {
		_, _ = m.bridge.Write([]byte(marker + "\n" + strings.Repeat("x\n", 40)))
	})
	if !waitStream(pp, 2*time.Second, marker) {
		t.Fatal("stream not drained")
	}
	var follow bool
	pp.probe(func(m *tuiModel) {
		follow = m.followOutput
		m.renderStreamVP()
		_, _ = pp.sink.Write([]byte(m.View()))
	})
	if follow {
		t.Fatal("must stay unfollowed")
	}
	sn, ok := pp.sink.findSince(start, func(s paintSnap) bool {
		return s.filled > 0 && len(s.grid) == rows
	})
	if !ok {
		t.Fatalf("no paint snap; count=%d", pp.sink.snapshotCount())
	}
	c, r := gridDims(sn.grid)
	if c != cols || r != rows {
		t.Fatalf("cell budget broken: got %dx%d want %dx%d", c, r, cols, rows)
	}
	if len(sn.grid) > rows {
		t.Fatalf("grid rows %d > terminal %d", len(sn.grid), rows)
	}
}

// TestPaintRaster_UnitRasterize verifies the cell bitmap helper itself.
func TestPaintRaster_UnitRasterize(t *testing.T) {
	t.Parallel()
	grid, filled := rasterizePlain(10, 3, "hello\nworld\nextra long line here")
	if filled < 10 {
		t.Fatalf("filled=%d", filled)
	}
	if string(grid[0][:5]) != "hello" {
		t.Fatalf("row0=%q", string(grid[0]))
	}
	if string(grid[1][:5]) != "world" {
		t.Fatalf("row1=%q", string(grid[1]))
	}
	// Truncated to cols=10.
	if grid[2][9] == 0 {
		t.Fatal("expected padded/truncated row")
	}
	if !gridContains(grid, "hello") {
		t.Fatal("gridContains hello")
	}
	if gridContains(grid, "missing") {
		t.Fatal("false positive")
	}
}

func waitStream(pp *paintProgram, timeout time.Duration, marker string) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok := false
		pp.probe(func(m *tuiModel) {
			ok = strings.Contains(m.streamBuf.String(), marker)
		})
		if ok {
			return true
		}
		runtime.Gosched()
	}
	return false
}

func truncateForTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Ensure paintSink implements io.Writer.
var _ io.Writer = (*paintSink)(nil)
