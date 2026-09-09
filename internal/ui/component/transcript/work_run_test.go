package transcript

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// endedCall drives one tool call all the way through start, output and
// end, which is the path a real call takes. Building the finished Block
// by hand instead would skip updateLive - the very rule that decides
// whether the call folds - and pin nothing.
func endedCall(m Model, id, name, result, out string, ms int, ok bool) Model {
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: id, Name: name}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: id, Chunk: out}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: id, Name: name, OK: ok,
			Result: result, DurationMS: int64(ms)}})
	return m
}

// wallModel is a turn of mixed finished work: prose, then five calls of
// three different tools. Rendered per-block it is a dozen rows.
func wallModel(t *testing.T) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: "I will add a bounded retry."}})
	m = endedCall(m, "1", "read_file", "s3.go", "package storage", 12, true)
	m = endedCall(m, "2", "read_file", "s3_test.go", "package storage", 8, true)
	m = endedCall(m, "3", "edit", "s3.go", "@@ -14 +14 @@", 30, true)
	m = endedCall(m, "4", "run_command", "go build", "ok", 4100, true)
	m = endedCall(m, "5", "grep", "retry.Do", "s3.go:14", 50, true)
	return m
}

func viewRows(m Model) []string {
	return strings.Split(ansi.Strip(m.View()), "\n")
}

func rowContaining(rows []string, needle string) (int, string, bool) {
	for i, r := range rows {
		if strings.Contains(r, needle) {
			return i, r, true
		}
	}
	return -1, "", false
}

// TestAWallOfFinishedWorkFoldsToOneRow is the headline contract: a turn
// whose activity is five finished calls draws ONE row, and that row says
// what ran, how many calls it made and what they cost. A fold that
// stated none of those would hide the work rather than summarise it,
// which is the difference between a summary and a lie.
func TestAWallOfFinishedWorkFoldsToOneRow(t *testing.T) {
	m := wallModel(t)
	rows := viewRows(m)

	_, row, ok := rowContaining(rows, "work")
	if !ok {
		t.Fatalf("no work row in:\n%s", strings.Join(rows, "\n"))
	}
	for _, want := range []string{"read_file", "edit", "run_command", "5 calls"} {
		if !strings.Contains(row, want) {
			t.Errorf("work row %q does not state %q", row, want)
		}
	}
	// 12+8+30+4100+50 = 4200ms, which FormatElapsed renders as 4.2s.
	if !strings.Contains(row, "4.2s") {
		t.Errorf("work row %q does not add up its members' durations", row)
	}
	// Every member header must be gone: a fold that leaves the headers
	// behind has cost a row instead of saving eleven.
	for _, label := range []string{"edit ", "grep "} {
		if i, r, found := rowContaining(rows, label); found && !strings.Contains(r, "work") {
			t.Errorf("row %d still draws a folded member's own header: %q", i, r)
		}
	}
}

// TestTheWorkRowHangsUnderTheTurnLikeTheBlocksItStandsFor: the run is
// activity, so its row takes the same 2-column group indent every block
// it replaces would have taken. Emitting it at column 1 would put the
// summary of a turn's work out of line with the turn.
func TestTheWorkRowHangsUnderTheTurnLikeTheBlocksItStandsFor(t *testing.T) {
	rows := viewRows(wallModel(t))
	_, row, ok := rowContaining(rows, "work")
	if !ok {
		t.Fatal("no work row")
	}
	if !strings.HasPrefix(row, strings.Repeat(" ", groupIndent)) {
		t.Errorf("work row is not indented into the activity group: %q", row)
	}
}

// TestAFailedCallNeverFolds is the rule that keeps the fold honest. The
// failure is the one block worth its rows, so it must survive with its
// own header, its own body, and its own place on screen.
func TestAFailedCallNeverFolds(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: "running the tests"}})
	m = endedCall(m, "1", "read_file", "a.go", "package a", 5, true)
	m = endedCall(m, "2", "edit", "a.go", "@@", 5, true)
	m = endedCall(m, "3", "run_command", "go test", "--- FAIL: TestPut", 900, false)
	m = endedCall(m, "4", "read_file", "b.go", "package b", 5, true)

	rows := viewRows(m)
	if _, _, ok := rowContaining(rows, "failed"); !ok {
		t.Errorf("the failed call vanished from the screen:\n%s", strings.Join(rows, "\n"))
	}
	if _, _, ok := rowContaining(rows, "--- FAIL: TestPut"); !ok {
		t.Error("the failed call's body was collapsed away; a failure must stay open")
	}
}

