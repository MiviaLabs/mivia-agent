package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/diff"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type writeFileTool struct {
	ws                   *workspace.Root
	maxWriteKB           int
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
	writePathDenylist    []string
}

func (t *writeFileTool) Capability(args json.RawMessage) Capability {
	// Capability.MaxResultBytes is deliberately NOT declared: the agent loop
	// would tail-cut the diff-truncation notice this tool appends. The budget
	// reaches the dispatcher backstop via ResultBudgetBytes.
	return Capability{Class: ExecutionWrite, ResourceKey: pathCapabilityKey(args, t.ws)}
}

// ResultBudgetBytes declares the configured byte budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool). An overwrite
// result carries a unified diff of the previous contents against the new
// ones, so it is bounded by file size, not by the request.
func (t *writeFileTool) ResultBudgetBytes() int { return t.maxBytes }

// writeDiffTruncNotice closes a write result whose diff was cut to fit the
// byte budget. The file was still written in full; only the reported diff is
// partial, and the notice says so.
const writeDiffTruncNotice = "\n... diff truncated at %d bytes"

// capWriteResult trims a write result to the tool's byte budget, paying for
// the truncation notice out of the budget so the whole result fits.
func (t *writeFileTool) capWriteResult(out string) string {
	if t.maxBytes <= 0 || len(out) <= t.maxBytes {
		return out
	}
	notice := fmt.Sprintf(writeDiffTruncNotice, t.maxBytes)
	if len(notice) >= t.maxBytes {
		return notice
	}
	return diff.TruncateUTF8(out, t.maxBytes-len(notice)) + notice
}

