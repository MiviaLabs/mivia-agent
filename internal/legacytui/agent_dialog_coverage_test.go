package legacytui

// agent_dialog_coverage_test.go drives the agent-dialog helpers in
// agent_dialog.go that the legacytui runtime tests do not exercise
// individually. Each helper is a pure function on agents.ResolvedAgent
// and the registry.

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"

	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

func TestAgentListRowsEmpty(t *testing.T) {
	if rows := agentListRows(nil, ""); rows != nil {
		t.Errorf("agentListRows(nil) = %v, want nil", rows)
	}
}

func TestAgentListRowsWithCurrent(t *testing.T) {
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{Name: "alpha"})
	_ = reg.Publish(agents.ResolvedAgent{Name: "beta"})
	rows := agentListRows(reg, "beta")
	if len(rows) != 2 {
		t.Fatalf("agentListRows returned %d rows, want 2", len(rows))
	}
	found := false
	for _, r := range rows {
		if r.Name == "beta" && r.Current {
			found = true
		}
	}
	if !found {
		t.Errorf("expected beta to be marked current; rows=%+v", rows)
	}
}

func TestAgentDialogLifecycle(t *testing.T) {
	rows := []cli.AgentListRow{
		{Name: "alpha", Description: "first"},
		{Name: "beta", Current: true},
		{Name: "gamma"},
	}
	d := newAgentDialog(rows, false)
	if d == nil {
		t.Fatal("newAgentDialog returned nil")
	}
	// Cursor starts on the Current row.
	if d.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (Current row)", d.cursor)
	}
	// move within bounds and clamp at both ends.
	d.move(1)
	if d.cursor != 2 {
		t.Errorf("move(+1) cursor = %d, want 2", d.cursor)
	}
	d.move(5)
	if d.cursor != 2 {
		t.Errorf("move(+5) cursor = %d, want clamp to 2", d.cursor)
	}
	d.move(-10)
	if d.cursor != 0 {
		t.Errorf("move(-10) cursor = %d, want clamp to 0", d.cursor)
	}
	// move on empty rows is a no-op.
	empty := newAgentDialog(nil, false)
	empty.move(1) // must not panic
	// layout() returns a valid dialog geometry.
	lay := d.layout(80, 24)
	if lay.Rect.W <= 0 || lay.Rect.H <= 0 {
		t.Errorf("layout dims non-positive: %+v", lay)
	}
	// rowLines / rowLinesAt render rows; empty input renders the
	// placeholder line.
	if got := newAgentDialog(nil, false).rowLinesAt(40, 5, 0); len(got) != 1 {
		t.Errorf("rowLinesAt(empty) len = %d, want 1 placeholder", len(got))
	}
	lines := d.rowLines(40, 2)
	if len(lines) == 0 {
		t.Fatal("rowLines returned no lines")
	}
	// clampScroll across a page boundary.
	d.cursor = 0
	d.scroll = 0
	d.cursor = 2
	d.clampScroll(1)
	if d.scroll != 2 {
		t.Errorf("clampScroll cursor=2 page=1 scroll = %d, want 2", d.scroll)
	}
}
