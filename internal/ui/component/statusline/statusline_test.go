package statusline

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func TestViewEmptyWhenInactive(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	if got := m.View(time.Now()); got != "" {
		t.Errorf("got %q, want empty view when inactive", got)
	}
}

func TestStartArmsAndReturnsTickCmd(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cmd := m.Start("thinking", start)
	if cmd == nil {
		t.Fatal("expected Start to return a tick Cmd")
	}
	if _, ok := cmd().(TickMsg); !ok {
		t.Errorf("got %T, want the scheduled Cmd to yield TickMsg", cmd())
	}
	if !m.Active() {
		t.Fatal("expected Active() after Start")
	}
	got := m.View(start.Add(3 * time.Second))
	// "3.0s" is the shared FormatElapsed ladder (transcript-polish.md R5),
	// not the old Go time.Duration String() output.
	for _, want := range []string{"THINKING", "3.0s"} {
		if !strings.Contains(got, want) {
			t.Errorf("statusline view missing %q: %q", want, got)
		}
	}
}

func TestStopClearsView(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.Start("thinking", time.Now())
	m.Stop()
	if m.Active() {
		t.Error("expected Active() false after Stop")
	}
	if got := m.View(time.Now()); got != "" {
		t.Errorf("got %q, want empty view after Stop", got)
	}
}

func TestUpdateTickAdvancesFrameAndInactiveIgnoresTick(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	// Inactive: a stray tick must be a no-op with no rescheduling.
	next, cmd := m.Update(TickMsg{})
	if cmd != nil {
		t.Error("expected no Cmd from a tick while inactive")
	}
	m = next

	m.Start("running", time.Now())
	before := m.frame
	next, cmd = m.Update(TickMsg{})
	if cmd == nil {
		t.Error("expected the tick to reschedule while active")
	}
	if next.frame == before {
		t.Error("expected the spinner frame to advance on tick")
	}
}

func TestSetLabelDoesNotResetElapsed(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.Start("thinking", start)
	m.SetLabel("running tool")
	got := m.View(start.Add(5 * time.Second))
	if !strings.Contains(got, "RUNNING~") || !strings.Contains(got, "5.0s") {
		t.Errorf("got %q, want label updated and elapsed clock preserved", got)
	}
}

// TestMarkCarriesItsStateRole pins that the BRAND MARK carries the state
// colour (running fg, pending warning, failed danger, done success).
// Waiting and pending labels wear RoleWarning; all other labels stay subtle.
func TestMarkCarriesItsStateRole(t *testing.T) {
	th := loadTheme(t)
	cases := []struct {
		label     string
		markRole  theme.Role
		labelRole theme.Role
	}{
		{"running", theme.RoleFG, theme.RoleFGSubtle},
		{"waiting", theme.RoleWarning, theme.RoleWarning},
		{"pending", theme.RoleWarning, theme.RoleWarning},
		{"failed", theme.RoleDanger, theme.RoleFGSubtle},
		{"done", theme.RoleSuccess, theme.RoleFGSubtle},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			m := New(th, theme.TierTrueColor)
			m.Start(c.label, time.Now())
			got := m.View(time.Now())
			wantMark := render.Role(th, theme.TierTrueColor, c.markRole)
			hasGlyph := strings.Contains(got, wantMark.Render("⠶")) ||
				strings.Contains(got, wantMark.Render("⠛")) ||
				strings.Contains(got, wantMark.Render("⠿")) ||
				strings.Contains(got, wantMark.Render("⣿")) ||
				strings.Contains(got, wantMark.Render("⣶")) ||
				strings.Contains(got, wantMark.Render("⬖")) ||
				strings.Contains(got, wantMark.Render("◈")) ||
				strings.Contains(got, wantMark.Render("◆"))
			if !hasGlyph {
				t.Errorf("got %q, want the mark styled with %s", got, c.markRole)
			}
			wantLabel := render.Role(th, theme.TierTrueColor, c.labelRole).Render(fmt.Sprintf("%-8s", strings.ToUpper(c.label)))
			if !strings.Contains(got, wantLabel) {
				t.Errorf("got %q, want the label styled with %s (%q)", got, c.labelRole, wantLabel)
			}
		})
	}
}

// TestThinkingLabelHasNoStateRole: "thinking" has no status colour
// (monochrome), so its label renders in RoleFGSubtle rather than
// picking up a warning/danger color.
func TestThinkingLabelHasNoStateRole(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.Start("thinking", time.Now())
	got := m.View(time.Now())
	want := render.Role(th, theme.TierTrueColor, theme.RoleFGSubtle).Render("THINKING")
	if !strings.Contains(got, want) {
		t.Errorf("got %q, want thinking in subtle style: %q", got, want)
	}
}

// TestNoticeShowsWithoutATurn pins the case the copy key needs: there is
// no turn in flight, but the action still has to say what it did.
func TestNoticeShowsWithoutATurn(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	if m.Active() {
		t.Fatal("a new status line draws nothing")
	}
	m.Notice("copied the block")
	if !m.Active() {
		t.Error("a notice must claim its row, or the layout will not reserve it")
	}
	if got := m.View(time.Now()); !strings.Contains(got, "copied the block") {
		t.Errorf("got %q, want the notice text", got)
	}

	m.ClearNotice()
	if m.Active() || m.View(time.Now()) != "" {
		t.Error("ClearNotice left the line drawing")
	}
}

