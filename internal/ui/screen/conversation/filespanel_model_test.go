package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func modelPanelScreen(t *testing.T) Screen {
	t.Helper()
	s := panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...)
	s.SetCommandRunner(&fakeRunner{outcome: ports.CommandOutcome{ModelChoices: []string{"fast", "deep"}}})
	s.topbar.SetSession(ports.ModelInfo{Name: "claude-fable-5-1", Provider: "anthropic"}, ports.Usage{})
	return s
}

// TestOpenPanelLandsOnTheModelRow: opening the sidebar selects the model
// row first, and the row names the session's model.
func TestOpenPanelLandsOnTheModelRow(t *testing.T) {
	s := openPanel(t, modelPanelScreen(t))
	if got := s.panel.list.CursorRow(); got != 0 {
		t.Fatalf("cursor after open = %d, want 0 (the model row)", got)
	}
	view := ansi.Strip(s.View())
	// The screen refreshes the top bar's session from its conversation on
	// open, so assert against whatever model the bar now reports.
	name := s.topbar.Info().Name
	if name == "" || !strings.Contains(view, "> "+name) && !strings.Contains(view, "> "+s.topbar.Info().Provider+"/"+name) {
		t.Errorf("model row not marked as selected with the session's model %q:\n%s", name, view)
	}
	if strings.Contains(view, "SIDEBAR") {
		t.Errorf("the old SIDEBAR title must be gone:\n%s", view)
	}
}

// TestEnterOnModelRowOpensTheModelPicker: Enter on the model row opens
// the same picker "/model" does, not a content dialog.
func TestEnterOnModelRowOpensTheModelPicker(t *testing.T) {
	s := openPanel(t, modelPanelScreen(t))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.modelPicker == nil {
		t.Fatal("Enter on the model row must open the model picker")
	}
	if s.panel.dialog {
		t.Error("the model row must not open a content dialog")
	}
}

// TestDoubleClickOnModelRowOpensTheModelPicker: one click selects the
// model row, a second within the double-click window opens the picker.
func TestDoubleClickOnModelRowOpensTheModelPicker(t *testing.T) {
	s := openPanel(t, modelPanelScreen(t))
	now := time.Now()
	s.now = func() time.Time { return now }
	next, _ := s.handleNavClick(5) // row 5 is the model row (after the context section)
	s = next.(Screen)
	if s.modelPicker != nil || s.panel.dialog {
		t.Fatal("a single click on the model row must only select it")
	}
	next, _ = s.handleNavClick(5)
	s = next.(Screen)
	if s.modelPicker == nil {
		t.Fatal("a double-click on the model row must open the model picker")
	}
}

// TestTopBarHidesTheModelCapsuleWhileTheSidebarIsOpen: the model is named
// once - in the sidebar while it is open, in the top bar otherwise.
func TestTopBarHidesTheModelCapsuleWhileTheSidebarIsOpen(t *testing.T) {
	s := modelPanelScreen(t)
	if _, _, ok := s.topbar.ModelBounds(); !ok {
		t.Fatal("precondition: the top bar shows the model capsule while the sidebar is closed")
	}
	s = openPanel(t, s)
	if _, _, ok := s.topbar.ModelBounds(); ok {
		t.Error("the top bar must hide its model capsule while the sidebar is open")
	}
	top := ansi.Strip(strings.Split(s.View(), "\n")[1])
	if strings.Contains(top, "claude-fable-5-1") {
		t.Errorf("top bar still names the model while the sidebar shows it:\n%s", top)
	}
}

