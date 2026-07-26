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
	ws *workspace.Root
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
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
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
	return fmt.Sprintf("wrote %s (%d bytes)", t.ws.Rel(abs), len(in.Content)), nil
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
	return fmt.Sprintf("updated %s (%d replacement(s))", t.ws.Rel(abs), n), nil
}
