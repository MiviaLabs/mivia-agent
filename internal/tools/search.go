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

// truncationReserve is the byte headroom a search tool holds back for
// whichever truncation notice it may append, so the notice is paid for out of
// the budget instead of pushing the result past it.
func truncationReserve(maxMatches, maxBytes int) int {
	return max(
		len(fmt.Sprintf(byteTruncNotice, maxBytes)),
		len(fmt.Sprintf(matchesTruncNotice, maxMatches)),
	)
}

type grepTool struct {
	ws                   *workspace.Root
	maxMatches           int
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
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
	return "Search file contents with a regex. Params: pattern (required); optional path (default \".\"), optional glob (e.g. *.md, *.py, *.ts). " +
		"Returns path:line:text. Prefer this over shell grep/rg via run_command."
}
func (t *grepTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Regular expression pattern"},
		"path":    map[string]any{"type": "string", "description": "Relative file or directory to search (default \".\")"},
		"glob":    map[string]any{"type": "string", "description": "Optional filename glob filter (e.g. *.py, *.ts, *.md)"},
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
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	root, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	matches, err := walkGrep(ctx, t.ws, root, re, in, t.maxMatches, t.maxBytes, t.secretPathExceptions, t.secretPathPatterns)
	if err != nil && !errors.Is(err, errMaxMatches) && !errors.Is(err, errMaxBytes) && err != context.Canceled {
		return "", err
	}
	if len(matches) == 0 {
		if errors.Is(err, errMaxBytes) {
			// No match fit the budget: say so rather than claim "no matches".
			return strings.TrimPrefix(fmt.Sprintf(byteTruncNotice, t.maxBytes), "\n"), nil
		}
		return "no matches", nil
	}
	out := strings.Join(matches, "\n")
	switch {
	case errors.Is(err, errMaxBytes):
		out += fmt.Sprintf(byteTruncNotice, t.maxBytes)
	case errors.Is(err, errMaxMatches):
		out += fmt.Sprintf(matchesTruncNotice, t.maxMatches)
	}
	return out, nil
}

type grepInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Glob    string `json:"glob"`
}

// globMatches reports whether a filename filter selects a file.
//
// Callers write globs in several forms and only one of them used to work:
// the filter matched the BASE NAME only, so "*.md" matched but every
// path-shaped glob - including "**/*.md", the form the sibling glob tool's
// own description recommends - matched nothing, and grep looked broken for
// whole file types.
//
// Accepted, in order: a bare pattern against the base name ("*.md"), the
// same pattern against the workspace-relative path ("src/*.go"), and a "**/"
// prefix meaning "at any depth" ("**/*.md", "docs/**/*.md"). Matching is
// case-insensitive on the extension-style patterns people actually type.
func globMatches(glob, rel, base string) bool {
	glob = filepath.ToSlash(glob)
	rel = filepath.ToSlash(rel)

	match := func(pattern, name string) bool {
		if ok, err := filepath.Match(pattern, name); err == nil && ok {
			return true
		}
		// Case-insensitive retry: "*.MD" should still find README.md.
		ok, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(name))
		return err == nil && ok
	}

	// "**/" means "at any depth": try the tail against the base name, and the
	// whole pattern against the path with the wildcard collapsed.
	if idx := strings.Index(glob, "**/"); idx >= 0 {
		prefix, tail := glob[:idx], glob[idx+3:]
		if !strings.Contains(tail, "/") && match(tail, base) {
			// A prefix like "docs/" still has to be honoured.
			if prefix == "" || strings.HasPrefix(rel, prefix) {
				return true
			}
		}
		if match(prefix+tail, rel) {
			return true
		}
	}
	return match(glob, base) || match(glob, rel)
}

func walkGrep(ctx context.Context, ws *workspace.Root, root string, re *regexp.Regexp, in grepInput, maxMatches, maxBytes int, secretExceptions, secretPatterns []string) ([]string, error) {
	var matches []string
	// Bytes available for match lines: the joining newlines are counted with
	// each line, and the closing notice is reserved out of the budget.
	var budget int
	if maxBytes > 0 {
		budget = maxBytes - truncationReserve(maxMatches, maxBytes)
	}
	total := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		rel := ws.Rel(path)
		if in.Glob != "" && !globMatches(in.Glob, rel, d.Name()) {
			return nil
		}
		if isSecretPath(rel, secretExceptions, secretPatterns) {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		// TOCTOU-safe open: refuse if path flipped to a special file since WalkDir.
		f, _, err := openRegularFile(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for sc.Scan() {
			if lineNo&0xff == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			lineNo++
			line := sc.Text()
			if re.MatchString(line) {
				if len(line) > 200 {
					line = line[:200] + "..."
				}
				entry := fmt.Sprintf("%s:%d:%s", rel, lineNo, line)
				need := len(entry)
				if len(matches) > 0 {
					need++ // joining newline
				}
				if maxBytes > 0 && total+need > budget {
					return errMaxBytes
				}
				matches = append(matches, entry)
				total += need
				if maxMatches > 0 && len(matches) >= maxMatches {
					return errMaxMatches
				}
			}
		}
		return nil
	})
	return matches, err
}

type globTool struct {
	ws                   *workspace.Root
	maxMatches           int
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
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
	return "Find file paths by glob pattern. Params: pattern (required), e.g. **/*.md or src/**/*.ts. Prefer over shell find."
}
func (t *globTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Glob pattern"},
	}, []string{"pattern"})
}

func (t *globTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Pattern string `json:"pattern"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	// filepath.Glob is limited; walk and match base or full rel path.
	var hits []string
	var budget int
	if t.maxBytes > 0 {
		budget = t.maxBytes - truncationReserve(t.maxMatches, t.maxBytes)
	}
	total := 0
	err := filepath.WalkDir(t.ws.Abs, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		rel := t.ws.Rel(path)
		if isSecretPath(rel, t.secretPathExceptions, t.secretPathPatterns) {
			return nil
		}
		// One glob semantics for both tools: grep's filter and this listing
		// must agree, or "**/*.md" means different things depending on which
		// tool you reach for.
		if globMatches(in.Pattern, rel, d.Name()) {
			need := len(rel)
			if len(hits) > 0 {
				need++ // joining newline
			}
			if t.maxBytes > 0 && total+need > budget {
				return errMaxBytes
			}
			hits = append(hits, rel)
			total += need
			if t.maxMatches > 0 && len(hits) >= t.maxMatches {
				return errMaxMatches
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errMaxMatches) && !errors.Is(err, errMaxBytes) {
		return "", err
	}
	if len(hits) == 0 {
		if errors.Is(err, errMaxBytes) {
			// No path fit the budget: say so rather than claim "no matches".
			return strings.TrimPrefix(fmt.Sprintf(byteTruncNotice, t.maxBytes), "\n"), nil
		}
		return "no matches", nil
	}
	out := strings.Join(hits, "\n")
	switch {
	case errors.Is(err, errMaxBytes):
		out += fmt.Sprintf(byteTruncNotice, t.maxBytes)
	case errors.Is(err, errMaxMatches):
		out += fmt.Sprintf(matchesTruncNotice, t.maxMatches)
	}
	return out, nil
}

// matchGlob is the legacy single-argument matcher, kept for callers that
// have only one string. Prefer globMatches, which knows both the
// workspace-relative path and the base name.
func matchGlob(pattern, name string) bool {
	return globMatches(pattern, name, filepath.Base(name))
}