// TestStartClearsAPendingNotice: a notice belongs to the action that
// raised it, so a new turn must not inherit it.
func TestStartClearsAPendingNotice(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.Notice("copied the block")
	m.Start("thinking", time.Now())
	if got := m.View(time.Now()); strings.Contains(got, "copied") {
		t.Errorf("got %q, want the new turn's line, not the stale notice", got)
	}
}

// TestNoticeWinsOverTheTurnLine: the notice is the newer information,
// and the turn line returns as soon as it is cleared.
func TestNoticeTakesTheRowWhileSet(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.Start("thinking", time.Now())
	m.Notice("copied the block")
	if got := m.View(time.Now()); !strings.Contains(got, "copied") {
		t.Errorf("got %q, want the notice", got)
	}
	m.ClearNotice()
	if got := m.View(time.Now()); !strings.Contains(got, "THINKING") {
		t.Errorf("got %q, want the turn line back", got)
	}
}

// TestSetDetailShowsBesideTheLabel pins wireframes-panes.md section 9's
// active-turn line shape: "- running  <detail>   12s". Before SetDetail
// existed, the line had no way to show WHICH tool/command was running,
// only the generic "running" word.
func TestSetDetailShowsBesideTheLabel(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.Start("thinking", start)
	m.SetLabel("running")
	m.SetDetail("run_command command=go test ./...")
	got := m.View(start.Add(3 * time.Second))
	if !strings.Contains(got, "run_command command=go test ./...") {
		t.Errorf("got %q, want the detail shown beside the label", got)
	}
}

// TestSetLabelClearsThePreviousDetail: a label change without a new
// detail (e.g. back to "thinking" once a tool call ends) must not keep
// showing the PREVIOUS tool's detail - that would misreport what is
// happening now.
func TestSetLabelClearsThePreviousDetail(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.Start("thinking", start)
	m.SetLabel("running")
	m.SetDetail("run_command command=go test ./...")
	m.SetLabel("thinking")
	got := m.View(start.Add(1 * time.Second))
	if strings.Contains(got, "run_command") {
		t.Errorf("got %q, want the stale detail cleared on label change", got)
	}
}

func TestStatusLineBadgeFixedFourteenRunes(t *testing.T) {
	th := loadTheme(t)
	labels := []string{"thinking", "running", "waiting", "pending", "done", "failed", "x", "running tool long label"}
	for _, tier := range []theme.Tier{theme.TierTrueColor, theme.TierASCII} {
		for _, lbl := range labels {
			m := New(th, tier)
			m.Start(lbl, time.Now())
			v := m.View(time.Now())
			// Find the capsule [ ... ]
			plain := ansi.Strip(v)
			open := strings.Index(plain, "[")
			close := strings.Index(plain, "]")
			if open < 0 || close < 0 || close <= open {
				t.Fatalf("tier %v, label %q: no capsule found in view %q", tier, lbl, plain)
			}
			badge := plain[open : close+1]
			if w := ansi.StringWidth(badge); w != 14 {
				t.Errorf("tier %v, label %q: badge %q width = %d, want 14", tier, lbl, badge, w)
			}
		}
	}
}

func TestStatusLineDividerTierGated(t *testing.T) {
	th := loadTheme(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(3 * time.Second)

	// TrueColor tier uses hairline middle dot divider " · "
	mTrue := New(th, theme.TierTrueColor)
	mTrue.Start("running", start)
	mTrue.SetDetail("go test ./...")
	vTrue := mTrue.View(now)
	if !strings.Contains(vTrue, "go test ./... · 3.0s") {
		t.Errorf("got %q, want 'go test ./... · 3.0s' on TrueColor tier", vTrue)
	}

	// ASCII tier uses " - " divider
	mAscii := New(th, theme.TierASCII)
	mAscii.Start("running", start)
	mAscii.SetDetail("go test ./...")
	vAscii := mAscii.View(now)
	if !strings.Contains(vAscii, "go test ./... - 3.0s") {
		t.Errorf("got %q, want 'go test ./... - 3.0s' on ASCII tier", vAscii)
	}
	if strings.Contains(vAscii, "·") {
		t.Errorf("ASCII tier must not contain unicode middle dot '·': %q", vAscii)
	}
}

func TestStatusLineSafetyPillsAndTelemetry(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierASCII)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.Start("thinking", start)
	m.SetSafetyMode("safe")
	m.SetCost(0.05)

	got := m.View(start)
	if !strings.Contains(got, "[SAFE]") {
		t.Errorf("got %q, want [SAFE] pill in ASCII mode", got)
	}
	if strings.Contains(got, "ctx") {
		t.Errorf("got %q, want no ctx indicator in bottom status line", got)
	}
	if !strings.Contains(got, "$0.05") {
		t.Errorf("got %q, want $0.05 cost in status line", got)
	}

	m.SetSafetyMode("auto")
	gotAuto := m.View(start)
	if !strings.Contains(gotAuto, "[AUTO]") {
		t.Errorf("got %q, want [AUTO] pill in ASCII mode", gotAuto)
	}
}

func TestMarkViewAndMarkGlyphDelegateToTheMark(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor)
	if got := m.MarkView(); got != m.Mark().View() {
		t.Errorf("MarkView() = %q, want it to match Mark().View() = %q", got, m.Mark().View())
	}
	if got := m.MarkGlyph(); got != m.Mark().Glyph() {
		t.Errorf("MarkGlyph() = %q, want it to match Mark().Glyph() = %q", got, m.Mark().Glyph())
	}
}
