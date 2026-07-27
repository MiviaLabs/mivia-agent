package cli

import (
	"strings"
	"testing"
)

func TestFormatUserMessageCard_BorderAndLabel(t *testing.T) {
	lines := formatUserMessageCard("hello world", 40)
	if len(lines) < 3 {
		t.Fatalf("expected ≥3 lines, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") || !strings.Contains(plain, "│") {
		t.Fatalf("expected box borders, got %q", plain)
	}
	if !strings.Contains(plain, "you") {
		t.Fatalf("expected you label, got %q", plain)
	}
	if !strings.Contains(plain, "hello world") {
		t.Fatalf("expected content, got %q", plain)
	}
}

func TestFormatUserMessageCard_WrapsLongContent(t *testing.T) {
	long := strings.Repeat("word ", 30)
	lines := formatUserMessageCard(long, 24)
	if len(lines) < 4 {
		t.Fatalf("expected multi-line card for long content, got %d lines: %v", len(lines), lines)
	}
	for _, line := range lines {
		if visibleWidth(line) > 24 {
			t.Fatalf("line exceeds width 24: vis=%d %q", visibleWidth(line), stripANSI(line))
		}
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "word") {
		t.Fatalf("expected wrapped content, got %q", plain)
	}
}

func TestFormatModelHeader_Chrome(t *testing.T) {
	h := formatModelHeader("deepseek-v4", 40)
	plain := stripANSI(h)
	if !strings.Contains(plain, "╭─") {
		t.Fatalf("expected ╭─ chrome, got %q", plain)
	}
	if !strings.Contains(plain, "deepseek-v4") {
		t.Fatalf("expected model name, got %q", plain)
	}
}
