package clichat

import (
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// formatEffectiveLimitsSummary returns one operator-facing line describing
// effective tool volume caps, context chunk/limits, and the memory backstop.
// When all tool volume caps are unlimited (0), the line says so explicitly
// while still printing the OOM backstop.
func formatEffectiveLimitsSummary(res *config.Resolved) string {
	if res == nil {
		return ""
	}
	tc := res.Tools
	// MaxEditFileBytes is a MEMORY bound (see its doc comment): resolveToolsConfig
	// always fills it to a positive value (the migrated max_read_bytes or the
	// memory backstop), so it is never legitimately 0 the way a context cap can
	// be. It stays out of volUnlimited - a memory bound that can never read 0
	// would otherwise make "tool volume caps unlimited" permanently
	// unreachable for any config that actually went through Load() - and is
	// printed unconditionally alongside memory_backstop_mb instead.
	volUnlimited := tc.MaxReadBytes == 0 && tc.MaxOutputBytes == 0 &&
		tc.MaxToolResultBytes == 0 && tc.MaxListDirEntries == 0 && tc.MaxWriteKB == 0

	var b strings.Builder
	b.WriteString("limits: ")
	if volUnlimited {
		b.WriteString("tool volume caps unlimited")
	} else {
		fmt.Fprintf(&b, "max_read_bytes=%d max_output_bytes=%d max_tool_result_bytes=%d max_list_dir_entries=%d max_write_kb=%d",
			tc.MaxReadBytes, tc.MaxOutputBytes, tc.MaxToolResultBytes, tc.MaxListDirEntries, tc.MaxWriteKB)
	}
	fmt.Fprintf(&b, "; max_edit_file_bytes=%d; memory_backstop_mb=%d", tc.MaxEditFileBytes, tc.MemoryBackstopMB)
	cc := res.Context
	fmt.Fprintf(&b, "; context max_source_event_bytes=%d (chunk size; 0→default)", cc.MaxSourceEventBytes)
	return b.String()
}

// logEffectiveLimitsOnce writes the effective-limits summary and any tools
// config warnings to w (typically stderr) exactly when chat starts. quiet
// (--quiet) suppresses only the informational limits summary line; warnings
// that signal a misconfiguration still print.
func logEffectiveLimitsOnce(w io.Writer, res *config.Resolved, quiet bool) {
	if w == nil || res == nil {
		return
	}
	for _, warn := range config.ToolResultBytesWarnings(res.Tools) {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
	logMCPWarnings(w, res)
	if line := formatEffectiveLimitsSummary(res); line != "" && !quiet {
		fmt.Fprintln(w, line)
	}
}

func logMCPWarnings(w io.Writer, res *config.Resolved) {
	if w == nil || res == nil {
		return
	}
	for _, warn := range res.MCPWarnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
}
