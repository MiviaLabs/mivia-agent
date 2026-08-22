package clichat

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
	logEffectiveLimitsOnce(&buf, res, false)
	out := buf.String()
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "not clamped") {
		t.Fatalf("expected warn: %q", out)
	}
	if !strings.Contains(out, "limits:") {
		t.Fatalf("expected limits line: %q", out)
	}
}

func TestLogEffectiveLimitsOnceQuietSuppresses(t *testing.T) {
	res := &config.Resolved{
		Tools: config.ToolsConfig{
			MaxToolResultBytes: config.UsefulToolResultRequestBytes + 1,
			MemoryBackstopMB:   256,
		},
	}
	var buf bytes.Buffer
	logEffectiveLimitsOnce(&buf, res, true)
	out := buf.String()
	if strings.Contains(out, "limits:") {
		t.Fatalf("quiet should suppress the limits summary line, got %q", out)
	}
	// Warnings that signal a misconfiguration still print under --quiet.
	if !strings.Contains(out, "warning:") {
		t.Fatalf("quiet must not hide config warnings, got %q", out)
	}
}

func TestLogEffectiveLimitsOnceWarnsPlaintextMCP(t *testing.T) {
	res := &config.Resolved{MCPWarnings: []string{"MCP server \"plain\" uses plaintext HTTP"}}
	var buf bytes.Buffer
	logEffectiveLimitsOnce(&buf, res, false)
	if out := buf.String(); !strings.Contains(out, "warning: MCP server \"plain\" uses plaintext HTTP") {
		t.Fatalf("operator output = %q", out)
	}
}

func TestLogMCPWarningsForWorkflowDiagnostics(t *testing.T) {
	res := &config.Resolved{MCPWarnings: []string{"MCP server \"plain\" uses plaintext HTTP"}}
	var buf bytes.Buffer
	logMCPWarnings(&buf, res)
	if got := buf.String(); got != "warning: MCP server \"plain\" uses plaintext HTTP\n" {
		t.Fatalf("workflow diagnostics = %q", got)
	}
}
