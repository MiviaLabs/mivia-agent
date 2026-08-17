package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	return "Create or overwrite a whole text file. " +
		"For small edits prefer search_replace (multi_edit for several edits to one file)."
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

	// Snapshot the pre-write state used for the result message and diff.
	existed, oldLines, oldContent, err := snapshotExistingFile(ctx, abs, in.Content)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	// Serialize against the in-place edit tools (search_replace, multi_edit)
	// and delete_file on the same path: write_file used to skip editFileLocks,
	// so a concurrent read-modify-write span could interleave inside this
	// guard+write and one mutation would be silently dropped while both tools
	// reported success (see edit_lock.go).
	unlock := lockEditFile(abs)
	defer unlock()
	// Refuse an overwrite of a file that changed on disk since the agent last
	// saw it; first writes (never observed) proceed and record.
	if err := guardStaleWrite(abs); err != nil {
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
	refreshFileObservation(abs)
	return t.formatWriteResult(rel, in.Content, existed, oldLines, oldContent), nil
}

// snapshotExistingFile captures the pre-write state of abs for the result
// message and overwrite diff; a missing path is a legitimate create, not an
// error, and the stat/diff read precede the lock because the diff is advisory.
func snapshotExistingFile(ctx context.Context, abs, newContent string) (existed bool, oldLines int, oldContent string, err error) {
	st, err := os.Stat(abs)
	if err != nil {
		return false, 0, "", nil
	}
	if st.IsDir() {
		return false, 0, "", fmt.Errorf("path is a directory")
	}
	if !st.Mode().IsRegular() {
		return false, 0, "", fmt.Errorf("path is not a regular file (mode %s); refusing special files that can block", st.Mode().Type())
	}
	// Stream-count lines for stats only - never load whole file into memory.
	// Cap scan so a multi-GB target cannot OOM the agent on a small rewrite.
	oldLines = countFileLinesCapped(abs, 8<<20) // 8 MiB scan budget
	if st.Size() <= overwriteDiffMaxBytes && int64(len(newContent)) <= overwriteDiffMaxBytes {
		if data, readErr := readFileWithContext(ctx, abs); readErr == nil {
			oldContent = string(data)
		}
	}
	return true, oldLines, oldContent, nil
}

// formatWriteResult renders the write_file confirmation: a create summary, or
// an overwrite summary with the capped unified diff of the previous contents.
func (t *writeFileTool) formatWriteResult(rel, content string, existed bool, oldLines int, oldContent string) string {
	newLines := countLines(content)
	if !existed {
		return fmt.Sprintf("wrote %s (%d bytes, create +%d)", rel, len(content), newLines)
	}
	header := fmt.Sprintf("wrote %s (%d bytes, overwrite %d→%d lines)", rel, len(content), oldLines, newLines)
	if oldContent == "" && oldLines > 0 {
		return header + "\n(diff omitted: file exceeds diff size budget)"
	}
	if oldContent == "" {
		return header
	}
	return t.capWriteResult(header + "\n" + generateUnifiedDiffAt(rel, oldContent, content, 1))
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
	return "Edit a file by exact string replace. " +
		"Prefer over full-file rewrite; for several edits to one file prefer multi_edit."
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
	// Held across the read and the write below: without it, two concurrent
	// search_replace calls to the same file can both read the same original
	// content and race to write, silently dropping whichever wrote first
	// (see edit_lock.go).
	unlock := lockEditFile(abs)
	defer unlock()
	// Same stale-write guard as multi_edit: the file must not have changed on
	// disk since the agent last read or wrote it (editor, second session, hook).
	if err := guardStaleWrite(abs); err != nil {
		return "", err
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
	if alreadyApplied(content, in.OldString, in.NewString) {
		return fmt.Sprintf("no change to %s (edit already applied: new_string already present)", rel), nil
	}
	next, n, err := computeSearchReplace(content, in.OldString, in.NewString, in.Path, in.ReplaceAll)
	if err != nil {
		return "", err
	}
	if err := rewriteRegularFileContents(abs, next, st.Mode().Perm()); err != nil {
		return "", err
	}
	refreshFileObservation(abs)
	matchAt := strings.Index(content, in.OldString)
	oldLine := strings.Count(content[:matchAt], "\n") + 1
	return formatSearchReplaceResultAt(t.ws.Rel(abs), n, in.OldString, in.NewString, content, next, oldLine, t.maxBytes), nil
}

// alreadyApplied reports whether this exact edit has already landed in
// content: new_string is present, and old_string no longer occurs anywhere
// outside the landed new_string occurrences. It is true for a retried or
// re-issued call, or a second agent applying the same fix independently.
// old_string is frequently a substring of new_string (an anchor line the
// edit extends), so without the strip old_string would still match post-edit
// and a reapply would silently duplicate the inserted text instead of
// no-op'ing. The strip also keeps the check honest the other way: a file that
// merely CONTAINS new_string elsewhere while old_string still needs replacing
// is a live edit, not a reapply, and must not be skipped. One degenerate case
// needs its own guard: when the file is (up to surrounding whitespace)
// entirely new_string text, the strip removes old_string along with it and
// the edit would be skipped even though it never landed - content "foobar"
// with old "foo" -> "foobar" must still become "foobarbar". That content is a
// pre-existing occurrence of the target text, not evidence the edit landed,
// so it is treated as a live edit. Always false for new_string == "" (a
// deletion edit), where "already applied" can't be distinguished from "there
// was never anything to delete".
func alreadyApplied(content, oldString, newString string) bool {
	if newString == "" {
		return false
	}
	if !strings.Contains(content, newString) {
		return false
	}
	stripped := strings.ReplaceAll(content, newString, "")
	// Content that is (up to whitespace) entirely new_string occurrences is a
	// pre-existing target text, not a landed edit: old_string inside it would
	// be stripped along with new_string, falsely reporting "already applied"
	// (content "foobar" with old "foo" -> "foobar"). Treat it as a live edit.
	if strings.TrimSpace(stripped) == "" {
		return false
	}
	// Strip the landed new_string occurrences and skip only when no old_string
	// survives outside them.
	return !strings.Contains(stripped, oldString)
}

// computeSearchReplace validates old_string's match count against
// replaceAll and returns the replaced content plus the number of
// replacements made.
func computeSearchReplace(content, oldString, newString, path string, replaceAll bool) (string, int, error) {
	count := strings.Count(content, oldString)
	if count == 0 {
		return "", 0, fmt.Errorf("old_string not found in %s", path)
	}
	if count > 1 && !replaceAll {
		return "", 0, fmt.Errorf("old_string found %d times; pass replace_all=true or make old_string unique", count)
	}
	if replaceAll {
		return strings.ReplaceAll(content, oldString, newString), count, nil
	}
	return strings.Replace(content, oldString, newString, 1), 1, nil
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
