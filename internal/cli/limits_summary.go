package cli

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
	fmt.Fprintf(&b, "; memory_backstop_mb=%d", tc.MemoryBackstopMB)
	cc := res.Context
	fmt.Fprintf(&b, "; context max_source_event_bytes=%d (chunk size; 0→default)", cc.MaxSourceEventBytes)
	return b.String()
}

// logEffectiveLimitsOnce writes the effective-limits summary and any tools
// config warnings to w (typically stderr) exactly when chat starts.
func logEffectiveLimitsOnce(w io.Writer, res *config.Resolved) {
	if w == nil || res == nil {
		return
	}
	for _, warn := range config.ToolResultBytesWarnings(res.Tools) {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
	if line := formatEffectiveLimitsSummary(res); line != "" {
		fmt.Fprintln(w, line)
	}
}
