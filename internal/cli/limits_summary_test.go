package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestFormatEffectiveLimitsSummaryUnlimited(t *testing.T) {
	res := &config.Resolved{
		Tools: config.ToolsConfig{
			MemoryBackstopMB: config.DefaultMemoryBackstopMB,
		},
	}
	line := formatEffectiveLimitsSummary(res)
	if !strings.Contains(line, "tool volume caps unlimited") {
		t.Fatalf("expected unlimited phrase: %q", line)
	}
	if !strings.Contains(line, "memory_backstop_mb=256") {
		t.Fatalf("expected backstop in summary: %q", line)
	}
	if !strings.Contains(line, "max_source_event_bytes") {
		t.Fatalf("expected context chunk note: %q", line)
	}
}

func TestFormatEffectiveLimitsSummaryBounded(t *testing.T) {
	res := &config.Resolved{
		Tools: config.ToolsConfig{
			MaxReadBytes:       1024,
			MaxToolResultBytes: 4096,
			MemoryBackstopMB:   128,
		},
	}
	line := formatEffectiveLimitsSummary(res)
	if strings.Contains(line, "unlimited") {
		t.Fatalf("bounded config should not say unlimited: %q", line)
	}
	if !strings.Contains(line, "max_read_bytes=1024") || !strings.Contains(line, "memory_backstop_mb=128") {
		t.Fatalf("summary = %q", line)
	}
}

func TestLogEffectiveLimitsOnceWarnsHugeToolResult(t *testing.T) {
	res := &config.Resolved{
		Tools: config.ToolsConfig{
			MaxToolResultBytes: config.UsefulToolResultRequestBytes + 1,
			MemoryBackstopMB:   256,
		},
	}
	var buf bytes.Buffer
	logEffectiveLimitsOnce(&buf, res)
	out := buf.String()
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "not clamped") {
		t.Fatalf("expected warn: %q", out)
	}
	if !strings.Contains(out, "limits:") {
		t.Fatalf("expected limits line: %q", out)
	}
}
