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

// TestTableColumnsAlign checks that column borders line up across rows.
func TestTableColumnsAlign(t *testing.T) {
	input := "| Name | Age | City |\n|------|-----|------|\n| Alice | 30 | NYC |\n| Bob | 25 | SF |\n"
	rendered := RenderMarkdown(input, 78)
	var tableLines []string
	// Do not TrimSpace the whole document — leading spaces on the first table row are padding.
	for _, line := range strings.Split(rendered, "\n") {
		plain := stripAnsiOut(line)
		if strings.Contains(plain, "│") {
			tableLines = append(tableLines, plain)
		}
	}
	if len(tableLines) < 2 {
		t.Fatalf("need ≥2 table lines, got %v", tableLines)
	}
	// Second │ position (end of col0) should match across rows.
	posSecond := func(s string) int {
		first := strings.Index(s, "│")
		if first < 0 {
			return -1
		}
		return strings.Index(s[first+len("│"):], "│") + first + len("│")
	}
	ref := posSecond(tableLines[0])
	if ref < 0 {
		t.Fatalf("no second border in %q", tableLines[0])
	}
	for _, line := range tableLines[1:] {
		p := posSecond(line)
		if p != ref {
			t.Errorf("column misaligned: second │ at %d vs ref %d\n  %q\n  %q", p, ref, tableLines[0], line)
		}
		if len(line) != len(tableLines[0]) {
			// Same structure: equal total visible width preferred.
			if visibleWidth(line) != visibleWidth(tableLines[0]) {
				t.Errorf("row visible width %d != header %d\n  %q\n  %q",
					visibleWidth(line), visibleWidth(tableLines[0]), tableLines[0], line)
			}
		}
	}
}

// TestTableGFMSeparatorWithColonsDropped ensures :--- / ---: / :---: separators
// are dropped and leave no blank line between header and body.
func TestTableGFMSeparatorWithColonsDropped(t *testing.T) {
	input := "| Left | Center | Right |\n|:-----|:------:|------:|\n| a | b | c |\n"
	rendered := RenderMarkdown(input, 78)
	t.Logf("rendered:\n%s", rendered)

	for _, line := range strings.Split(rendered, "\n") {
		plain := stripAnsiOut(line)
		if gfmSepCell.MatchString(strings.TrimSpace(strings.ReplaceAll(plain, "│", ""))) {
			t.Fatalf("separator-like content leaked: %q", line)
		}
		if strings.Contains(plain, "---") && strings.Contains(plain, "│") {
			// dash-only cells should not appear as table rows
			t.Fatalf("separator row leaked: %q", line)
		}
	}

	// No blank line between the two data rows.
	lines := strings.Split(strings.TrimSpace(rendered), "\n")
	var idx []int
	for i, line := range lines {
		if strings.Contains(stripAnsiOut(line), "│") {
			idx = append(idx, i)
		}
	}
	if len(idx) != 2 {
		t.Fatalf("expected 2 data rows, got %d in %q", len(idx), rendered)
	}
	if idx[1] != idx[0]+1 {
		t.Fatalf("blank line between header/body: indices %v in %q", idx, rendered)
	}
	// Content preserved.
	plain := stripAnsiOut(rendered)
	for _, want := range []string{"Left", "Center", "Right", "a", "b", "c"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// TestTableNarrowWrapNoSplit ensures wrapANSIv2 does not soft-wrap a rendered
// table row into multiple lines (hard truncate at width instead).
func TestTableNarrowWrapNoSplit(t *testing.T) {
	input := "| Key      | Behavior     |\n|----------|-------------|\n| Enter    | Send message |\n| Ctrl+C   | Cancel/quit  |\n"
	rendered := RenderMarkdown(input, 78)
	dataBefore := 0
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(stripAnsiOut(line), "│") {
			dataBefore++
		}
	}

	wrapped := wrapANSIv2(rendered, 30)
	t.Logf("wrapped@30:\n%s", wrapped)

	dataAfter := 0
	for _, line := range strings.Split(wrapped, "\n") {
		plain := stripAnsiOut(line)
		if strings.Contains(plain, "│") {
			dataAfter++
			if visibleWidth(line) > 30 {
				t.Errorf("table line exceeds 30: vis=%d %q", visibleWidth(line), plain)
			}
			// Soft-wrap would leave a continuation without leading │ — each
			// original table row must remain a single physical line.
			if !isRenderedTableRow(line) && strings.Contains(plain, "│") {
				t.Errorf("unexpected mid-row fragment: %q", plain)
			}
		}
	}
	if dataAfter != dataBefore {
		t.Fatalf("wrap split table rows: before=%d after=%d\n%s", dataBefore, dataAfter, wrapped)
	}
	// Content still present (possibly truncated with … on very narrow width).
	plain := stripAnsiOut(wrapped)
	if !strings.Contains(plain, "Enter") && !strings.Contains(plain, "…") {
		t.Fatalf("expected Enter or truncation marker in %q", plain)
	}
}
