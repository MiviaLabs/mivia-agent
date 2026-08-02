package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type readFileTool struct {
	ws                   *workspace.Root
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
}

func (t *readFileTool) Capability(args json.RawMessage) Capability {
	// Capability.MaxResultBytes is deliberately NOT declared: the agent loop
	// treats it as a wire truncation bound, and the window path's honest
	// framing (header + truncation notice) rides above maxBytes. The content
	// budget feeds the dispatcher backstop via ResultBudgetBytes instead.
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

// ResultBudgetBytes declares the configured content budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool).
func (t *readFileTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *readFileTool) Name() string { return "read_file" }
func (t *readFileTool) Description() string {
	return "Read a text file by relative workspace path. " +
		"Params: path (required), optional offset (1-based start line), optional limit (max lines). " +
		"Use offset+limit for large files or excerpts. Do not pass file content, encoding, or other agent-style fields. " +
		"Prefer this over run_command for reading files."
}
func (t *readFileTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Relative path to the file (required)",
		},
		"offset": map[string]any{
			"type":        "integer",
			"description": "1-based line number to start reading (default 1). Use with limit for large files.",
		},
		"limit": map[string]any{
			"type":        "integer",
			"description": "Maximum number of lines to return (default: all, capped by max read size).",
		},
	}, []string{"path"})
}

func (t *readFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	in.Path = strings.TrimSpace(in.Path)
	if in.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if isSecretPath(t.ws.Rel(abs), t.secretPathExceptions, t.secretPathPatterns) {
		return "", fmt.Errorf("reading secret-like path is blocked")
	}
	st, err := requireRegularFile(abs)
	if err != nil {
		return "", err
	}

	// Full-file path: small files only.
	if in.Offset <= 1 && in.Limit <= 0 {
		if t.maxBytes > 0 && st.Size() > int64(t.maxBytes) {
			return "", fmt.Errorf("file too large (%d bytes; max %d). Re-call with offset and limit to read a line window", st.Size(), t.maxBytes)
		}
		data, err := readFileWithContext(ctx, abs)
		if err != nil {
			return "", err
		}
		if !utf8.Valid(data) {
			return "", fmt.Errorf("file is not valid UTF-8")
		}
		return string(data), nil
	}

	return t.readLineWindow(ctx, abs, in.Offset, in.Limit)
}

func (t *readFileTool) readLineWindow(ctx context.Context, abs string, offset, limit int) (string, error) {
	if offset < 1 {
		offset = 1
	}
	// Non-blocking open + fstat closes the TOCTOU window where a path becomes
	// a FIFO between Stat and Open.
	f, _, err := openRegularFile(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	// When maxBytes is 0 (uncapped), the scanner max token size must still be
	// large enough to handle long lines: Go's Scanner.Buffer sets
	// maxTokenSize = max(max, cap(buf)), so max=0 falls back to 64 KiB,
	// which is a regression. Use 1 MiB as the floor, matching grep's
	// hardcoded scanner max.
	scannerMax := t.maxBytes
	if scannerMax <= 0 {
		scannerMax = 1 << 20 // 1 MiB
	}
	sc.Buffer(buf, scannerMax)

	// First pass: count total lines and collect the requested window.
	// The scanner must iterate to offset anyway, so counting is free.
	// When limit==0, all lines from offset onward are collected.
	var windowLines []string
	lineNo := 0
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		lineNo++
		if lineNo >= offset && (limit <= 0 || len(windowLines) < limit) {
			windowLines = append(windowLines, sc.Text())
		}
	}
	totalLines := lineNo
	if err := sc.Err(); err != nil {
		if err == bufio.ErrTooLong {
			return "", fmt.Errorf("line exceeds max read size (%d bytes)", t.maxBytes)
		}
		return "", err
	}
	if totalLines == 0 {
		return "", nil
	}
	if offset > totalLines {
		return "", fmt.Errorf("offset %d past end of file (%d lines)", offset, totalLines)
	}
	if len(windowLines) == 0 {
		return "", nil
	}

	return t.formatWindow(windowLines, offset, totalLines)
}

// formatWindow renders collected window lines with right-aligned line
// number prefixes (e.g. " 42 | content") and a header reporting the
// delivered range and total line count ("… lines X–Y of Z"). Prefix
// bytes are counted against maxBytes so truncation stays honest.
func (t *readFileTool) formatWindow(lines []string, offset, totalLines int) (string, error) {
	width := len(fmt.Sprintf("%d", totalLines))
	if width < 1 {
		width = 1
	}

	var b strings.Builder
	totalBytes := 0
	formatted := 0
	for i, line := range lines {
		num := offset + i
		prefix := fmt.Sprintf("%*d | ", width, num)
		need := len(prefix) + len(line) + 1
		if t.maxBytes > 0 && totalBytes+need > t.maxBytes {
			if b.Len() == 0 {
				return "", fmt.Errorf("line %d exceeds max read size (%d bytes)", num, t.maxBytes)
			}
			fmt.Fprintf(&b, "\n... truncated at max read size (%d bytes)", t.maxBytes)
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
			totalBytes++
		}
		b.WriteString(prefix)
		b.WriteString(line)
		totalBytes += len(prefix) + len(line)
		formatted++
	}

	out := b.String()
	if !utf8.ValidString(out) {
		return "", fmt.Errorf("file is not valid UTF-8")
	}
	header := fmt.Sprintf("… lines %d–%d of %d", offset, offset+formatted-1, totalLines)
	return header + "\n" + out, nil
}

// list_dir implementation lives in list_dir.go.