// TestSidebarContextSectionOwnsTheShareWhileOpen: the context share
// moves out of the top bar and off the model row into its own section,
// a header with the share right-aligned and a bar the full inner width.
func TestSidebarContextSectionOwnsTheShareWhileOpen(t *testing.T) {
	half := func(s Screen) Screen {
		s.topbar.SetSession(
			ports.ModelInfo{Name: "claude-fable-5-1", Provider: "anthropic", ContextWindow: 100_000},
			ports.Usage{InputTokens: 50_000},
		)
		return s
	}
	s := half(modelPanelScreen(t))
	if !strings.Contains(ansi.Strip(strings.Split(s.View(), "\n")[1]), "50%") {
		t.Fatal("precondition: the top bar shows the context share while the sidebar is closed")
	}
	s = half(openPanel(t, s)) // opening replays the fixture session; re-seed the share
	lines := strings.Split(ansi.Strip(s.View()), "\n")
	if strings.Contains(lines[1], "50%") {
		t.Errorf("top bar still shows the context share while the sidebar owns it:\n%s", lines[1])
	}
	var header, bar, model string
	for i, l := range lines {
		if strings.Contains(l, "context") && strings.Contains(l, "50%") && i+3 < len(lines) {
			header, bar, model = l, lines[i+1], lines[i+3]
			break
		}
	}
	if header == "" {
		t.Fatalf("no 'context ... 50%%' header in the sidebar:\n%s", strings.Join(lines, "\n"))
	}
	want := render.ContextBar(50, s.panelInnerWidth(), s.Tier)
	if !strings.Contains(bar, want) {
		t.Errorf("bar row %q lacks the half-filled full-width bar %q", bar, want)
	}
	if strings.Contains(model, "%") {
		t.Errorf("model row still carries the context share: %q", model)
	}
}

// contextUsageFixture is a session whose context is 79k of a 128k budget,
// split so every bucket is distinguishable by value.
func contextUsageFixture() (ports.ModelInfo, ports.Usage) {
	return ports.ModelInfo{Name: "claude-fable-5-1", Provider: "anthropic", ContextWindow: 128_000},
		ports.Usage{InputTokens: 79_000, Breakdown: ports.ContextBreakdown{
			System: 6_000, ToolSchemas: 21_000, ToolCount: 48, Memory: 2_000,
			Prose: 12_000, ToolResults: 36_000, Reasoning: 2_000,
		}}
}

// contextPanelScreen opens the sidebar on a terminal of the given height and
// seeds the session AFTER opening, because opening replays the fixture.
func contextPanelScreen(t *testing.T, height int) Screen {
	t.Helper()
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, height, sampleDiffs()...))
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: height})
	s = next.(Screen)
	s.topbar.SetSession(contextUsageFixture())
	return s
}

// TestContextSectionSummaryRowsAlwaysDraw: at any height the section states
// the share, draws the bar, and says what the share is in tokens. The tokens
// row is the one that answers "how much room is left", which a percentage
// alone never does.
func TestContextSectionSummaryRowsAlwaysDraw(t *testing.T) {
	s := contextPanelScreen(t, 24)
	view := ansi.Strip(s.View())
	for _, want := range []string{"context", "61%", "79k of 128k", "49k free"} {
		if !strings.Contains(view, want) {
			t.Errorf("summary rows missing %q:\n%s", want, view)
		}
	}
	// The detail block is the tall-terminal affordance; a short body keeps
	// its rows for the files and subagents instead.
	if strings.Contains(view, "thinking") {
		t.Errorf("the bucket block drew on a short body, crowding out the live rows:\n%s", view)
	}
}

// TestContextSectionBucketRowsSumToTheHeader is the discriminator for the
// breakdown being real: every bucket is named with its own value, and the
// values add up to the total the row above them reports. A display that
// dropped or double-counted a bucket would still look plausible row by row.
func TestContextSectionBucketRowsSumToTheHeader(t *testing.T) {
	s := contextPanelScreen(t, 40)
	view := ansi.Strip(s.View())
	_, usage := contextUsageFixture()
	b := usage.Breakdown
	for _, row := range []struct{ label, value string }{
		{"system", "6k"},
		{"tools (48)", "21k"},
		{"memory", "2k"},
		{"messages", "12k"},
		{"results", "36k"},
		{"thinking", "2k"},
	} {
		found := false
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, row.label) && strings.Contains(line, row.value) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no sidebar row pairing %q with %q:\n%s", row.label, row.value, view)
		}
	}
	if got := b.Total(); got != usage.InputTokens {
		t.Errorf("fixture buckets sum to %d, header reports %d: the rows would contradict the header", got, usage.InputTokens)
	}
}

