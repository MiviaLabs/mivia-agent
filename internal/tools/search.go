package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type grepTool struct {
	ws         *workspace.Root
	maxMatches int
}

func (t *grepTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

func (t *grepTool) Name() string { return "grep" }
func (t *grepTool) Description() string {
	return "Search file contents with a regex (path:line:text). Prefer this over run_command for content search."
}
func (t *grepTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Regular expression pattern"},
		"path":    map[string]any{"type": "string", "description": "Relative file or directory to search (default \".\")"},
		"glob":    map[string]any{"type": "string", "description": "Optional filename glob filter (e.g. *.py, *.ts, *.md)"},
	}, []string{"pattern"})
}

func (t *grepTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Glob    string `json:"glob"`
	}
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
	var matches []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if in.Glob != "" {
			ok, _ := filepath.Match(in.Glob, d.Name())
			if !ok {
				return nil
			}
		}
		rel := t.ws.Rel(path)
		if isSecretPath(rel) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if re.MatchString(line) {
				if len(line) > 200 {
					line = line[:200] + "..."
				}
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
				if len(matches) >= t.maxMatches {
					return fmt.Errorf("max matches")
				}
			}
		}
		return nil
	})
	if err != nil && err.Error() != "max matches" && err != context.Canceled {
		return "", err
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	out := strings.Join(matches, "\n")
	if err != nil && err.Error() == "max matches" {
		out += fmt.Sprintf("\n... truncated at %d matches", t.maxMatches)
	}
	return out, nil
}

type globTool struct {
	ws         *workspace.Root
	maxMatches int
}

func (t *globTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

func (t *globTool) Name() string { return "glob" }
func (t *globTool) Description() string {
	return "Find file paths by glob pattern (e.g. **/*.md, src/**/*.ts). Prefer this over shell find."
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
		if isSecretPath(rel) {
			return nil
		}
		// Support ** by converting to simple match on rel and base.
		ok := matchGlob(in.Pattern, rel) || matchGlob(in.Pattern, d.Name())
		if ok {
			hits = append(hits, rel)
			if len(hits) >= t.maxMatches {
				return fmt.Errorf("max matches")
			}
		}
		return nil
	})
	if err != nil && err.Error() != "max matches" {
		return "", err
	}
	if len(hits) == 0 {
		return "no matches", nil
	}
	out := strings.Join(hits, "\n")
	if err != nil && err.Error() == "max matches" {
		out += fmt.Sprintf("\n... truncated at %d matches", t.maxMatches)
	}
	return out, nil
}

func matchGlob(pattern, name string) bool {
	// Normalize ** to * for filepath.Match limitations.
	p := strings.ReplaceAll(pattern, "**/", "*")
	p = strings.ReplaceAll(p, "**", "*")
	ok, err := filepath.Match(p, name)
	if err != nil {
		return false
	}
	if ok {
		return true
	}
	// Also try matching only the basename against patterns like *.go
	ok, _ = filepath.Match(p, filepath.Base(name))
	return ok
}
