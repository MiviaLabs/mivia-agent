package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

const (
	listDirMaxDepth = 16
	// listDirCountWidth is the worst-case digit width reserved for notice counts
	// (20-digit integer) so co-occurring notices always fit the pre-reserved block.
	listDirCountWidth = 20
)

// listDirByteNotice closes a flat (depth-1) listing cut short by the byte budget.
const listDirByteNotice = "... truncated at %d bytes (%d more)\n"

type listDirTool struct {
	ws                   *workspace.Root
	maxEntries           int
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
	// ignore is the shared ignore decision. Nil is safe (empty snapshot).
	ignore *gitignoreMatcher
}

func (t *listDirTool) Capability(args json.RawMessage) Capability {
	// Capability.MaxResultBytes is deliberately NOT declared: the agent loop
	// treats it as a wire truncation bound and would tail-cut the truncation
	// notice this tool appends to stay honest. The byte budget reaches the
	// dispatcher backstop via ResultBudgetBytes instead.
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

// ResultBudgetBytes declares the configured byte budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool). The entry-count cap
// alone cannot bound the result: a single name may be 255 bytes, so
// max_list_dir_entries entries can be two orders of magnitude past any fixed
// ceiling. The byte budget is the bound the backstop is derived from.
func (t *listDirTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *listDirTool) Name() string { return "list_dir" }
func (t *listDirTool) Description() string {
	return "List files and subdirectories in a workspace folder by relative path (default \".\"). " +
		"Params: optional path; optional depth (default 1, max 16) for a recursive tree; " +
		"optional include_size (boolean; when omitted defaults to true if depth > 1, false if depth is 1). " +
		"Recursive mode emits an indented tree with file sizes and collapses ignored/secret directories. " +
		"Prefer this over run_command for listing."
}
func (t *listDirTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path": map[string]any{"type": "string", "description": "Relative directory path (default \".\")"},
		"depth": map[string]any{
			"type":        "integer",
			"description": "Tree depth to list (default 1 = flat listing, max 16)",
		},
		"include_size": map[string]any{
			"type":        "boolean",
			"description": "Include file sizes. When omitted: true if depth > 1, false if depth is 1.",
		},
	}, nil)
}

