package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var errMaxMatches = fmt.Errorf("max matches")

// errMaxBytes stops a walk that has filled its byte budget. Both search tools
// cap results by COUNT, and a count cap bounds no number of bytes: a match
// line carries a workspace-relative path, which on a deep tree approaches
// PATH_MAX. The byte budget is what makes their output bounded at all.
var errMaxBytes = fmt.Errorf("max bytes")

const (
	byteTruncNotice    = "\n... truncated at %d bytes"
	matchesTruncNotice = "\n... truncated at %d matches"
)

// walkErrsNoticeReserve is the byte headroom a search tool holds back for the
// walk-error trailer ("… N files skipped (first: …)") when its byte budget is
// set. executeGrep/executeGlob clamp that trailer to whatever budget room the
// content and truncation notice left; this reserve is what guarantees the room
// exists even when matches fill the whole budget. Sized for the capped-count
// header plus a readable prefix of the first error line (both search tools cap
// the trailer at 10 entries).
const walkErrsNoticeReserve = 256

// truncationReserve is the byte headroom a search tool holds back for
// whichever truncation notice it may append (byte or match - exactly one
// fires), plus the walk-error trailer, so every notice is paid for out of the
// budget instead of pushing the result past it.
func truncationReserve(maxMatches, maxBytes int) int {
	return max(
		len(fmt.Sprintf(byteTruncNotice, maxBytes)),
		len(fmt.Sprintf(matchesTruncNotice, maxMatches)),
	) + walkErrsNoticeReserve
}

// appendWalkNotice appends the walk-error trailer, clamping it to the room the
// content and truncation notice left in the byte budget when one is set, so a
// pathological first error line (a near-PATH_MAX path) can never push an
// honest result past its declared ResultBudgetBytes. truncateUTF8 never splits
// a rune. With no byte budget (0 = uncapped) the trailer is appended whole.
func appendWalkNotice(out string, maxBytes int, errs *walkErrors) string {
	if errs == nil || errs.count() == 0 {
		return out
	}
	notice := errs.notice()
	if maxBytes > 0 {
		notice = truncateUTF8(notice, max(0, maxBytes-len(out)))
	}
	return out + notice
}

type grepTool struct {
	ws                   *workspace.Root
	maxMatches           int
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
	// ignore is the shared ignore decision (floor + config + gitignore). Nil is
	// safe: walks treat it as an empty snapshot (match nothing).
	ignore *gitignoreMatcher
}

func (t *grepTool) Capability(args json.RawMessage) Capability {
	// Capability.MaxResultBytes is deliberately NOT declared - see
	// listDirTool.Capability. The budget reaches the dispatcher backstop via
	// ResultBudgetBytes.
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

// ResultBudgetBytes declares the configured byte budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool).
func (t *grepTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *grepTool) Name() string { return "grep" }
func (t *grepTool) Description() string {
	return "Search file contents with a regex. Returns path:line:text. Paginate with offset/limit."
}
func (t *grepTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"pattern":            map[string]any{"type": "string", "description": "Regular expression"},
		"path":               map[string]any{"type": "string", "description": "Relative file or directory to search (default \".\")"},
		"glob":               map[string]any{"type": "string", "description": "Filename glob filter (e.g. *.py, *.ts)"},
		"case_insensitive":   map[string]any{"type": "boolean", "description": "Match ignoring case (default false)"},
		"files_with_matches": map[string]any{"type": "boolean", "description": "Return only matching file paths (default false)"},
		"offset":             map[string]any{"type": "integer", "description": "0-based match index to skip (pagination)"},
		"limit":              map[string]any{"type": "integer", "description": "Max matches to return"},
	}, []string{"pattern"})
}

func (t *grepTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.executeGrep(ctx, args)
}

