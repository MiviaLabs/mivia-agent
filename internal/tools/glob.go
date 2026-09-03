package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

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
