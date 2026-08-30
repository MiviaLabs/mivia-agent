package uiadapter

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// parseToolDiff extracts a structured uievent.Diff from file tool inputs and outputs.
func parseToolDiff(name, input, output string) *uievent.Diff {
	lower := strings.ToLower(name)
	isFileTool := strings.Contains(lower, "write") ||
		strings.Contains(lower, "edit") ||
		strings.Contains(lower, "replace") ||
		strings.Contains(lower, "patch") ||
		strings.Contains(lower, "delete") ||
		strings.Contains(lower, "create") ||
		strings.Contains(lower, "append")
	if !isFileTool {
		return nil
	}

	args := parseArgs(input)
	var path string
	for _, key := range []string{"path", "TargetFile", "target_file", "file_path", "filePath", "filename"} {
		if v, ok := args[key].(string); ok && v != "" {
			path = v
			break
		}
	}

	isDelete := strings.Contains(lower, "delete")
	if isDelete {
		if path == "" {
			path = extractPathFromOutput(output)
		}
		if path != "" {
			return &uievent.Diff{Path: path, Deleted: true}
		}
		return nil
	}

	hunks, added, removed, diffPath := parseDiffHunks(output)
	if path == "" {
		path = diffPath
	}
	if path == "" {
		path = extractPathFromOutput(output)
	}
	if path == "" {
		return nil
	}

	return &uievent.Diff{
		Path:    path,
		Added:   added,
		Removed: removed,
		Hunks:   hunks,
	}
}

// parseDiffHunks parses unified diff headers and lines from tool output.
func parseDiffHunks(output string) (hunks []uievent.DiffHunk, added, removed int, path string) {
	lines := strings.Split(output, "\n")
	var currentHunk *uievent.DiffHunk

	for i, line := range lines {
		if strings.HasPrefix(line, "--- a/") || strings.HasPrefix(line, "--- ") {
			p := strings.TrimPrefix(line, "--- a/")
			p = strings.TrimPrefix(p, "--- ")
			p = strings.TrimSpace(p)
			if path == "" && p != "" && p != "/dev/null" {
				path = p
			}
			continue
		}
		if strings.HasPrefix(line, "+++ b/") || strings.HasPrefix(line, "+++ ") {
			p := strings.TrimPrefix(line, "+++ b/")
			p = strings.TrimPrefix(p, "+++ ")
			p = strings.TrimSpace(p)
			if path == "" && p != "" && p != "/dev/null" {
				path = p
			}
			continue
		}
		if line == "" && (i+1 == len(lines) || strings.HasPrefix(lines[i+1], "@@")) {
			// An empty line just before a hunk header is a separator, and
			// the empty final split element of an output that ends with a
			// newline is the terminator. Neither is content; both rendered
			// as an empty row in the TUI. A blank context row inside a hunk
			// (a tool that trims trailing whitespace emits it without its
			// leading space) has content lines after it and stays.
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if currentHunk != nil {
				hunks = append(hunks, *currentHunk)
			}
			currentHunk = &uievent.DiffHunk{Header: line}
			continue
		}
		if currentHunk != nil {
			if strings.HasPrefix(line, "+") {
				added++
				currentHunk.Lines = append(currentHunk.Lines, uievent.DiffLine{
					Kind: uievent.DiffLineAdd,
					Text: strings.TrimPrefix(line, "+"),
				})
			} else if strings.HasPrefix(line, "-") {
				removed++
				currentHunk.Lines = append(currentHunk.Lines, uievent.DiffLine{
					Kind: uievent.DiffLineDel,
					Text: strings.TrimPrefix(line, "-"),
				})
			} else if strings.HasPrefix(line, " ") {
				currentHunk.Lines = append(currentHunk.Lines, uievent.DiffLine{
					Kind: uievent.DiffLineContext,
					Text: strings.TrimPrefix(line, " "),
				})
			} else if line == "" {
				currentHunk.Lines = append(currentHunk.Lines, uievent.DiffLine{
					Kind: uievent.DiffLineContext,
					Text: "",
				})
			}
		}
	}
	if currentHunk != nil {
		hunks = append(hunks, *currentHunk)
	}
	return hunks, added, removed, path
}

// extractPathFromOutput attempts to extract a file path from summary outputs like
// "wrote foo/bar.go (100 bytes)" or "updated foo/bar.go (1 replacement)".
func extractPathFromOutput(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return ""
	}
	first := strings.TrimSpace(lines[0])
	words := strings.Fields(first)
	if len(words) >= 2 {
		verb := strings.ToLower(words[0])
		if verb == "wrote" || verb == "updated" || verb == "created" || verb == "deleted" || verb == "saved" {
			candidate := words[1]
			candidate = strings.Trim(candidate, " :;()[]'\"`")
			return candidate
		}
	}
	return ""
}
