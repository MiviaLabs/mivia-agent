package render

import "strings"

// ReadOnlyToolClass reports the read-only lookup class of a tool name:
// "read" for file reads, "search" for grep/find/glob/symbol lookups.
// The transcript coalesces consecutive same-class calls into one leader
// row (transcript-polish.md R2). "" means the tool is not read-only.
func ReadOnlyToolClass(name string) string {
	lower := strings.ToLower(name)
	switch {
	case isReadTool(lower), isListDirTool(lower):
		return "read"
	case isSearchTool(lower):
		return "search"
	}
	return ""
}
