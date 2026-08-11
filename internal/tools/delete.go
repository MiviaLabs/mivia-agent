package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// deleteFileTool removes one regular file from the workspace. It is the
// counterpart of write_file: an agent that created a file must be able to
// remove it again. Without this primitive a leftover file (for example a
// renamed test scaffold) is permanent, and a reviewer finding that requires
// its removal is unfixable by the agent, which loops the repair gate forever
// (DC-9).
//
// Like the edit tools, it honors the write-path blocklist: a protected path
// is refused, so deletion cannot bypass the protection that guards writing.
type deleteFileTool struct {
	ws                *workspace.Root
	writePathDenylist []string
}

func (t *deleteFileTool) Name() string { return "delete_file" }

func (t *deleteFileTool) Description() string {
	return "Delete one regular file. Params: path (required). " +
		"Refuses directories, symlinks, special files, and paths outside the workspace. " +
		"Prefer over run_command for file removal."
}

func (t *deleteFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Workspace-relative path of the file to delete.",
			},
		},
		"required": []string{"path"},
	}
}

func (t *deleteFileTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionWrite, ResourceKey: pathCapabilityKey(args, t.ws)}
}

// ResultBudgetBytes bounds the confirmation output: a fixed short line
// ("deleted <rel>"), at most one path under the filesystem path limit.
func (t *deleteFileTool) ResultBudgetBytes() int { return 4096 }

func (t *deleteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
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
	rel := t.ws.Rel(abs)
	if rel == "." {
		return "", fmt.Errorf("refusing to delete the workspace root")
	}
	if writePathDenied(t.ws, in.Path, rel, t.writePathDenylist) {
		return "", fmt.Errorf("deleting protected path is blocked")
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("cannot delete %s: %w", rel, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("path %s is a directory; delete_file removes one file", rel)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("path %s is not a regular file (mode %s); refusing special files", rel, st.Mode().Type())
	}
	if err := os.Remove(abs); err != nil {
		return "", fmt.Errorf("delete %s: %w", rel, err)
	}
	return fmt.Sprintf("deleted %s", rel), nil
}
