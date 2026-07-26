package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type writeFileTool struct {
	ws         *workspace.Root
	maxWriteKB int
}

func (t *writeFileTool) Name() string { return "write_file" }
func (t *writeFileTool) Description() string {
	return "Create or overwrite a whole text file in the workspace. Prefer search_replace for small edits."
}
func (t *writeFileTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path":    map[string]any{"type": "string", "description": "Relative path to write"},
		"content": map[string]any{"type": "string", "description": "Full file contents"},
	}, []string{"path", "content"})
}

func (t *writeFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	abs, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if isSecretPath(t.ws.Rel(abs)) {
		return "", fmt.Errorf("writing secret-like path is blocked: %s", in.Path)
	}
	// Enforce max write size at runtime to prevent agent from writing oversized files.
	if t.maxWriteKB > 0 && len(in.Content) > t.maxWriteKB*1024 {
		return "", fmt.Errorf("write_file content too large (%d bytes, max %d KiB)", len(in.Content), t.maxWriteKB)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	existed := false
	oldLines := 0
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		existed = true
		// Stream-count lines for stats only — never load whole file into memory.
		// Cap scan so a multi-GB target cannot OOM the agent on a small rewrite.
		oldLines = countFileLinesCapped(abs, 8<<20) // 8 MiB scan budget
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return "", err
	}
	rel := t.ws.Rel(abs)
	newLines := countLines(in.Content)
	if !existed {
		return fmt.Sprintf("wrote %s (%d bytes, create +%d)", rel, len(in.Content), newLines), nil
	}
	return fmt.Sprintf("wrote %s (%d bytes, overwrite %d→%d lines)", rel, len(in.Content), oldLines, newLines), nil
}

type searchReplaceTool struct {
	ws *workspace.Root
}

func (t *searchReplaceTool) Name() string { return "search_replace" }
func (t *searchReplaceTool) Description() string {
	return "Edit a file by replacing an exact string (unique match unless replace_all is true). Prefer over full-file rewrite."
}
func (t *searchReplaceTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path":        map[string]any{"type": "string"},
		"old_string":  map[string]any{"type": "string"},
		"new_string":  map[string]any{"type": "string"},
		"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences (default false)"},
	}, []string{"path", "old_string", "new_string"})
}

func (t *searchReplaceTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.OldString == "" {
		return "", fmt.Errorf("old_string must not be empty")
	}
	abs, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if isSecretPath(t.ws.Rel(abs)) {
		return "", fmt.Errorf("editing secret-like path is blocked: %s", in.Path)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	content := string(data)
	count := strings.Count(content, in.OldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", in.Path)
	}
	if count > 1 && !in.ReplaceAll {
		return "", fmt.Errorf("old_string found %d times; pass replace_all=true or make old_string unique", count)
	}
	var next string
	if in.ReplaceAll {
		next = strings.ReplaceAll(content, in.OldString, in.NewString)
	} else {
		next = strings.Replace(content, in.OldString, in.NewString, 1)
	}
	if err := os.WriteFile(abs, []byte(next), 0o644); err != nil {
		return "", err
	}
	n := 1
	if in.ReplaceAll {
		n = count
	}
	return formatSearchReplaceResult(t.ws.Rel(abs), n, in.OldString, in.NewString), nil
}

// countLines returns the number of lines in s (0 if empty).
// Non-empty strings without a trailing newline count as one line per strings.Count("\n")+1.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// countFileLinesCapped streams a file and counts newlines without loading it all.
// Stops after maxBytes (if >0). Returns lines seen in the scanned prefix.
// If the file is larger than maxBytes, the count is a lower bound for stats only.
func countFileLinesCapped(path string, maxBytes int64) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	const bufSize = 32 * 1024
	buf := make([]byte, bufSize)
	var total int64
	lines := 0
	var last byte
	for {
		if maxBytes > 0 && total >= maxBytes {
			break
		}
		toRead := bufSize
		if maxBytes > 0 {
			remain := maxBytes - total
			if remain < int64(toRead) {
				toRead = int(remain)
			}
		}
		n, err := f.Read(buf[:toRead])
		if n > 0 {
			total += int64(n)
			for i := 0; i < n; i++ {
				if buf[i] == '\n' {
					lines++
				}
			}
			last = buf[n-1]
		}
		if err != nil {
			break
		}
	}
	if total == 0 {
		return 0
	}
	// Match countLines: content without trailing newline still counts final line.
	if last != '\n' {
		lines++
	}
	return lines
}

const searchReplaceResultMaxBytes = 4096

// generateUnifiedDiff produces a GitHub-style unified diff snippet from old/new strings.
// Output:
//
//	--- a/path
//	+++ b/bath
//	@@ -1,N +1,M @@
//	 old line
//	-old line
//	+new line
//
// Each line is prefixed with ' ' (context), '-' (removed), or '+' (added).
func generateUnifiedDiff(path, oldStr, newStr string) string {
	oldLines := splitLines(oldStr)
	newLines := splitLines(newStr)
	var b strings.Builder

	b.WriteString("--- a/" + path + "\n")
	b.WriteString("+++ b/" + path + "\n")
	b.WriteString(fmt.Sprintf("@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines)))

	// Simple line-by-line comparison: match as many lines as possible.
	// Lines that match exactly are context ' ', lines only in old are '-', only in new are '+'.
	maxLen := len(oldLines)
	if len(newLines) > maxLen {
		maxLen = len(newLines)
	}
	for i := 0; i < maxLen; i++ {
		if i < len(oldLines) && i < len(newLines) {
			if oldLines[i] == newLines[i] {
				b.WriteString(" " + oldLines[i] + "\n")
			} else {
				b.WriteString("-" + oldLines[i] + "\n")
				b.WriteString("+" + newLines[i] + "\n")
			}
		} else if i < len(oldLines) {
			b.WriteString("-" + oldLines[i] + "\n")
		} else {
			b.WriteString("+" + newLines[i] + "\n")
		}
	}

	// Truncate trailing empty diff lines.
	result := strings.TrimRight(b.String(), "\n")
	return result
}

// splitLines splits s into lines, trimming trailing newline if present.
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func formatSearchReplaceResult(path string, n int, oldStr, newStr string) string {
	oldLC := countLines(oldStr)
	newLC := countLines(newStr)
	noun := "replacement"
	if n != 1 {
		noun = "replacements"
	}
	header := fmt.Sprintf("updated %s (%d %s, +%d −%d)", path, n, noun, newLC, oldLC)
	dump := generateUnifiedDiff(path, oldStr, newStr)
	out := header + "\n" + dump
	if len(out) > searchReplaceResultMaxBytes {
		if len(header)+1 < searchReplaceResultMaxBytes {
			bodyBudget := searchReplaceResultMaxBytes - len(header) - 1 - len("…")
			if bodyBudget < 0 {
				bodyBudget = 0
			}
			if len(dump) > bodyBudget {
				dump = dump[:bodyBudget]
			}
			out = header + "\n" + dump + "…"
		} else {
			out = out[:searchReplaceResultMaxBytes] + "…"
		}
	}
	return out
}