// TestALiveCallNeverFolds: work still running is the one thing the
// reader is waiting on. A fold that could swallow it would make the
// screen go quiet exactly when it should not.
func TestALiveCallNeverFolds(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: "working"}})
	m = endedCall(m, "1", "read_file", "a.go", "package a", 5, true)
	m = endedCall(m, "2", "read_file", "b.go", "package b", 5, true)
	m = endedCall(m, "3", "edit", "a.go", "@@", 5, true)
	// A fourth call that has started but not ended.
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "4", Name: "run_command"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "4", Chunk: strings.Repeat("line\n", 20)}})

	rows := viewRows(m)
	if _, _, ok := rowContaining(rows, "run_command"); !ok {
		t.Errorf("the running call is not on screen:\n%s", strings.Join(rows, "\n"))
	}
	last := m.blocks[len(m.blocks)-1]
	if n, _ := m.runAt(len(m.blocks) - 1); n > 0 {
		t.Error("a running call was made the head of a coalesced run")
	}
	if last.Kind == uievent.KindToolEnd {
		t.Fatal("precondition: the fourth call has not ended")
	}
}

// TestTwoCallsAreNotAWall: below minWorkRun the fold costs the reader
// the tool names to save a single row, so it must not happen.
func TestTwoCallsAreNotAWall(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: "two things"}})
	m = endedCall(m, "1", "edit", "a.go", "@@", 5, true)
	m = endedCall(m, "2", "run_command", "go build", "ok", 5, true)

	rows := viewRows(m)
	if _, _, ok := rowContaining(rows, "work"); ok {
		t.Errorf("two calls folded into a work row:\n%s", strings.Join(rows, "\n"))
	}
	for _, want := range []string{"edit", "run_command"} {
		if _, _, ok := rowContaining(rows, want); !ok {
			t.Errorf("%q lost its own header", want)
		}
	}
}

// TestASameClassReadRunKeepsItsNamedRow: the read row lists its targets,
// so it says strictly more than the generic work row about the same
// blocks. A tie must go to it, or the more informative rendering is lost
// the moment the generic one exists.
func TestASameClassReadRunKeepsItsNamedRow(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: "reading"}})
	m = endedCall(m, "1", "read_file", "a.go", "package a", 5, true)
	m = endedCall(m, "2", "read_file", "b.go", "package b", 5, true)
	m = endedCall(m, "3", "read_file", "c.go", "package c", 5, true)

	rows := viewRows(m)
	if _, _, ok := rowContaining(rows, "Read"); !ok {
		t.Errorf("a pure read run lost its named row:\n%s", strings.Join(rows, "\n"))
	}
	if _, _, ok := rowContaining(rows, "work"); ok {
		t.Error("the generic work row displaced the more informative read row")
	}
}

// TestClickingTheWorkRowOpensEveryMember: the row stands in for all of
// them, so the click means "show me these" - the same contract the read
// run already had.
func TestClickingTheWorkRowOpensEveryMember(t *testing.T) {
	m := wallModel(t)
	row, _, ok := rowContaining(viewRows(m), "work")
	if !ok {
		t.Fatal("no work row to click")
	}
	next, toggled := m.ToggleBlockAtScreenRow(groupIndent, row)
	if !toggled {
		t.Fatal("clicking the work row reported nothing")
	}
	rows := viewRows(next)
	for _, want := range []string{"edit", "run_command", "grep"} {
		if _, _, found := rowContaining(rows, want); !found {
			t.Errorf("%q did not come back after the run was opened:\n%s", want, strings.Join(rows, "\n"))
		}
	}
}