func (t *grepTool) executeGrep(ctx context.Context, args json.RawMessage) (string, error) {
	var in grepInput
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if in.Path == "" {
		in.Path = "."
	}
	pattern := in.Pattern
	if in.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	root, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	view := ignoreView{}
	if t.ignore != nil {
		view = t.ignore.snapshot()
	}
	matches, errs, err := walkGrep(ctx, t.ws, root, re, in, t.maxMatches, t.maxBytes, t.secretPathExceptions, t.secretPathPatterns, view)
	// A real cancellation (including plain context.Canceled) must discard
	// matches/errs unread, never fall through: see walkFilteredFiles' doc
	// comment for why.
	if err != nil && !errors.Is(err, errMaxMatches) && !errors.Is(err, errMaxBytes) {
		return "", err
	}
	// Apply pagination: offset and limit.
	totalFound := len(matches)
	if in.Offset > 0 {
		if in.Offset >= len(matches) {
			// A walk cut off at the byte budget (errMaxBytes) has collected
			// only a partial prefix, so "no matches" here is a false
			// negative that silently drops the truncation notice - mirror
			// the empty-result branch below. An untruncated offset-past-end
			// page keeps the "no matches" convention.
			if errors.Is(err, errMaxBytes) {
				return strings.TrimPrefix(fmt.Sprintf(byteTruncNotice, t.maxBytes), "\n"), nil
			}
			return "no matches", nil
		}
		matches = matches[in.Offset:]
	}
	if in.Limit > 0 && len(matches) > in.Limit {
		matches = matches[:in.Limit]
	}
	if len(matches) == 0 {
		if errors.Is(err, errMaxBytes) {
			return strings.TrimPrefix(fmt.Sprintf(byteTruncNotice, t.maxBytes), "\n"), nil
		}
		return "no matches", nil
	}
	out := strings.Join(matches, "\n")
	// Pagination trailer.
	if in.Limit > 0 && in.Offset+len(matches) < totalFound {
		remaining := totalFound - in.Offset - len(matches)
		out += fmt.Sprintf("\n... %d more matches (use offset=%d to continue)", remaining, in.Offset+len(matches))
	}
	switch {
	case errors.Is(err, errMaxBytes):
		out += fmt.Sprintf(byteTruncNotice, t.maxBytes)
	case errors.Is(err, errMaxMatches):
		out += fmt.Sprintf(matchesTruncNotice, t.maxMatches)
	}
	// Error reporting trailer. The walk-error notice is part of the byte
	// budget: it is clamped to the room the content and truncation notice
	// left (truncateUTF8 never splits a rune), so a pathological first error
	// line (a near-PATH_MAX path) can never push an honest result past its
	// declared ResultBudgetBytes.
	return appendWalkNotice(out, t.maxBytes, errs), nil
}

type grepInput struct {
	Pattern          string `json:"pattern"`
	Path             string `json:"path"`
	Glob             string `json:"glob"`
	CaseInsensitive  bool   `json:"case_insensitive,omitempty"`
	FilesWithMatches bool   `json:"files_with_matches,omitempty"`
	Offset           int    `json:"offset,omitempty"`
	Limit            int    `json:"limit,omitempty"`
}

// appendMatchEntry appends entry to matches if it fits the byte budget,
// returning errMaxBytes/errMaxMatches on the same terms scanFile's two match
// sites (files-with-matches vs full match line) both need.
func appendMatchEntry(entry string, matches *[]string, total *int, budget, maxMatches int) error {
	need := len(entry)
	if len(*matches) > 0 {
		need++ // joining newline
	}
	if budget > 0 && *total+need > budget {
		return errMaxBytes
	}
	*matches = append(*matches, entry)
	*total += need
	if maxMatches > 0 && len(*matches) >= maxMatches {
		return errMaxMatches
	}
	return nil
}

