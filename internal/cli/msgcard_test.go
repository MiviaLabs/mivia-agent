package cli

import (
	"strings"
	"testing"
	"time"
)

func TestFormatUserMessageCard_NoBorderKeepsBodyAndTime(t *testing.T) {
	sent := time.Date(2026, 7, 27, 15, 4, 5, 0, time.Local)
	lines := formatUserMessageCard("hello world", 40, sent)
	if len(lines) < 1 {
		t.Fatalf("expected ≥1 line, got %d", len(lines))
	}
	plain := stripANSI(strings.Join(lines, "\n"))
	if strings.Contains(plain, "╭") || strings.Contains(plain, "╰") || strings.Contains(plain, "│") {
		t.Fatalf("expected no box borders, got %q", plain)
	}
	if strings.Contains(plain, "you") {
		t.Fatalf("expected time label not 'you', got %q", plain)
	}
	local := sent.In(time.Local).Format("15:04:05")
	if !strings.Contains(plain, local) {
		t.Fatalf("expected local time %q in %q", local, plain)
	}
	if !strings.Contains(plain, "hello world") {
		t.Fatalf("expected content after time, got %q", plain)
	}
}

func TestFormatUserMessageCard_WrapsLongContent(t *testing.T) {
	long := strings.Repeat("word ", 30)
	lines := formatUserMessageCard(long, 24, time.Now())
	if len(lines) < 2 {
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

func TestFormatUserMessageCard_ZeroTimeStillShowsBody(t *testing.T) {
	lines := formatUserMessageCard("body only", 40, time.Time{})
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "body only") {
		t.Fatalf("expected body without time, got %q", plain)
	}
}

func TestFormatModelHeader_NoChrome(t *testing.T) {
	if h := formatModelHeader("deepseek-v4", 40); h != "" {
		t.Fatalf("expected empty model header (no border), got %q", h)
	}
	if f := formatModelFooter(40); f != "" {
		t.Fatalf("expected empty model footer, got %q", f)
	}
}