// TestTheDumpNeverFoldsAWorkRun: coalescing is display-only. The dump is
// what "[" writes to the primary screen for grep and tmux copy, so a
// fold reaching it would delete the session's record of what ran.
func TestTheDumpNeverFoldsAWorkRun(t *testing.T) {
	dump := wallModel(t).Dump()
	if strings.Contains(dump, "5 calls") {
		t.Error("the dump coalesced a work run; coalescing must be display-only")
	}
	for _, want := range []string{"edit", "run_command", "grep"} {
		if !strings.Contains(dump, want) {
			t.Errorf("the dump lost %q", want)
		}
	}
}

// TestAProductionShapedCallStillReportsItsDuration is the discriminator
// the original work-run tests lacked. They built tool.end events with a
// DurationMS filled in, which NO producer sets: uiadapter.translateToolEnd
// and thread.LoadHistory are the only two, and agent.Event has no
// duration to give them. So the suite was green while every tool header
// on screen read "0ms" and the work row silently dropped the cost it
// advertises. This builds the event the way production does - no
// DurationMS - and requires a duration anyway.
func TestAProductionShapedCallStillReportsItsDuration(t *testing.T) {
	clock := time.Unix(1700000000, 0)
	m := New(loadTheme(t), theme.TierASCII)
	m.Now = func() time.Time { return clock }
	m.SetSize(80, 40)

	call := func(id, name string) {
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: id, Name: name}})
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput,
			Body: uievent.ToolOutputBody{ToolCallID: id, Chunk: "out"}})
		clock = clock.Add(1500 * time.Millisecond)
		// Exactly what translateToolEnd emits: no DurationMS.
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
			Body: uievent.ToolEndBody{ToolCallID: id, Name: name, OK: true, Result: name + " done"}})
	}

	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: "starting now"}})
	call("1", "read_file")

	// A lone finished call: its own header must carry the measured time.
	if _, row, ok := rowContaining(viewRows(m), "read_file"); !ok {
		t.Fatal("no read_file row")
	} else if !strings.Contains(row, "1.5s") {
		t.Errorf("a production-shaped call renders %q; the duration was never measured", row)
	}

	call("2", "edit")
	call("3", "run_command")

	_, row, ok := rowContaining(viewRows(m), "3 calls")
	if !ok {
		t.Fatalf("no work row in:\n%s", strings.Join(viewRows(m), "\n"))
	}
	// 3 calls x 1.5s.
	if !strings.Contains(row, "4.5s") {
		t.Errorf("work row %q does not report the cost it advertises", row)
	}
}

// TestAProducerSuppliedDurationStillWins: the measured fallback must not
// override a producer that does report a duration, or a replayed history
// would be re-timed to the moment it was replayed.
func TestAProducerSuppliedDurationStillWins(t *testing.T) {
	clock := time.Unix(1700000000, 0)
	m := New(loadTheme(t), theme.TierASCII)
	m.Now = func() time.Time { return clock }
	m.SetSize(80, 40)

	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "1", Name: "read_file"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "1", Chunk: "out"}})
	clock = clock.Add(90 * time.Second) // a long replay gap
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "1", Name: "read_file", OK: true, DurationMS: 250}})

	_, row, ok := rowContaining(viewRows(m), "read_file")
	if !ok {
		t.Fatal("no read_file row")
	}
	if !strings.Contains(row, "250ms") {
		t.Errorf("row %q: the measured gap overrode the duration the producer reported", row)
	}
}

// TestEveryRunMemberRoutesToTheLayoutsHead: any SUFFIX of a run is
// itself a run, so asking "does block i head a run" answered yes for a
// member halfway down one. The keyboard toggle then expanded from there,
// replacing the row the user acted on with a different summary row and
// leaving the earlier members folded - not the dissolve into per-block
// headers that expandRun and ToggleFocused both promise.
func TestEveryRunMemberRoutesToTheLayoutsHead(t *testing.T) {
	m := wallModel(t)
	spans := m.layout()
	head := -1
	for i := range spans {
		if spans[i].runSize > 0 && spans[i].height > 0 {
			head = i
			break
		}
	}
	if head < 0 {
		t.Fatal("precondition: the fixture coalesces a run")
	}
	size := spans[head].runSize
	for i := head; i < head+size; i++ {
		got, ok := m.leaderHeadOf(i)
		if !ok || got != head {
			t.Errorf("leaderHeadOf(%d) = (%d, %v), want (%d, true)", i, got, ok, head)
		}
	}
}

