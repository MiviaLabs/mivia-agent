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

type grepTool struct {
	ws                   *workspace.Root
	maxMatches           int
	secretPathExceptions []string
	secretPathPatterns   []string
}

func (t *grepTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

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
	matches, err := walkGrep(ctx, t.ws, root, re, in, t.maxMatches, t.secretPathExceptions, t.secretPathPatterns)
	if err != nil && !errors.Is(err, errMaxMatches) && err != context.Canceled {
		return "", err
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	out := strings.Join(matches, "\n")
	if err != nil && errors.Is(err, errMaxMatches) {
		out += fmt.Sprintf("\n... truncated at %d matches", t.maxMatches)
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
// path-shaped glob — including "**/*.md", the form the sibling glob tool's
// own description recommends — matched nothing, and grep looked broken for
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

func walkGrep(ctx context.Context, ws *workspace.Root, root string, re *regexp.Regexp, in grepInput, max int, secretExceptions, secretPatterns []string) ([]string, error) {
	var matches []string
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
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
				if len(matches) >= max {
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
	secretPathExceptions []string
	secretPathPatterns   []string
}

func (t *globTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

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
			hits = append(hits, rel)
			if len(hits) >= t.maxMatches {
				return errMaxMatches
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errMaxMatches) {
		return "", err
	}
	if len(hits) == 0 {
		return "no matches", nil
	}
	out := strings.Join(hits, "\n")
	if err != nil && errors.Is(err, errMaxMatches) {
		out += fmt.Sprintf("\n... truncated at %d matches", t.maxMatches)
	}
	return out, nil
}

// matchGlob is the legacy single-argument matcher, kept for callers that
// have only one string. Prefer globMatches, which knows both the
// workspace-relative path and the base name.
func matchGlob(pattern, name string) bool {
	return globMatches(pattern, name, filepath.Base(name))
}
