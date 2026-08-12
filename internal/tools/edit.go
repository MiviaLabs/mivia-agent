package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/diff"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// MultiEditToolName is the registered name of the batched-edit tool.
const MultiEditToolName = "multi_edit"

// multiEditTool applies a sequence of exact-string edits to one file in a
// single call. It exists because the alternative - N search_replace calls -
// costs N round trips, N whole-file reads and N rewrites for what is one
// logical change, and leaves the file half-edited when edit K fails. Here the
// edits are applied to an in-memory copy and the file is written once, so a
// failing edit leaves the file exactly as it was.
type multiEditTool struct {
	ws                   *workspace.Root
	maxFileBytes         int
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
	writePathDenylist    []string
}

func (t *multiEditTool) Capability(args json.RawMessage) Capability {
	// Capability.MaxResultBytes is deliberately NOT declared, for the reason
	// given on searchReplaceTool.Capability.
	return Capability{Class: ExecutionWrite, ResourceKey: pathCapabilityKey(args, t.ws)}
}

// ResultBudgetBytes declares the byte budget the result is clamped to for
// dispatcher output-backstop derivation (see tools.ResultBudgetTool).
func (t *multiEditTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *multiEditTool) Name() string { return MultiEditToolName }

func (t *multiEditTool) Description() string {
	return "Apply several exact string replacements to one file in a single call. " +
		"Params: path, edits (array of {old_string, new_string, optional replace_all}); both required. " +
		"Edits apply in order to the result of the previous one. Each old_string must match uniquely " +
		"unless replace_all is true. All-or-nothing: if any edit fails, the file is left untouched. " +
		"Prefer this over repeated search_replace calls on the same file."
}

func (t *multiEditTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Relative file path",
		},
		"edits": map[string]any{
			"type":        "array",
			"description": "Edits applied in order (at least one)",
			"items": schemaObject(map[string]any{
				"old_string": map[string]any{
					"type":        "string",
					"description": "Exact string to find (must match uniquely unless replace_all=true)",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "Replacement string",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "Replace all occurrences of this edit's old_string (default false)",
				},
			}, []string{"old_string", "new_string"}),
		},
	}, []string{"path", "edits"})
}

type editSpec struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t *multiEditTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path  string     `json:"path"`
		Edits []editSpec `json:"edits"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if len(in.Edits) == 0 {
		return "", fmt.Errorf("edits must contain at least one edit")
	}
	abs, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	rel := t.ws.Rel(abs)
	if writePathDenied(t.ws, in.Path, rel, t.writePathDenylist) {
		return "", fmt.Errorf("writing protected path is blocked")
	}
	if isSecretPath(rel, t.secretPathExceptions, t.secretPathPatterns) {
		return "", fmt.Errorf("editing secret-like path is blocked")
	}
	st, err := requireRegularFile(abs)
	if err != nil {
		return "", err
	}
	if err := guardEditFileSize(rel, st.Size(), t.maxFileBytes); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	// Held across the read and the write below: shares editFileLocks with
	// search_replace (see edit_lock.go) so the two tools mutually exclude on
	// the same file, not just against themselves.
	unlock := lockEditFile(abs)
	defer unlock()
	data, err := readFileWithContext(ctx, abs)
	if err != nil {
		return "", err
	}
	original := string(data)

	next, replacements, err := applyEdits(ctx, original, in.Path, in.Edits)
	if err != nil {
		return "", err
	}
	if next == original {
		return fmt.Sprintf("no change to %s (%d edits matched, replacement text identical)", rel, len(in.Edits)), nil
	}
	if err := rewriteRegularFileContents(abs, next, st.Mode().Perm()); err != nil {
		return "", err
	}
	return formatMultiEditResult(rel, len(in.Edits), replacements, original, next, t.maxBytes), nil
}

// applyEdits folds every edit into content in order, so edit K sees the result
// of edit K-1 exactly as a sequence of search_replace calls would. Errors name
// the 1-based edit index: with several edits in one call, "old_string not
// found" alone does not say which one to fix.
func applyEdits(ctx context.Context, content, path string, edits []editSpec) (string, int, error) {
	replacements := 0
	for i, e := range edits {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		label := fmt.Sprintf("edit %d/%d", i+1, len(edits))
		if e.OldString == "" {
			return "", 0, fmt.Errorf("%s: old_string must not be empty", label)
		}
		if e.OldString == e.NewString {
			return "", 0, fmt.Errorf("%s: old_string and new_string are identical", label)
		}
		// This edit already landed against the running content - see
		// alreadyApplied for why (old_string is frequently a substring of
		// new_string, so skipping this would let a retried or independently
		// re-issued edit duplicate the inserted text).
		if alreadyApplied(content, e.NewString) {
			continue
		}
		count := strings.Count(content, e.OldString)
		if count == 0 {
			// An edit that matched the ORIGINAL file but not the running
			// content is the failure mode unique to this tool: an earlier
			// edit consumed the text. Say so, rather than let the model
			// conclude its old_string was wrong about the file on disk.
			hint := ""
			if i > 0 {
				hint = " (an earlier edit in this call may have already rewritten that text)"
			}
			return "", 0, fmt.Errorf("%s: old_string not found in %s%s", label, path, hint)
		}
		if count > 1 && !e.ReplaceAll {
			return "", 0, fmt.Errorf("%s: old_string found %d times; pass replace_all=true or make old_string unique", label, count)
		}
		if e.ReplaceAll {
			content = strings.ReplaceAll(content, e.OldString, e.NewString)
			replacements += count
		} else {
			content = strings.Replace(content, e.OldString, e.NewString, 1)
			replacements++
		}
	}
	return content, replacements, nil
}

// formatMultiEditResult renders one whole-file diff for the batch rather than
// one per edit: the edits interleave in the file, and a per-edit diff would
// report line numbers from intermediate contents that never hit disk.
func formatMultiEditResult(path string, edits, replacements int, oldContent, newContent string, budget int) string {
	header := fmt.Sprintf("updated %s (%d %s, %d %s", path,
		edits, plural(edits, "edit"), replacements, plural(replacements, "replacement"))
	return formatEditDiffResult(header, path, oldContent, newContent, budget)
}

func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// formatEditDiffResult closes the header with the +/− stats of the whole-file
// diff and appends the diff itself, clamped to budget.
func formatEditDiffResult(header, path, oldContent, newContent string, budget int) string {
	result, err := diff.Compute(oldContent, newContent, diff.Options{MaxInputBytes: 512 << 10, Timeout: 100 * time.Millisecond})
	insertions, deletions := 0, 0
	if err == nil {
		insertions, deletions = diff.Stats(result)
	}
	header = fmt.Sprintf("%s, +%d −%d)", header, insertions, deletions)
	dump := generateUnifiedDiffAt(path, oldContent, newContent, firstChangedLine(oldContent, newContent))
	if err != nil {
		dump = fmt.Sprintf("--- a/%s\n+++ b/%s\n(diff omitted: %v)", path, path, err)
	}
	return clampEditResult(header, dump, budget)
}

// firstChangedLine returns the 1-based line of the first difference between
// old and new. generateUnifiedDiffAt numbers its first hunk from this line, so
// getting it from the contents themselves keeps the hunk headers true no
// matter where in the file the batch's edits landed.
func firstChangedLine(oldContent, newContent string) int {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	i := 0
	for i < len(oldLines) && i < len(newLines) && oldLines[i] == newLines[i] {
		i++
	}
	return i + 1
}