// TestFocusingAMemberAndTogglingOpensTheWholeRun is the same defect from
// the user's side: space on any folded member must dissolve the run the
// row stands for, not a suffix of it.
func TestFocusingAMemberAndTogglingOpensTheWholeRun(t *testing.T) {
	m := wallModel(t)
	m.focus = len(m.blocks) - 1 // the last member of the run
	m = m.syncFocus()

	next, acted := m.ToggleFocused()
	if !acted {
		t.Fatal("toggling a folded run member did nothing")
	}
	rows := viewRows(next)
	for _, want := range []string{"read_file", "edit", "run_command", "grep"} {
		if _, _, ok := rowContaining(rows, want); !ok {
			t.Errorf("%q is still folded after opening the run from a member:\n%s",
				want, strings.Join(rows, "\n"))
		}
	}
}

// TestADirectlyPushedDiffCollapsesLikeAMergedOne: the two collapse rules
// must agree. toolEndBlockValue decided from the body it had built, then
// REPLACED that body with the rendered diff, so a diff arriving without a
// live start block to merge into stayed expanded while the merged one
// collapsed - and broke any work run it landed in.
func TestADirectlyPushedDiffCollapsesLikeAMergedOne(t *testing.T) {
	diff := &uievent.Diff{
		Path: "s3.go", Added: 2, Removed: 1,
		Hunks: []uievent.DiffHunk{{Header: "@@ -1 +1 @@",
			Lines: []uievent.DiffLine{{Kind: uievent.DiffLineAdd, Text: "retry"}}}},
	}
	end := uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "1", Name: "edit", OK: true, Diff: diff}}

	// Direct push: no start block to merge into.
	direct := New(loadTheme(t), theme.TierASCII)
	direct.SetSize(80, 20)
	direct, _ = direct.HandleEvent(end)

	// The merge path: a start block exists first.
	merged := New(loadTheme(t), theme.TierASCII)
	merged.SetSize(80, 20)
	merged, _ = merged.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "1", Name: "edit"}})
	merged, _ = merged.HandleEvent(end)

	d, mg := direct.blocks[len(direct.blocks)-1], merged.blocks[len(merged.blocks)-1]
	if d.Collapsed != mg.Collapsed || d.Collapsible != mg.Collapsible {
		t.Errorf("direct push {collapsible:%v collapsed:%v} vs merged {collapsible:%v collapsed:%v}: the two rules disagree",
			d.Collapsible, d.Collapsed, mg.Collapsible, mg.Collapsed)
	}
	if !d.Collapsed {
		t.Error("a finished successful diff did not collapse by default")
	}
}

// TestTrimKeepsTheReaderOnTheSameContent: eviction used to subtract the
// departing row count from the offset, which assumes the survivors lay
// out unchanged. A coalesced work run cut below minWorkRun stops
// coalescing - one leader row becomes several headers - so everything
// under it moved and the reader drifted by a row for free.
func TestTrimKeepsTheReaderOnTheSameContent(t *testing.T) {
	build := func(extra int) Model {
		m := New(loadTheme(t), theme.TierASCII)
		m.SetSize(80, 10)
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd,
			Body: uievent.TextEndBody{Text: "anchor prose"}})
		// A run long enough to coalesce, so trimming into it can drop it
		// below the fold threshold.
		for i, name := range []string{"read_file", "read_file", "edit", "run_command"} {
			m = endedCall(m, string(rune('a'+i)), name, "r", "out", 5, true)
		}
		for i := 0; i < extra; i++ {
			m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd,
				Body: uievent.TextEndBody{Text: "filler " + itoa(i)}})
		}
		return m
	}

	for split := 1; split <= 5; split++ {
		// Exactly at the bound, with the run still at the front, so the
		// pushes below evict INTO it. Overshooting here trimmed the run
		// away before the measurement and the case never arose.
		m := build(uikitconfig.MaxTranscriptLines - 5)
		// Pause follow well BELOW the prefix eviction will drop: a
		// reader parked on content that is itself evicted has nothing to
		// be held on, which is not what this pins.
		m = m.ScrollBy(-8)
		before := ansi.Strip(m.Rows()[0])

		// Push enough blocks to evict `split` from the front.
		for i := 0; i < split; i++ {
			m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd,
				Body: uievent.TextEndBody{Text: "new " + itoa(i)}})
		}
		if after := ansi.Strip(m.Rows()[0]); after != before {
			t.Errorf("split %d: the reader's top row moved from %q to %q", split, before, after)
		}
	}
}

