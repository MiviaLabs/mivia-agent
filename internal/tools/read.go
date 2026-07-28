package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type readFileTool struct {
	ws       *workspace.Root
	maxBytes int
}

func (t *readFileTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

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
	if isSecretPath(t.ws.Rel(abs)) {
		return "", fmt.Errorf("reading secret-like path is blocked: %s", in.Path)
	}
	st, err := requireRegularFile(abs)
	if err != nil {
		return "", err
	}

	// Full-file path: small files only.
	if in.Offset <= 1 && in.Limit <= 0 {
		if st.Size() > int64(t.maxBytes) {
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
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return "", err
	} else if !st.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file (mode %s); refusing special files that can block", st.Mode().Type())
	}

	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, t.maxBytes)

	var b strings.Builder
	lineNo := 0
	taken := 0
	totalBytes := 0
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		lineNo++
		if lineNo < offset {
			continue
		}
		if limit > 0 && taken >= limit {
			break
		}
		line := sc.Text()
		need := len(line) + 1
		if totalBytes+need > t.maxBytes {
			if b.Len() == 0 {
				return "", fmt.Errorf("line %d exceeds max read size (%d bytes)", lineNo, t.maxBytes)
			}
			fmt.Fprintf(&b, "\n... truncated at max read size (%d bytes)", t.maxBytes)
			break
		}
		if taken > 0 {
			b.WriteByte('\n')
			totalBytes++
		}
		b.WriteString(line)
		totalBytes += len(line)
		taken++
	}
	if err := sc.Err(); err != nil {
		if err == bufio.ErrTooLong {
			return "", fmt.Errorf("line exceeds max read size (%d bytes)", t.maxBytes)
		}
		return "", err
	}
	if lineNo == 0 {
		return "", nil
	}
	if offset > lineNo {
		return "", fmt.Errorf("offset %d past end of file (%d lines)", offset, lineNo)
	}
	if b.Len() == 0 {
		return "", nil
	}
	out := b.String()
	if !utf8.ValidString(out) {
		return "", fmt.Errorf("file is not valid UTF-8")
	}
	header := fmt.Sprintf("… lines %d–%d", offset, offset+taken-1)
	return header + "\n" + out, nil
}

type listDirTool struct {
	ws         *workspace.Root
	maxEntries int
}

func (t *listDirTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

func (t *listDirTool) Name() string { return "list_dir" }
func (t *listDirTool) Description() string {
	return "List files and subdirectories in a workspace folder by relative path (default \".\"). " +
		"Params: optional path. Prefer this over run_command for listing."
}
func (t *listDirTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path": map[string]any{"type": "string", "description": "Relative directory path (default \".\")"},
	}, nil)
}

func (t *listDirTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Path == "" {
		in.Path = "."
	}
	abs, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if isSecretPath(t.ws.Rel(abs)) {
		return "", fmt.Errorf("listing secret-like path is blocked: %s", in.Path)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, e := range entries {
		if i >= t.maxEntries {
			fmt.Fprintf(&b, "... truncated (%d more)\n", len(entries)-t.maxEntries)
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		fmt.Fprintln(&b, name)
	}
	if b.Len() == 0 {
		return "(empty)", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
