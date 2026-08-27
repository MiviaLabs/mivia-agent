package clichat

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// writeMinimalLoadableConfig writes a config with a declared provider and
// model but no [tools] section, so every tools knob resolves from defaults -
// exercising the real resolveToolsConfig path a hand-constructed
// config.ToolsConfig{} literal never goes through.
func writeMinimalLoadableConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	body := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\n" +
		"models = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

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
	if !strings.Contains(line, "max_read_bytes=1024") || !strings.Contains(line, "max_edit_file_bytes=0") || !strings.Contains(line, "memory_backstop_mb=128") {
		t.Fatalf("summary = %q", line)
	}
}

// TestFormatEffectiveLimitsSummaryUnlimitedSurvivesResolution pins the actual
// regression: MaxEditFileBytes is a memory bound that resolveToolsConfig
// always fills to a positive value, so a literal-constructed ToolsConfig (as
// the other tests here use) can never exhibit the bug where folding it into
// volUnlimited's all-must-be-zero check makes "tool volume caps unlimited"
// permanently unreachable for any config that went through Load(). This
// loads a real config with every context cap left unset and asserts the
// unlimited phrase still appears despite MaxEditFileBytes resolving nonzero.
func TestFormatEffectiveLimitsSummaryUnlimitedSurvivesResolution(t *testing.T) {
	res, err := config.Load(config.LoadOptions{ConfigPath: writeMinimalLoadableConfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.MaxEditFileBytes <= 0 {
		t.Fatalf("MaxEditFileBytes = %d, want a resolved positive memory bound", res.Tools.MaxEditFileBytes)
	}
	line := formatEffectiveLimitsSummary(res)
	if !strings.Contains(line, "tool volume caps unlimited") {
		t.Fatalf("expected unlimited phrase despite a resolved MaxEditFileBytes: %q", line)
	}
	if !strings.Contains(line, "max_edit_file_bytes=") {
		t.Fatalf("expected max_edit_file_bytes to still be reported: %q", line)
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
