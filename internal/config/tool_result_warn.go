package config

import "fmt"

// ToolResultBytesWarnings returns non-fatal operator warnings for the tools
// result-cap surface. Values are never clamped here — only advised.
func ToolResultBytesWarnings(tc ToolsConfig) []string {
	if tc.MaxToolResultBytes > UsefulToolResultRequestBytes {
		return []string{fmt.Sprintf(
			"[tools] max_tool_result_bytes=%d exceeds a useful single-provider-request size (%d bytes); not clamped — large history entries may be costly",
			tc.MaxToolResultBytes, UsefulToolResultRequestBytes,
		)}
	}
	return nil
}
