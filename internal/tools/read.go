package tools

import (
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
	return "Read a text file in the workspace by relative path. Prefer this over run_command for reading files."
}
func (t *readFileTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path": map[string]any{"type": "string", "description": "Relative path to the file"},
	}, []string{"path"})
}

func (t *readFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	abs, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if isSecretPath(t.ws.Rel(abs)) {
		return "", fmt.Errorf("reading secret-like path is blocked: %s", in.Path)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("path is a directory; use list_dir")
	}
	if st.Size() > int64(t.maxBytes) {
		return "", fmt.Errorf("file too large (%d bytes; max %d)", st.Size(), t.maxBytes)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file is not valid UTF-8")
	}
	return string(data), nil
}

type listDirTool struct {
	ws *workspace.Root
}

func (t *listDirTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

func (t *listDirTool) Name() string { return "list_dir" }
func (t *listDirTool) Description() string {
	return "List files and subdirectories in a workspace folder. Prefer this over run_command for listing."
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
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	const maxEntries = 500
	for i, e := range entries {
		if i >= maxEntries {
			fmt.Fprintf(&b, "... truncated (%d more)\n", len(entries)-maxEntries)
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
