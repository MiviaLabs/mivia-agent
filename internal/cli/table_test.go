package cli

import (
	"strings"
	"testing"
)

// TestTableFullPipeline verifies that a markdown table survives the full
// rendering pipeline: raw markdown → RenderMarkdown → wrapANSIv2 → viewport.
func TestTableFullPipeline(t *testing.T) {
	input := "| Key      | Behavior     |\n|----------|-------------|\n| Enter    | Send message |\n| Ctrl+C   | Cancel/quit  |\n| Tab      | Select tool  |\n"

	// Step 1: RenderMarkdown produces ANSI-coded output
	rendered := RenderMarkdown(input, 78)
	t.Logf("RenderMarkdown output:\n%s", rendered)

	// Verify ANSI codes are present (dim borders)
	if !strings.Contains(rendered, ansiDim) {
		t.Fatal("expected dim ANSI codes in rendered table")
	}
	if !strings.Contains(rendered, "│") {
		t.Fatal("expected │ separators in rendered table")
	}
	if !strings.Contains(rendered, "Enter") {
		t.Fatal("expected cell content 'Enter' in rendered table")
	}
	if !strings.Contains(rendered, "Send message") {
		t.Fatal("expected cell content 'Send message' in rendered table")
	}
	if !strings.Contains(rendered, "Cancel") {
		t.Fatal("expected cell content in rendered table")
	}

	// Step 2: wrapANSIv2 should preserve ANSI codes and wrap long lines
	wrapped := wrapANSIv2(rendered, 40)
	t.Logf("wrapANSIv2 output (width=40):\n%s", wrapped)

	if !strings.Contains(wrapped, ansiDim) {
		t.Fatal("wrapANSIv2 dropped dim ANSI codes")
	}
	if !strings.Contains(wrapped, "│") {
		t.Fatal("wrapANSIv2 dropped │ separators")
	}
	// Each line should be within width
	for _, line := range strings.Split(wrapped, "\n") {
		if len(line) > 0 {
			vis := visibleWidth(line)
			if vis > 40 {
				t.Errorf("line %q has visible width %d > 40", line, vis)
			}
		}
	}
}

// TestTableHistoryRoundTrip tests that a table in session history loads,
// renders, and displays correctly in the viewport context.
func TestTableHistoryRoundTrip(t *testing.T) {
	// Simulate what happens when a message with a table comes from session history.
	content := "Here are the keybindings:\n\n| Key    | Action             |\n|--------|-------------------|\n| Enter  | Send message       |\n| Ctrl+C | Cancel or quit     |\n| Esc    | Deselect tool      |\n| Tab    | Navigate tools     |\n| G      | Scroll to bottom   |\n\nUse these to navigate the interface."

	// Step 1: RenderMarkdown (as done in runTUI and loadMoreMessages)
	rendered := RenderMarkdown(content, 78)
	t.Logf("RenderMarkdown:\n%s", rendered)

	if !strings.Contains(rendered, ansiDim) {
		t.Fatal("RenderMarkdown should produce dim ANSI for table borders")
	}
	if !strings.Contains(rendered, "│") {
		t.Fatal("RenderMarkdown should produce │ table separators")
	}
	if strings.Contains(rendered, "|---|---|") {
		t.Fatal("RenderMarkdown should DROP separator rows")
	}
	if !strings.Contains(rendered, "Scroll to bottom") {
		t.Fatal("should contain all rows")
	}

	// Step 2: Simulate wrapping at viewport width
	wrapped := wrapANSIv2(rendered, 60)
	t.Logf("Wrapped at 60:\n%s", wrapped)

	if !strings.Contains(wrapped, ansiDim) {
		t.Fatal("ANSI codes preserved after wrapping")
	}
	if !strings.Contains(wrapped, "│") {
		t.Fatal("│ separators preserved after wrapping")
	}
	if !strings.Contains(wrapped, "Scroll to bottom") {
		t.Fatal("content preserved after wrapping")
	}

	// Step 3: Verify viewport content building (simulating buildViewportContent)
	// Store rendered as a message, then build viewport content from it
	messages := []string{
		tuiHeaderStyle.Render("── you ──"),
		"what are the keybindings?",
		tuiHeaderStyle.Render("── deepseek-v4-flash ──"),
		wrapped,
	}

	vpContent := strings.Join(messages, "\n")
	if !strings.Contains(vpContent, "│") {
		t.Fatal("viewport content missing │")
	}
	// Check that separator row (|---|---|) is NOT in the output.
	if strings.Contains(vpContent, "---") {
		// Make sure it's not a standalone separator row (one or more dashes between pipes).
		for _, line := range strings.Split(vpContent, "\n") {
			plain := stripAnsiOut(line)
			trimmed := strings.TrimSpace(plain)
			if strings.HasPrefix(trimmed, "|") && strings.Contains(trimmed, "---") {
				t.Fatalf("separator row leaked into output: %q", line)
			}
		}
	}
	// Check data rows preserved using stripped text.
	plain := stripAnsiOut(vpContent)
	if !strings.Contains(plain, "Send message") {
		t.Fatal("viewport content missing 'Send message'")
	}
	if !strings.Contains(plain, "Scroll to bottom") {
		t.Fatal("viewport content missing 'Scroll to bottom'")
	}
}

// TestTableNoDataLoss verifies that table content is NOT lost through pipeline.
func TestTableNoDataLoss(t *testing.T) {
	input := "| Package | Description |\n|---------|------------|\n| cli     | TUI handler |\n| agent   | Agent loop  |\n| tools   | File tools  |\n"

	rendered := RenderMarkdown(input, 78)
	wrapped := wrapANSIv2(rendered, 60)

	// Strip all ANSI and line breaks to check content preservation
	plain := stripAnsiOut(wrapped)
	plain = strings.ReplaceAll(plain, "\n", " ")
	plain = strings.Join(strings.Fields(plain), " ")

	for _, want := range []string{"Package", "Description", "cli", "TUI handler", "agent", "Agent loop", "tools", "File tools"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing content %q in pipeline output: %q", want, plain)
		}
	}
}

// TestTableSeparatorDropped verifies that separator rows are completely
// removed from output (not rendered as plain text).
func TestTableSeparatorDropped(t *testing.T) {
	input := "| A | B |\n|---|---|\n| 1 | 2 |\n"
	rendered := RenderMarkdown(input, 78)

	// Separator row should be COMPLETELY gone — not even as raw text.
	for _, line := range strings.Split(strings.TrimSpace(rendered), "\n") {
		plain := stripAnsiOut(line)
		trimmed := strings.TrimSpace(plain)
		if strings.HasPrefix(trimmed, "|") && strings.Contains(trimmed, "---") {
			t.Fatalf("separator row leaked: %q in output %q", line, rendered)
		}
	}

	// Should have exactly 2 data rows (header + 1 data) with │
	lines := strings.Split(strings.TrimSpace(rendered), "\n")
	dataLines := 0
	for _, line := range lines {
		plain := stripAnsiOut(line)
		if strings.Contains(plain, "│") {
			dataLines++
		}
	}
	if dataLines != 2 {
		t.Fatalf("expected exactly 2 table data lines (header + data), got %d in %q", dataLines, rendered)
	}
}
