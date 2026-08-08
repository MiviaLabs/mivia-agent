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
	return "Search file contents with a regex. Params: pattern (required); optional path (default \".\"), optional glob (e.g. *.md, *.py, *.ts), optional case_insensitive, optional files_with_matches, optional offset/limit for pagination. " +
		"Returns path:line:text. Prefer this over shell grep/rg via run_command."
}
func (t *grepTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"pattern":            map[string]any{"type": "string", "description": "Regular expression pattern"},
		"path":               map[string]any{"type": "string", "description": "Relative file or directory to search (default \".\")"},
		"glob":               map[string]any{"type": "string", "description": "Optional filename glob filter (e.g. *.py, *.ts, *.md)"},
		"case_insensitive":   map[string]any{"type": "boolean", "description": "Match without regard to case (default false)"},
		"files_with_matches": map[string]any{"type": "boolean", "description": "Show only matching file paths, not match lines (default false)"},
		"offset":             map[string]any{"type": "integer", "description": "Optional 0-based match index to skip (for pagination)"},
		"limit":              map[string]any{"type": "integer", "description": "Optional max matches to return"},
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
	if err != nil && !errors.Is(err, errMaxMatches) && !errors.Is(err, errMaxBytes) && err != context.Canceled {
		return "", err
	}
	// Apply pagination: offset and limit.
	totalFound := len(matches)
	if in.Offset > 0 {
		if in.Offset >= len(matches) {
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

// scanFile opens a regular file, scans it for regex matches, and appends
// matching entries to matches. It returns the number of matches added.
// If filesWithMatches is true, it stops after the first match and returns
// the file path as a bare name.
func scanFile(ctx context.Context, path, rel string, re *regexp.Regexp, in grepInput, matches *[]string, total *int, budget, maxMatches int, errs *walkErrors) error {
	f, _, err := openRegularFile(path)
	if err != nil {
		errs.add(rel, err)
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	matched := false
	for sc.Scan() {
		if lineNo&0xff == 0 {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
		}
		lineNo++
		line := sc.Text()
		if re.MatchString(line) {
			if in.FilesWithMatches {
				if matched {
					continue
				}
				matched = true
				entry := rel
				need := len(entry)
				if len(*matches) > 0 {
					need++
				}
				if budget > 0 && *total+need > budget {
					return errMaxBytes
				}
				*matches = append(*matches, entry)
				*total += need
				if maxMatches > 0 && len(*matches) >= maxMatches {
					return errMaxMatches
				}
				continue
			}
			if len(line) > 200 {
				line = truncateUTF8(line, 200) + "..."
			}
			entry := fmt.Sprintf("%s:%d:%s", rel, lineNo, line)
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
		}
	}
	if err := sc.Err(); err != nil {
		errs.add(rel, err)
	}
	return nil
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

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
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
		if in.Glob != "" && !globMatches(in.Glob, rel, d.Name()) {
			return nil
		}
		if isSecretPath(rel, secretExceptions, secretPatterns) {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			errs.add(rel, fmt.Errorf("not a regular file"))
			return nil
		}
		return scanFile(ctx, path, rel, re, in, &matches, &total, budget, maxMatches, errs)
	})
	return matches, errs, err
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
	return "Find file paths by glob pattern. Params: pattern (required), e.g. **/*.md or src/**/*.ts. Optional path (default \".\"). Prefer over shell find."
}
func (t *globTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Glob pattern"},
		"path":    map[string]any{"type": "string", "description": "Relative directory to search (default \".\")"},
		"offset":  map[string]any{"type": "integer", "description": "Optional 0-based path index to skip (for pagination)"},
		"limit":   map[string]any{"type": "integer", "description": "Optional max paths to return"},
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
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			errs.add(path, walkErr)
			return nil
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
		if isSecretPath(rel, secretExceptions, secretPatterns) {
			return nil
		}
		if globMatches(pattern, rel, d.Name()) {
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