// TestLeaderHeadOfRefusesAnOutOfRangeIndex: focus indices can outrun the
// block list between an eviction and a reindex, and every caller treats
// "no head" as "do nothing".
func TestLeaderHeadOfRefusesAnOutOfRangeIndex(t *testing.T) {
	m := wallModel(t)
	for _, i := range []int{-1, len(m.blocks), len(m.blocks) + 5} {
		if h, ok := m.leaderHeadOf(i); ok {
			t.Errorf("leaderHeadOf(%d) = (%d, true), want no head", i, h)
		}
	}
}

// TestARunOfPureReasoningIsCountedInSteps: a work run made only of
// reasoning made no tool calls, so "0 calls" would be true and useless.
func TestARunOfPureReasoningIsCountedInSteps(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 20)
	m.blocks = []Block{
		{Kind: uievent.KindTextEnd, Prose: true, Body: []string{"prose"}},
	}
	for i := 0; i < 3; i++ {
		m.blocks = append(m.blocks, Block{
			Kind:        uievent.KindReasoning,
			Header:      Header{Label: "reasoning", Meta: "9 words", State: "hidden"},
			Body:        []string{"thinking"},
			Collapsible: true, Collapsed: true,
		})
	}

	spans := m.layout()
	head := -1
	for i := range spans {
		if spans[i].runSize > 0 && spans[i].height > 0 {
			head = i
		}
	}
	if head < 0 {
		t.Fatal("three folded reasoning blocks did not coalesce")
	}
	spec := m.workRunSpec(spans[head], head)
	if !strings.Contains(spec.Meta, "3 steps") {
		t.Errorf("meta = %q, want it counted in steps: no call was made", spec.Meta)
	}
	if strings.Contains(spec.Meta, "calls") {
		t.Errorf("meta = %q claims calls a reasoning-only run never made", spec.Meta)
	}
}

// TestAToolStartAfterAPendingBlockKeepsTheOriginalStartTime: a call that
// was announced as pending and then started must time from the moment it
// was first seen, not from the start event, or the wait the reader
// actually sat through is understated.
func TestAToolStartAfterAPendingBlockKeepsTheOriginalStartTime(t *testing.T) {
	clock := time.Unix(1700000000, 0)
	m := New(loadTheme(t), theme.TierASCII)
	m.Now = func() time.Time { return clock }
	m.SetSize(80, 20)

	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolPending,
		Body: uievent.ToolPendingBody{ToolCallID: "1", Name: "run_command"}})
	pendingAt := m.blocks[len(m.blocks)-1].StartedAt
	if pendingAt.IsZero() {
		t.Fatal("a pending block carries no start time")
	}

	clock = clock.Add(2 * time.Second) // queued for two seconds
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "1", Name: "run_command"}})
	if got := m.blocks[len(m.blocks)-1].StartedAt; !got.Equal(pendingAt) {
		t.Errorf("start time moved from %v to %v: the queued wait was discarded", pendingAt, got)
	}

	clock = clock.Add(1 * time.Second)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "1", Chunk: "out"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "1", Name: "run_command", OK: true}})
	if got := m.blocks[len(m.blocks)-1].ElapsedMS; got != 3000 {
		t.Errorf("elapsed = %dms, want 3000 (queued 2s + ran 1s)", got)
	}
}