func (t *listDirTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Path        string `json:"path"`
		Depth       int    `json:"depth"`
		IncludeSize *bool  `json:"include_size"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Path == "" {
		in.Path = "."
	}
	if in.Depth == 0 {
		in.Depth = 1
	}
	if in.Depth < 1 || in.Depth > listDirMaxDepth {
		return "", fmt.Errorf("depth must be between 1 and %d", listDirMaxDepth)
	}
	includeSize := in.Depth > 1
	if in.IncludeSize != nil {
		includeSize = *in.IncludeSize
	}

	abs, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if isSecretPath(t.ws.Rel(abs), t.secretPathExceptions, t.secretPathPatterns) {
		return "", fmt.Errorf("listing secret-like path is blocked")
	}

	// Depth-1 without sizes preserves the historical flat listing byte-for-byte
	// when include_size is unset (golden invariant).
	if in.Depth == 1 && !includeSize {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return "", err
		}
		out := t.formatEntries(entries)
		if out == "" {
			return "(empty)", nil
		}
		return strings.TrimRight(out, "\n"), nil
	}

	view := ignoreView{}
	if t.ignore != nil {
		view = t.ignore.snapshot()
	}
	out, err := t.formatTree(ctx, abs, in.Depth, includeSize, view)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "(empty)", nil
	}
	return strings.TrimRight(out, "\n"), nil
}

// formatEntries renders directory entries under BOTH caps: at most maxEntries
// entries, and at most maxBytes bytes in total - including whichever
// truncation notice is appended, whose worst-case length is reserved up front
// so the notice can never push the result past the budget it reports.
func (t *listDirTool) formatEntries(entries []os.DirEntry) string {
	reserve := len(fmt.Sprintf(listDirByteNotice, t.maxBytes, len(entries)))
	var b strings.Builder
	used, emitted, byteBound := 0, 0, false
	for _, e := range entries {
		if t.maxEntries > 0 && emitted >= t.maxEntries {
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		if t.maxBytes > 0 && used+len(name)+1 > t.maxBytes-reserve {
			byteBound = true
			break
		}
		b.WriteString(name)
		b.WriteByte('\n')
		used += len(name) + 1
		emitted++
	}
	switch omitted := len(entries) - emitted; {
	case byteBound:
		fmt.Fprintf(&b, listDirByteNotice, t.maxBytes, omitted)
	case omitted > 0:
		fmt.Fprintf(&b, "... truncated (%d more)\n", omitted)
	}
	return b.String()
}

// recursiveNoticeReserve is the fixed worst-case block for all four notice
// species with max-width counts, reserved up front so content+notices always
// fit maxBytes.
func recursiveNoticeReserve(maxBytes int) int {
	// unreadable + beyond-depth + entry cap + byte cap, each with trailing \n
	pad := strings.Repeat("9", listDirCountWidth)
	return len(fmt.Sprintf("... %s entries unreadable\n", pad)) +
		len(fmt.Sprintf("... %s entries beyond depth\n", pad)) +
		len(fmt.Sprintf("... truncated (%s more encountered)\n", pad)) +
		len(fmt.Sprintf("... truncated at %d bytes (%s more)\n", maxBytes, pad))
}

type treeWalkState struct {
	b               strings.Builder
	used            int
	emitted         int
	budget          int  // content-byte budget when byteCapped; 0 means no content room
	byteCapped      bool // true when maxBytes > 0 (budget==0 is NOT uncapped)
	entryCap        int  // max emitted lines; 0 = uncapped
	includeSize     bool
	view            ignoreView
	secretsEx       []string
	secretsPat      []string
	beyondDepth     int
	unreadable      int
	moreEncountered int
	byteBound       bool
	entryBound      bool
	stop            bool
}

func (t *listDirTool) formatTree(ctx context.Context, abs string, depth int, includeSize bool, view ignoreView) (string, error) {
	st := &treeWalkState{
		includeSize: includeSize,
		view:        view,
		secretsEx:   t.secretPathExceptions,
		secretsPat:  t.secretPathPatterns,
		entryCap:    t.maxEntries,
	}
	if t.maxBytes > 0 {
		// Distinguish uncapped (maxBytes==0) from a positive cap that leaves no
		// content room after notice reservation. budget==0 with byteCapped must
		// refuse emits; treating it as uncapped overflowed the result.
		st.byteCapped = true
		reserve := recursiveNoticeReserve(t.maxBytes)
		if reserve > t.maxBytes {
			reserve = t.maxBytes
		}
		st.budget = t.maxBytes - reserve
	}
	if err := t.walkTree(ctx, st, abs, t.ws.Rel(abs), 1, depth); err != nil {
		return "", err
	}
	return st.finalize(t.maxBytes), nil
}

// formatNotices builds the trailing notice block in co-occurrence order:
// unreadable, beyond-depth, entry cap, byte cap — each at most once.
func (st *treeWalkState) formatNotices(maxBytes int) string {
	var b strings.Builder
	if st.unreadable > 0 {
		fmt.Fprintf(&b, "... %d entries unreadable\n", st.unreadable)
	}
	if st.beyondDepth > 0 {
		fmt.Fprintf(&b, "... %d entries beyond depth\n", st.beyondDepth)
	}
	if st.entryBound && st.moreEncountered > 0 {
		fmt.Fprintf(&b, "... truncated (%d more encountered)\n", st.moreEncountered)
	}
	if st.byteBound {
		fmt.Fprintf(&b, "... truncated at %d bytes (%d more)\n", maxBytes, st.moreEncountered)
	}
	return b.String()
}

// finalize joins content + notices and guarantees content+notices <= maxBytes
// when a positive cap is configured. Notices are preferred over content.
func (st *treeWalkState) finalize(maxBytes int) string {
	content := st.b.String()
	notices := st.formatNotices(maxBytes)
	if maxBytes <= 0 {
		return content + notices
	}
	if len(content)+len(notices) <= maxBytes {
		return content + notices
	}
	// Prefer full notices; shrink content to the remaining room on a line boundary.
	if len(notices) >= maxBytes {
		// Notices alone exceed the budget: keep as many complete notice lines as fit.
		return trimLinesToBudget(notices, maxBytes)
	}
	room := maxBytes - len(notices)
	content = trimLinesToBudget(content, room)
	return content + notices
}

// trimLinesToBudget returns a prefix of s of at most budget bytes, ending on a
// newline when possible so we never emit a partial entry line.
func trimLinesToBudget(s string, budget int) string {
	if budget <= 0 || s == "" {
		return ""
	}
	if len(s) <= budget {
		return s
	}
	s = s[:budget]
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[:i+1]
	}
	return ""
}

func (st *treeWalkState) tryEmit(line string) bool {
	if st.stop {
		return false
	}
	if st.entryCap > 0 && st.emitted >= st.entryCap {
		st.entryBound = true
		st.stop = true
		return false
	}
	need := len(line) + 1
	// byteCapped distinguishes "no content room" (budget==0) from uncapped.
	if st.byteCapped && st.used+need > st.budget {
		st.byteBound = true
		st.stop = true
		return false
	}
	st.b.WriteString(line)
	st.b.WriteByte('\n')
	st.used += need
	st.emitted++
	return true
}

func (t *listDirTool) walkTree(ctx context.Context, st *treeWalkState, abs, rel string, level, maxDepth int) error {
	if st.stop {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return err
	}
	// ReadDir returns lexical order.
	for i, e := range entries {
		if st.stop {
			// Remaining siblings in this directory were encountered but not emitted.
			st.moreEncountered += len(entries) - i
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := e.Name()
		childRel := name
		if rel != "" && rel != "." {
			childRel = filepath.ToSlash(filepath.Join(rel, name))
		} else {
			childRel = filepath.ToSlash(name)
		}
		indent := strings.Repeat("  ", level-1)

		// Directories (lstat / DirEntry type — symlinks to dirs are NOT dirs).
		if e.IsDir() {
			if err := t.emitDir(ctx, st, abs, name, childRel, indent, level, maxDepth); err != nil {
				return err
			}
			continue
		}
		t.emitFile(st, e, name, childRel, indent)
	}
	return nil
}

func (t *listDirTool) emitDir(ctx context.Context, st *treeWalkState, parentAbs, name, childRel, indent string, level, maxDepth int) error {
	// Secret directories: collapse as (blocked), never descend.
	if isSecretPath(childRel, st.secretsEx, st.secretsPat) {
		if !st.tryEmit(indent + name + "/  (blocked)") {
			st.moreEncountered++
		}
		return nil
	}
	// Ignored directories: collapse as (ignored), never descend.
	// (Explicit walk root is listed by the caller; children still apply ignore.)
	if st.view.ShouldIgnoreDir(name, childRel) {
		if !st.tryEmit(indent + name + "/  (ignored)") {
			st.moreEncountered++
		}
		return nil
	}

	// Depth cut: emit marker and count children encountered-not-emitted.
	if level >= maxDepth {
		if !st.tryEmit(indent + name + "/ ...") {
			st.moreEncountered++
			return nil
		}
		childAbs := filepath.Join(parentAbs, name)
		kids, err := os.ReadDir(childAbs)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				st.unreadable++
			}
			return nil
		}
		st.beyondDepth += len(kids)
		return nil
	}

	// Emit directory line (no size aggregation).
	line := indent + name + "/"
	if !st.tryEmit(line) {
		st.moreEncountered++
		return nil
	}
	return t.walkTree(ctx, st, filepath.Join(parentAbs, name), childRel, level+1, maxDepth)
}

func (t *listDirTool) emitFile(st *treeWalkState, e os.DirEntry, name, childRel, indent string) {
	// Secret files: name only, no size.
	if isSecretPath(childRel, st.secretsEx, st.secretsPat) {
		if !st.tryEmit(indent + name) {
			st.moreEncountered++
		}
		return
	}
	// Gitignore file patterns: omit entirely (grep/glob precedent), not shown.
	// Design for list_dir focuses on dir collapse; files matching gitignore
	// should not appear in tree walks that honor the same ignore decision.
	if st.view.ShouldIgnoreFile(name, childRel) {
		return
	}

	line := indent + name
	if st.includeSize {
		info, err := e.Info()
		switch {
		case err == nil:
			if info.Mode().IsRegular() {
				line = fmt.Sprintf("%s%s  %d", indent, name, info.Size())
			}
			// Symlinks and specials: name only (no size).
		case errors.Is(err, os.ErrNotExist):
			// Race: entry vanished — skip.
			return
		default:
			// Emit name without size; count as unreadable.
			st.unreadable++
		}
	}
	if !st.tryEmit(line) {
		st.moreEncountered++
	}
}
