package config

import (
	"strings"
	"testing"
)

func TestToolResultBytesWarningsHuge(t *testing.T) {
	warns := ToolResultBytesWarnings(ToolsConfig{MaxToolResultBytes: UsefulToolResultRequestBytes + 1})
	if len(warns) != 1 {
		t.Fatalf("want 1 warning, got %v", warns)
	}
	if !strings.Contains(warns[0], "not clamped") {
		t.Fatalf("warning must say not clamped: %q", warns[0])
	}
}

func TestToolResultBytesWarningsNoClampSmallOrZero(t *testing.T) {
	for _, v := range []int{0, 1024, UsefulToolResultRequestBytes} {
		if got := ToolResultBytesWarnings(ToolsConfig{MaxToolResultBytes: v}); len(got) != 0 {
			t.Fatalf("value %d: unexpected warnings %v", v, got)
		}
	}
}