// scanFile opens a regular file, scans it for regex matches, and appends
// matching entries to matches. If filesWithMatches is true, it stops after
// the first match and returns the file path as a bare name. Line scanning is
// delegated to scanLinesWithContext (fs_guard.go), which owns f's lifecycle
// and honors ctx during a blocking Scan() itself, not just between lines; a
// scan error (sc.Err()) surfaces as scanLinesWithContext's return value and
// is recorded into errs here, same as the old post-loop sc.Err() check did -
// not propagated as scanFile's own return value.
func scanFile(ctx context.Context, path, rel string, re *regexp.Regexp, in grepInput, matches *[]string, total *int, budget, maxMatches int, errs *walkErrors) error {
	f, _, err := openRegularFile(path)
	if err != nil {
		errs.add(rel, err)
		return nil
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	matched := false
	consume := func(line string) (bool, error) {
		lineNo++
		if !re.MatchString(line) {
			return false, nil
		}
		if in.FilesWithMatches {
			if matched {
				return false, nil
			}
			matched = true
			return false, appendMatchEntry(rel, matches, total, budget, maxMatches)
		}
		if len(line) > 200 {
			line = truncateUTF8(line, 200) + "..."
		}
		entry := fmt.Sprintf("%s:%d:%s", rel, lineNo, line)
		return false, appendMatchEntry(entry, matches, total, budget, maxMatches)
	}
	// scanFile is called with a nil ctx directly in tests (and permitted by
	// walkFilteredFiles/walkGrep, which both treat nil as "no cancellation"),
	// but scanLinesWithContext requires a non-nil ctx - substitute Background
	// so nil-ctx callers keep their old never-canceled behavior.
	scanCtx := ctx
	if scanCtx == nil {
		scanCtx = context.Background()
	}
	scanErr := scanLinesWithContext(scanCtx, sc, f, consume)
	switch {
	case scanErr == nil:
		return nil
	case errors.Is(scanErr, errMaxMatches), errors.Is(scanErr, errMaxBytes),
		errors.Is(scanErr, context.Canceled), errors.Is(scanErr, context.DeadlineExceeded):
		return scanErr
	default:
		errs.add(rel, scanErr)
		return nil
	}
}

func walkGrep(ctx context.Context, ws *workspace.Root, root string, re *regexp.Regexp, in grepInput, maxMatches, maxBytes int, secretExceptions, secretPatterns []string, view ignoreView) ([]string, *walkErrors, error) {
	var matches []string
	var budget int
	if maxBytes > 0 {
		budget = maxBytes - truncationReserve(maxMatches, maxBytes)
		// A reserve larger than the budget itself (tiny maxBytes) must still
		// keep the walk bounded: scanFile reads `budget > 0` as "byte cap
		// active", so floor the budget at 1 and the first match trips
		// errMaxBytes instead of disabling the cap.
		if budget < 1 {
			budget = 1
		}
	}
	total := 0
	errs := &walkErrors{maxErrs: 10}
	err := walkFilteredFiles(ctx, ws, root, in.Glob, secretExceptions, secretPatterns, view, true, errs, func(path, rel string, _ os.FileInfo) error {
		return scanFile(ctx, path, rel, re, in, &matches, &total, budget, maxMatches, errs)
	})
	return matches, errs, err
}

// walkFilteredFiles walks root, visiting each non-ignored, non-secret,
// glob-matching file exactly once, in filepath.WalkDir order. It is the
// shared traversal policy for grep, glob, and inspect_repository:
// ignore/secret/glob rules must not drift between them. errs accumulates both
// walk-level errors (permission denied, unreadable regular-file stat) and any
// the caller records from inside visit, so both land in the same trailer
// notice.
//
// requireRegular gates the "not a regular file" check: grep and
// inspect_repository read file content, so a symlink or device node is
// unreadable content and correctly skipped as a walk error. glob only lists
// matching paths - it never opens the file - so it must keep listing
// symlinks and other non-regular entries exactly as it did before this
// traversal was extracted; requireRegular=false skips the check entirely and
// visit receives a nil info.
//
// The walk itself races on a background goroutine against ctx.Done(), the
// same escape hatch readFileWithContext uses (fs_guard.go): see the doc
// comment on the goroutine below for why (a real, reported hang, not a
// theoretical one) and its data-race implication for callers.
func walkFilteredFiles(ctx context.Context, ws *workspace.Root, root, glob string, secretExceptions, secretPatterns []string, view ignoreView, requireRegular bool, errs *walkErrors, visit func(path, rel string, info os.FileInfo) error) error {
	walk := func() error {
		return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				errs.add(path, walkErr)
				return nil
			}
			if ctx != nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
			}
			rel := ws.Rel(path)
			if d.IsDir() {
				// Do not skip the walk root even if it matches ignore (explicit path).
				if path != root && view.ShouldIgnoreDir(d.Name(), rel) {
					return filepath.SkipDir
				}
				return nil
			}
			if view.ShouldIgnoreFile(d.Name(), rel) {
				return nil
			}
			if glob != "" && !globMatchesCtx(ctx, glob, rel, d.Name()) {
				return nil
			}
			if isSecretPath(rel, secretExceptions, secretPatterns) {
				return nil
			}
			if !requireRegular {
				return visit(path, rel, nil)
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() {
				errs.add(rel, fmt.Errorf("not a regular file"))
				return nil
			}
			return visit(path, rel, info)
		})
	}
	if ctx == nil {
		return walk()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A single stalled directory syscall (stale NFS/FUSE handle, a directory
	// removed mid-walk) can block filepath.WalkDir forever with no way for
	// ctx to interrupt it - a real, reported hang, not a theoretical one.
	// Racing on a goroutine cannot kill that stuck call (Go has no
	// primitive for it), but frees the caller the moment ctx cancels. The
	// walk goroutine is then abandoned, not killed: it may keep running and
	// keep calling visit/errs.add after this function has returned, so
	// every caller MUST discard (never read) whatever they accumulated on
	// a ctx.Done() return - see each caller's own comment for how.
	done := make(chan error, 1)
	go func() { done <- walk() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

type globTool struct {
	ws                   *workspace.Root
	maxMatches           int
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
	// ignore is the shared ignore decision (floor + config + gitignore). Nil is
	// safe: walks treat it as an empty snapshot (match nothing).
	ignore *gitignoreMatcher
}

func (t *globTool) Capability(args json.RawMessage) Capability {
	// Capability.MaxResultBytes is deliberately NOT declared - see
	// listDirTool.Capability.
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

// ResultBudgetBytes declares the configured byte budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool).
func (t *globTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *globTool) Name() string { return "glob" }
func (t *globTool) Description() string {
	return "Find file paths by glob pattern (e.g. **/*.md, src/**/*.ts)."
}
func (t *globTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Glob pattern"},
		"path":    map[string]any{"type": "string", "description": "Relative directory to search (default \".\")"},
		"offset":  map[string]any{"type": "integer", "description": "0-based path index to skip (pagination)"},
		"limit":   map[string]any{"type": "integer", "description": "Max paths to return"},
	}, []string{"pattern"})
}

func (t *globTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in globInput
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	root := t.ws.Abs
	if in.Path != "" {
		var err error
		root, err = t.ws.Resolve(in.Path)
		if err != nil {
			return "", err
		}
	}
	view := ignoreView{}
	if t.ignore != nil {
		view = t.ignore.snapshot()
	}
	hits, errs, err := walkGlob(ctx, t.ws, root, in.Pattern, t.maxMatches, t.maxBytes, t.secretPathExceptions, t.secretPathPatterns, view)
	if err != nil && !errors.Is(err, errMaxMatches) && !errors.Is(err, errMaxBytes) {
		return "", err
	}
	totalFound := len(hits)
	if in.Offset > 0 {
		if in.Offset >= len(hits) {
			// Same false-negative guard as executeGrep: an offset past a
			// byte-truncated prefix must report the byte notice, not
			// "no matches"; an untruncated offset-past-end page keeps the
			// "no matches" convention.
			if errors.Is(err, errMaxBytes) {
				return strings.TrimPrefix(fmt.Sprintf(byteTruncNotice, t.maxBytes), "\n"), nil
			}
			return "no matches", nil
		}
		hits = hits[in.Offset:]
	}
	if in.Limit > 0 && len(hits) > in.Limit {
		hits = hits[:in.Limit]
	}
	if len(hits) == 0 {
		if errors.Is(err, errMaxBytes) {
			return strings.TrimPrefix(fmt.Sprintf(byteTruncNotice, t.maxBytes), "\n"), nil
		}
		return "no matches", nil
	}
	out := strings.Join(hits, "\n")
	if in.Limit > 0 && in.Offset+len(hits) < totalFound {
		remaining := totalFound - in.Offset - len(hits)
		out += fmt.Sprintf("\n... %d more paths (use offset=%d to continue)", remaining, in.Offset+len(hits))
	}
	switch {
	case errors.Is(err, errMaxBytes):
		out += fmt.Sprintf(byteTruncNotice, t.maxBytes)
	case errors.Is(err, errMaxMatches):
		out += fmt.Sprintf(matchesTruncNotice, t.maxMatches)
	}
	// Part of the byte budget, like executeGrep's trailer: clamped to the
	// room the content and truncation notice left, never splitting a rune.
	return appendWalkNotice(out, t.maxBytes, errs), nil
}

// walkGlob walks the filesystem matching files against a glob pattern.
func walkGlob(ctx context.Context, ws *workspace.Root, root, pattern string, maxMatches, maxBytes int, secretExceptions, secretPatterns []string, view ignoreView) ([]string, *walkErrors, error) {
	var hits []string
	var budget int
	if maxBytes > 0 {
		budget = maxBytes - truncationReserve(maxMatches, maxBytes)
		// Same floor as walkGrep: a reserve larger than the budget must keep
		// the walk bounded (immediate errMaxBytes), not disable the byte cap.
		if budget < 1 {
			budget = 1
		}
	}
	total := 0
	errs := &walkErrors{maxErrs: 10}
	err := walkFilteredFiles(ctx, ws, root, pattern, secretExceptions, secretPatterns, view, false, errs, func(_, rel string, _ os.FileInfo) error {
		need := len(rel)
		if len(hits) > 0 {
			need++
		}
		if maxBytes > 0 && total+need > budget {
			return errMaxBytes
		}
		hits = append(hits, rel)
		total += need
		if maxMatches > 0 && len(hits) >= maxMatches {
			return errMaxMatches
		}
		return nil
	})
	return hits, errs, err
}

// globInput is the parameter struct for the glob tool.
type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}