// TestContextBarSplitsFloorFromConversation: the fill is drawn in two runs,
// the floor first. A single-run bar would still fill the same number of
// cells, so the count of cells is not the assertion - the split point is.
func TestContextBarSplitsFloorFromConversation(t *testing.T) {
	s := contextPanelScreen(t, 24)
	inner := s.panelInnerWidth()
	_, usage := contextUsageFixture()
	floorPct := int(usage.Breakdown.Floor() * 100 / 128_000)
	wantFloor := render.ContextCells(floorPct, inner)
	if wantFloor <= 0 || wantFloor >= render.ContextCells(61, inner) {
		t.Fatalf("fixture is degenerate: floor cells = %d of a %d-cell fill", wantFloor, render.ContextCells(61, inner))
	}
	bar := s.panelContextBar(inner, 61, floorPct)
	full, _ := render.ContextGlyphs(s.Tier)
	floorRun := render.Role(s.Theme, s.Tier, theme.RoleBorder).Render(strings.Repeat(full, wantFloor))
	if !strings.HasPrefix(bar, floorRun) {
		t.Errorf("bar does not open with a %d-cell floor run in the border role:\n%q", wantFloor, bar)
	}
	if got := strings.Count(ansi.Strip(bar), full); got != render.ContextCells(61, inner) {
		t.Errorf("bar filled %d cells, want %d", got, render.ContextCells(61, inner))
	}
}

// TestContextSectionSurvivesAnUnknownBudget: an unbound session must draw the
// section's fixed rows without inventing a share, so nothing below it moves
// once a model binds.
func TestContextSectionSurvivesAnUnknownBudget(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	s.topbar.SetSession(ports.ModelInfo{Name: "m"}, ports.Usage{})
	rows := s.panelContextRows(s.panelInnerWidth(), 20)
	if len(rows) != contextSummaryRows {
		t.Fatalf("unknown budget drew %d rows, want the fixed %d", len(rows), contextSummaryRows)
	}
	if !strings.Contains(ansi.Strip(rows[0]), "unknown") {
		t.Errorf("header %q does not say the share is unknown", ansi.Strip(rows[0]))
	}
	if strings.Contains(ansi.Strip(rows[2]), "free") {
		t.Errorf("totals row %q reports free room against an unknown budget", ansi.Strip(rows[2]))
	}
}

// TestLiveUsageNeverGrowsTheFloor is the regression for what the sidebar
// showed during a turn: the system prompt and tool schema rows climbing while
// messages, results and thinking sat at zero. The session adopts a turn's
// messages only when the turn ends, so mid-turn its composition is the
// previous turn's, and reconciling it with the provider's growing total used
// to stretch every row including the two that cannot grow.
func TestLiveUsageNeverGrowsTheFloor(t *testing.T) {
	s := contextPanelScreen(t, 40)
	before := s.topbar.Usage().Breakdown
	if before.System == 0 || before.Total() == 0 {
		t.Fatal("precondition: the seeded session has a floor and a composition")
	}

	const liveTotal = 200_000
	live := ports.Usage{InputTokens: liveTotal}
	s.liveUsage = &live
	s.refreshTopbar()

	got := s.topbar.Usage()
	if got.InputTokens != liveTotal {
		t.Fatalf("live reading not adopted: InputTokens = %d, want %d", got.InputTokens, liveTotal)
	}
	if got.Breakdown.System != before.System {
		t.Errorf("system prompt grew from %d to %d during a turn; it cannot grow",
			before.System, got.Breakdown.System)
	}
	if got.Breakdown.ToolSchemas != before.ToolSchemas {
		t.Errorf("tool schemas grew from %d to %d during a turn; they cannot grow",
			before.ToolSchemas, got.Breakdown.ToolSchemas)
	}
	if got.Breakdown.Pending == 0 {
		t.Error("the unadopted cost of the running turn is not reported as pending")
	}
	if got.Breakdown.Total() != liveTotal {
		t.Errorf("breakdown sums to %d, header reports %d: the rows contradict the header",
			got.Breakdown.Total(), liveTotal)
	}
	if got.Breakdown.ToolCount != before.ToolCount {
		t.Errorf("ToolCount became %d, want %d: a schema count is not a token cost",
			got.Breakdown.ToolCount, before.ToolCount)
	}
}

