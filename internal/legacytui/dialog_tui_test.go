package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestHelpReflowsAfterResize(t *testing.T) {
	d := newHelpDialog(20)
	narrow := d.displayRows(d.layout(50, 20).InnerW)
	wideLayout := d.layout(120, 30)
	wide := d.displayRows(wideLayout.InnerW)
	if len(wide) >= len(narrow) {
		t.Fatalf("help did not reflow to wider content area: narrow=%d wide=%d", len(narrow), len(wide))
	}
	if !strings.Contains(cli.StripANSI(strings.Join(wide, "\n")), "current") {
		t.Fatal("wide help lost semantic description content")
	}
}

func TestStatusDialogOverflowPolicy(t *testing.T) {
	d := newDialog("◇ status · captured at open", []string{
		"Session", "  model  test", "  workspace  /tmp", "  messages  3", "  turns  1", "  blocks  2",
		"Current turn", "  elapsed  1s", "  tools open  2", "    ◆ alpha  2 done, 0 open", "    ◆ beta  1 done, 1 open",
	})
	d.kind = "status"
	l := d.layout(40, 12)
	rows := d.rowsForLayout(l.InnerW, 4)
	plain := cli.StripANSI(strings.Join(rows, "\n"))
	if strings.Contains(plain, "◆ alpha") || strings.Contains(plain, "◆ beta") {
		t.Fatalf("status overflow retained detailed agent rows: %q", plain)
	}
	if !strings.Contains(plain, "agents: 2") {
		t.Fatalf("status overflow omitted compact agent count: %q", plain)
	}
	rows = d.rowsForLayout(l.InnerW, 4)
	plain = cli.StripANSI(strings.Join(rows, "\n"))
	for _, fact := range []string{"model", "workspace", "messages", "turns", "agents: 2"} {
		if !strings.Contains(plain, fact) {
			t.Fatalf("status narrow fallback omitted core fact %q: %q", fact, plain)
		}
	}
	tiny := d.rowsForLayout(10, 4)
	if len(tiny) > 4 {
		t.Fatalf("tiny status exceeded available rows: %q", tiny)
	}
	for _, row := range tiny {
		if width := ansi.StringWidth(row); width > 10 {
			t.Fatalf("tiny status row width=%d: %q", width, row)
		}
	}
	tinyRows := d.rowsForLayout(10, 3)
	if len(tinyRows) > 3 {
		t.Fatalf("three-row status exceeded available rows: %q", tinyRows)
	}
}

func TestStatusAndFleetSnapshotPolicy(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.modelName = "before"
	status := m.newStatusDialog()
	m.modelName = "after"
	if !strings.Contains(cli.StripANSI(strings.Join(status.lines, "\n")), "before") || strings.Contains(cli.StripANSI(strings.Join(status.lines, "\n")), "after") {
		t.Fatal("status dialog was not a snapshot captured at open")
	}
	if !strings.Contains(status.title, "captured at open") {
		t.Fatalf("status title does not identify snapshot semantics: %q", status.title)
	}
	m.waiting = true
	feedAgents(m, 1)
	if !m.openFleetOverlay() || !strings.Contains(m.overlay.title, "captured at open") {
		t.Fatal("fleet detail did not identify its captured-at-open snapshot")
	}
	m.setOverlay(nil)
	m.waiting = false
	if handled := m.openFleetOverlay(); !handled || m.overlay != nil {
		t.Fatal("stale fleet rows reopened as an active modal after the turn")
	}
}

func TestDialogProducerPrefs(t *testing.T) {
	cases := []struct {
		title          string
		wantW, wantMin int
		pager          bool
	}{
		{"◇ status", 60, 32, false}, {"⚙ tools", 50, 28, true}, {"? help", 76, 40, true},
	}
	for _, tc := range cases {
		p := dialogPrefsForTitle(tc.title)
		if p.PreferredW != tc.wantW || p.MinW != tc.wantMin || p.Pager != tc.pager {
			t.Fatalf("prefs(%q)=%+v", tc.title, p)
		}
	}
}

func TestBlockOverlayPreservesLongLines(t *testing.T) {
	block := cli.ChatBlock{Kind: cli.ChatBlockTool, ToolName: "output", Text: strings.Repeat("界🙂", 80)}
	o := newBlockOverlay(block)
	view, layout := o.ViewAt(50, 12)
	if len(o.displayRows(layout.InnerW)) < 2 || view == "" {
		t.Fatal("long content was not wrapped into reachable display rows")
	}
	o.yOffset = 1 << 30
	view, _ = o.ViewAt(50, 12)
	if !strings.Contains(cli.StripANSI(view), "🙂") {
		t.Fatal("final wrapped row was not reachable")
	}
}

func TestDialogProducerRedactsUntrustedLabels(t *testing.T) {
	block := newBlockOverlay(cli.ChatBlock{ToolName: "\x1b[31mtool", AgentName: "\x1b[32magent"})
	if strings.Contains(block.title, "\x1b") {
		t.Fatalf("block title retained terminal control: %q", block.title)
	}
	m := newReadyChatModel(24, 80)
	tools := m.newToolsDialog([]string{"\x1b[33mtool"})
	if strings.Contains(strings.Join(tools.lines, "\n"), "\x1b") {
		t.Fatalf("tools dialog retained terminal control: %q", tools.lines)
	}
	row := fleetRowLine(cli.SubagentRun{Name: "\x1b[34magent", LastDetail: "\x1b[35mdetail"}, 40, time.Now())
	if strings.Contains(row, "\x1b") {
		t.Fatalf("fleet row retained terminal control: %q", row)
	}
}