func (t *writeFileTool) Name() string { return "write_file" }
func (t *writeFileTool) Description() string {
	return "Create or overwrite a whole text file. Params: path, content (both required). " +
		"Prefer search_replace (or multi_edit for several edits to one file) for small edits. " +
		"Do not pass encoding or mode fields."
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
	rel := t.ws.Rel(abs)
	if writePathDenied(t.ws, in.Path, rel, t.writePathDenylist) {
		return "", fmt.Errorf("writing protected path is blocked")
	}
	if isSecretPath(rel, t.secretPathExceptions, t.secretPathPatterns) {
		return "", fmt.Errorf("writing secret-like path is blocked")
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
	var oldContent string
	if st, err := os.Stat(abs); err == nil {
		if st.IsDir() {
			return "", fmt.Errorf("path is a directory")
		}
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("path is not a regular file (mode %s); refusing special files that can block", st.Mode().Type())
		}
		existed = true
		// Stream-count lines for stats only - never load whole file into memory.
		// Cap scan so a multi-GB target cannot OOM the agent on a small rewrite.
		oldLines = countFileLinesCapped(abs, 8<<20) // 8 MiB scan budget
		if st.Size() <= overwriteDiffMaxBytes && int64(len(in.Content)) <= overwriteDiffMaxBytes {
			if data, readErr := readFileWithContext(ctx, abs); readErr == nil {
				oldContent = string(data)
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	if err := writeRegularFileContents(abs, in.Content); err != nil {
		return "", err
	}
	newLines := countLines(in.Content)
	if !existed {
		return fmt.Sprintf("wrote %s (%d bytes, create +%d)", rel, len(in.Content), newLines), nil
	}
	header := fmt.Sprintf("wrote %s (%d bytes, overwrite %d→%d lines)", rel, len(in.Content), oldLines, newLines)
	if oldContent == "" && oldLines > 0 {
		return header + "\n(diff omitted: file exceeds diff size budget)", nil
	}
	if oldContent == "" {
		return header, nil
	}
	return t.capWriteResult(header + "\n" + generateUnifiedDiffAt(rel, oldContent, in.Content, 1)), nil
}

// writeRegularFileContents writes content via non-blocking open + fstat so a
// FIFO planted after Stat cannot block the tool worker (TOCTOU). New files get
// the default 0644; an existing file keeps the mode it already has, because
// O_TRUNC does not re-apply perm.
func writeRegularFileContents(abs, content string) error {
	return rewriteRegularFileContents(abs, content, 0o644)
}

// rewriteRegularFileContents replaces an existing file's contents while
// preserving its mode. perm is the mode observed before the write; it takes
// effect only in the narrow window where the file was removed between the
// stat and this open and O_CREATE recreates it. In the ordinary O_TRUNC path
// the kernel keeps the file's existing mode, which is what makes an edited
// executable stay executable - pinned by TestSearchReplacePreservesFileMode.
func rewriteRegularFileContents(abs, content string, perm os.FileMode) error {
	wf, _, err := openRegularFileWrite(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, werr := wf.Write([]byte(content))
	cerr := wf.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

type searchReplaceTool struct {
	ws                   *workspace.Root
	maxFileBytes         int
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
	writePathDenylist    []string
}

func (t *searchReplaceTool) Capability(args json.RawMessage) Capability {
	// Capability.MaxResultBytes is deliberately NOT declared: the agent loop
	// treats it as a wire truncation bound and would tail-cut the "…"
	// truncation marker this tool pays for out of its own budget. The budget
	// reaches the dispatcher backstop via ResultBudgetBytes instead.
	return Capability{Class: ExecutionWrite, ResourceKey: pathCapabilityKey(args, t.ws)}
}

// ResultBudgetBytes declares the byte budget the result is clamped to for
// dispatcher output-backstop derivation (see tools.ResultBudgetTool). The
// result is a header plus a unified diff, both cut to this bound by
// formatSearchReplaceResultAt.
func (t *searchReplaceTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *searchReplaceTool) Name() string { return "search_replace" }
func (t *searchReplaceTool) Description() string {
	return "Edit a file by exact string replace. Params: path, old_string, new_string (required); optional replace_all (bool). " +
		"old_string must match uniquely unless replace_all is true. Prefer over full-file rewrite. " +
		"For several edits to the same file, prefer multi_edit."
}
func (t *searchReplaceTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Relative file path",
		},
		"old_string": map[string]any{
			"type":        "string",
			"description": "Exact string to find (must match uniquely unless replace_all=true)",
		},
		"new_string": map[string]any{
			"type":        "string",
			"description": "Replacement string",
		},
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
	if err := guardEditFileSize(t.ws.Rel(abs), st.Size(), t.maxFileBytes); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	data, err := readFileWithContext(ctx, abs)
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
	if err := rewriteRegularFileContents(abs, next, st.Mode().Perm()); err != nil {
		return "", err
	}
	n := 1
	if in.ReplaceAll {
		n = count
	}
	matchAt := strings.Index(content, in.OldString)
	oldLine := strings.Count(content[:matchAt], "\n") + 1
	return formatSearchReplaceResultAt(t.ws.Rel(abs), n, in.OldString, in.NewString, content, next, oldLine, t.maxBytes), nil
}

// guardEditFileSize refuses an in-place edit whose whole-file read would blow
// the effective read bound. Without it search_replace was the one workspace
// tool that loaded a file of any size into memory with no guard at all: under
// the uncapped default the only thing standing between the agent and an OOM
// was the size of the file it happened to name. The message states the real
// size and names the two tools that can still make progress, so the model has
// somewhere to go instead of retrying the same call.
func guardEditFileSize(rel string, size int64, maxBytes int) error {
	if maxBytes <= 0 || size <= int64(maxBytes) {
		return nil
	}
	return fmt.Errorf("file too large to edit in place (%s is %d bytes; max %d). "+
		"Read a window with read_file offset+limit, then replace the file with write_file", rel, size, maxBytes)
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
	f, _, err := openRegularFile(path)
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
const overwriteDiffMaxBytes = 512 << 10

// formatSearchReplaceResultAt renders an edit result inside budget bytes. The
// budget is a hard bound on the whole return value - header, diff, and the "…"
// marker that reports the cut - so the declared ResultBudgetBytes is honest
// rather than an estimate of a typical diff.
func formatSearchReplaceResultAt(path string, n int, oldStr, newStr, fullOld, fullNew string, oldLine, budget int) string {
	result, err := diff.Compute(oldStr, newStr, diff.Options{MaxInputBytes: 512 << 10, Timeout: 100 * time.Millisecond})
	noun := "replacement"
	if n != 1 {
		noun = "replacements"
	}
	insertions, deletions := 0, 0
	if err == nil {
		insertions, deletions = diff.Stats(result)
	}
	header := fmt.Sprintf("updated %s (%d %s, +%d −%d)", path, n, noun, insertions, deletions)
	dump := generateUnifiedDiffAt(path, fullOld, fullNew, oldLine)
	if err != nil {
		dump = fmt.Sprintf("--- a/%s\n+++ b/%s\n(diff omitted: %v)", path, path, err)
	}
	return clampEditResult(header, dump, budget)
}

// clampEditResult joins an edit result's header and diff and cuts the whole
// thing to budget, paying for the elision marker out of the budget so the
// declared ResultBudgetBytes bounds the ENTIRE return value. The header is
// preserved where it fits: a truncated diff with intact "+N −M" stats still
// tells the model what happened, while a cut header tells it nothing.
func clampEditResult(header, dump string, budget int) string {
	if budget <= 0 {
		budget = searchReplaceResultMaxBytes
	}
	out := header + "\n" + dump
	if len(out) <= budget {
		return out
	}
	// Keeping the header costs its own bytes plus the newline and the marker;
	// only take that branch when all three actually fit.
	if len(header)+1+len("…") <= budget {
		bodyBudget := budget - len(header) - 1 - len("…")
		// TruncateUTF8 treats a non-positive bound as "no bound" and returns
		// the input whole, so a budget that leaves no room for the diff must
		// drop it here rather than hand the string to the truncator.
		if bodyBudget <= 0 {
			dump = ""
		} else if len(dump) > bodyBudget {
			dump = diff.TruncateUTF8(dump, bodyBudget)
		}
		return header + "\n" + dump + "…"
	}
	if budget > len("...") {
		return diff.TruncateUTF8(out, budget-3) + "..."
	}
	// Budget too small to carry even the elision marker; cut hard rather than
	// return a result that overruns the declaration.
	return diff.TruncateUTF8(out, budget)
}

func generateUnifiedDiffAt(path, oldStr, newStr string, oldLine int) string {
	result, err := diff.Compute(oldStr, newStr, diff.Options{MaxInputBytes: 512 << 10, Timeout: 100 * time.Millisecond})
	if err != nil {
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n(diff omitted: %v)", path, path, err)
	}
	return diff.FormatUnifiedAt(path, result, oldLine, oldLine, 3)
}