// TestPendingTurnCostHasItsOwnRow: the pending cost is named on screen rather
// than hidden, so a reader can see that the growth belongs to the turn in
// flight and not to the floor.
func TestPendingTurnCostHasItsOwnRow(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 40, sampleDiffs()...))
	s.topbar.SetSession(
		ports.ModelInfo{Name: "m", ContextWindow: 400_000, DeclaredWindow: 400_000},
		ports.Usage{InputTokens: 90_000, Breakdown: ports.ContextBreakdown{
			System: 6_000, ToolSchemas: 3_000, ToolCount: 19, Pending: 81_000,
		}})
	rows := s.panelContextRows(s.panelInnerWidth(), contextDetailMinRows)
	joined := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(joined, "this turn") || !strings.Contains(joined, "81k") {
		t.Errorf("detail block does not report the pending turn cost:\n%s", joined)
	}
}

// cappedInfo is a 1M-window model held to a 400k operator prompt cap, the
// configuration that made the sidebar look like it had lost 600k.
func cappedInfo() ports.ModelInfo {
	return ports.ModelInfo{
		Name: "glm-5.3-flash", Provider: "zai",
		ContextWindow: 400_000, DeclaredWindow: 1_048_576,
	}
}

// TestCappedBudgetSaysWhereTheWindowWent: a budget far below the model's own
// window is a config choice, and unsaid it reads as capacity that vanished.
// The section must name the window it was capped from.
func TestCappedBudgetSaysWhereTheWindowWent(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	s.topbar.SetSession(cappedInfo(), ports.Usage{InputTokens: 10_000})
	rows := s.panelContextRows(s.panelInnerWidth(), 20)
	if len(rows) != contextSummaryRows+1 {
		t.Fatalf("capped section drew %d rows, want %d", len(rows), contextSummaryRows+1)
	}
	if got := ansi.Strip(rows[3]); !strings.Contains(got, "capped") || !strings.Contains(got, "1M") {
		t.Errorf("cap row = %q, want it to name the 1M window it was capped from", got)
	}
	if got := ansi.Strip(rows[2]); !strings.Contains(got, "10k of 400k") {
		t.Errorf("totals row = %q, want the budget it is actually measured against", got)
	}
}

// TestUncappedBudgetDrawsNoCapRow: with no operator cap the section must not
// spend a row saying nothing, and must not call an ordinary output reserve a
// cap.
func TestUncappedBudgetDrawsNoCapRow(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...))
	s.topbar.SetSession(
		ports.ModelInfo{Name: "glm-5.3-flash", ContextWindow: 1_015_808, DeclaredWindow: 1_048_576},
		ports.Usage{InputTokens: 10_000})
	rows := s.panelContextRows(s.panelInnerWidth(), 20)
	if len(rows) != contextSummaryRows {
		t.Fatalf("uncapped section drew %d rows, want %d", len(rows), contextSummaryRows)
	}
	if got := ansi.Strip(rows[2]); !strings.Contains(got, "1M") {
		t.Errorf("totals row = %q, want the full window as the budget", got)
	}
}

// TestServerToolsGetTheirOwnRow: server-supplied schemas are the part of the
// floor an operator can remove by turning a server off, so they are reported
// apart from the compiled-in tools rather than merged into one number.
func TestServerToolsGetTheirOwnRow(t *testing.T) {
	s := openPanel(t, panelScreen(t, uikitconfig.BreakpointWide, 40, sampleDiffs()...))
	s.topbar.SetSession(
		ports.ModelInfo{Name: "m", ContextWindow: 400_000, DeclaredWindow: 400_000},
		ports.Usage{InputTokens: 10_000, Breakdown: ports.ContextBreakdown{
			ToolSchemas: 3_000, ExternalSchemas: 5_000, ToolCount: 19, ExternalToolCount: 12,
			Prose: 2_000,
		}})
	rows := s.panelContextRows(s.panelInnerWidth(), contextDetailMinRows)
	joined := ansi.Strip(strings.Join(rows, "\n"))
	for _, want := range []string{"tools (19)", "servers (12)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail block missing %q:\n%s", want, joined)
		}
	}
	var toolsRow, serversRow string
	for _, r := range rows {
		plain := ansi.Strip(r)
		if strings.Contains(plain, "tools (19)") {
			toolsRow = plain
		}
		if strings.Contains(plain, "servers (12)") {
			serversRow = plain
		}
	}
	if !strings.Contains(toolsRow, "3k") {
		t.Errorf("tools row = %q, want the compiled-in schema cost alone", toolsRow)
	}
	if !strings.Contains(serversRow, "5k") {
		t.Errorf("servers row = %q, want the server schema cost alone", serversRow)
	}
}
