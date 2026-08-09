package tools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// inspectRepositoryVersion is the output envelope's schema version. Bump it
// only for a breaking field-shape change.
const inspectRepositoryVersion = 1

// Truncation reasons. Exactly one fires per call; empty means untruncated.
const (
	inspectTruncResultLimit = "result_limit"
	inspectTruncByteLimit   = "byte_limit"
	inspectTruncWalkErrors  = "walk_error_limit"
)

// inspectEnvelopeSlack is extra byte headroom held back on top of the
// measured envelope size (Execute marshals the envelope with Results unset
// to get an exact byte count for everything else - see inspectEngine.envelopeReserve).
// It covers what that measurement does not: the "results" array's own
// brackets, one comma per element, and result_count's digits growing as
// results are added.
const inspectEnvelopeSlack = 64

// inspectRepositoryTool answers a bounded regex search across one or more
// workspace paths with per-match line context in one call, collapsing the
// grep-then-read_file round trip. See internal/tools/search.go for the
// shared ignore/secret/glob walk it reuses.
type inspectRepositoryTool struct {
	ws                   *workspace.Root
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
	// ignore is the shared ignore decision (floor + config + gitignore), the
	// same source list_dir/grep/glob use. Nil is safe: an empty snapshot
	// matches nothing.
	ignore *gitignoreMatcher
}

func (t *inspectRepositoryTool) Name() string { return "inspect_repository" }

func (t *inspectRepositoryTool) Description() string {
	return "Search file contents with a regex across one or more paths and return matches with surrounding line context in one bounded call. " +
		"Params: query (required regex), optional paths (default [\".\"]), optional glob (e.g. *.md, *.py, *.ts), max_results (required, 1-100), optional context_lines (0-10, default 0). " +
		"Returns fixed-shape JSON with provenance and truncation state. Prefer this over separate grep and read_file calls when you need match context."
}

func (t *inspectRepositoryTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"query": map[string]any{"type": "string", "description": "Regular expression pattern (required)"},
		"paths": map[string]any{
			"type":        "array",
			"description": "Workspace-relative paths to search (default [\".\"])",
			"items":       map[string]any{"type": "string"},
		},
		"glob": map[string]any{"type": "string", "description": "Optional filename glob filter (e.g. *.py, *.ts, *.md)"},
		"max_results": map[string]any{
			"type":        "integer",
			"description": "Maximum number of results to return (1-100, required)",
			"minimum":     float64(1),
			"maximum":     float64(100),
		},
		"context_lines": map[string]any{
			"type":        "integer",
			"description": "Lines of context before and after each match (0-10, default 0)",
			"minimum":     float64(0),
			"maximum":     float64(10),
		},
	}, []string{"query", "max_results"})
}

// ResultBudgetBytes declares the configured byte budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool). Like grep's and
// glob's maxBytes, <= 0 means uncapped by construction, matching the rest of
// this package's convention (DefaultOptions.MaxReadBytes, etc.) — see
// marshalInspectOutput's maxBytes > 0 guards. NewDefaultRegistry never
// constructs this tool with an uncapped value: the config layer floors
// max_inspect_repository_bytes at 4 KiB and inspectRepositoryBudget
// defaults <= 0 to 64 KiB. A caller that constructs inspectRepositoryTool
// directly with maxBytes <= 0 is opting into that same uncapped convention,
// not hitting an oversight.
func (t *inspectRepositoryTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *inspectRepositoryTool) Capability(args json.RawMessage) Capability {
	key := "workspace:read"
	var in inspectRepositoryInput
	if err := json.Unmarshal(args, &in); err == nil {
		requested := in.Paths
		if requested == nil {
			requested = []string{"."}
		}
		if scopes, err := t.normalizeScopes(requested); err == nil {
			key = inspectResourceKey(scopes, in.Glob, querySHA256(in.Query))
		}
	}
	return Capability{Class: ExecutionRead, ResourceKey: key}
}

type inspectRepositoryInput struct {
	Query        string   `json:"query"`
	Paths        []string `json:"paths"`
	Glob         string   `json:"glob"`
	MaxResults   int      `json:"max_results"`
	ContextLines int      `json:"context_lines"`
}

// inspectScope is one normalized, workspace-relative search root.
type inspectScope struct {
	rel string
	abs string
}

// normalizeScopes resolves each requested path through workspace.Root.Resolve,
// rejects duplicate normalized (absolute) paths, and returns scopes sorted by
// relative path for deterministic processing order.
func (t *inspectRepositoryTool) normalizeScopes(requested []string) ([]inspectScope, error) {
	seen := make(map[string]bool, len(requested))
	scopes := make([]inspectScope, 0, len(requested))
	for _, p := range requested {
		abs, err := t.ws.Resolve(p)
		if err != nil {
			return nil, err
		}
		rel := t.ws.Rel(abs)
		if seen[abs] {
			return nil, fmt.Errorf("duplicate path %q after normalization", rel)
		}
		seen[abs] = true
		scopes = append(scopes, inspectScope{rel: rel, abs: abs})
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].rel < scopes[j].rel })
	return scopes, nil
}

func inspectResourceKey(scopes []inspectScope, glob, querySHA string) string {
	h := sha256.New()
	for _, s := range scopes {
		h.Write([]byte(s.rel))
		h.Write([]byte{0})
	}
	h.Write([]byte{0x1e})
	h.Write([]byte(glob))
	h.Write([]byte{0x1e})
	h.Write([]byte(querySHA))
	return "inspect:" + hex.EncodeToString(h.Sum(nil))
}

func querySHA256(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:])
}

// inspectProvenance reproduces the inspection scope without a raw query or
// absolute path.
type inspectProvenance struct {
	WorkspaceRoot       string   `json:"workspace_root"`
	Paths               []string `json:"paths"`
	Glob                string   `json:"glob"`
	QuerySHA256         string   `json:"query_sha256"`
	IgnorePolicy        string   `json:"ignore_policy"`
	SecretPathsExcluded bool     `json:"secret_paths_excluded"`
}

type inspectResult struct {
	Path    string   `json:"path"`
	Line    int      `json:"line"`
	Text    string   `json:"text"`
	Context []string `json:"context"`
}

type inspectOutput struct {
	Version          int               `json:"version"`
	Provenance       inspectProvenance `json:"provenance"`
	Results          []inspectResult   `json:"results"`
	ResultCount      int               `json:"result_count"`
	Truncated        bool              `json:"truncated"`
	TruncationReason string            `json:"truncation_reason"`
}

func (t *inspectRepositoryTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in inspectRepositoryInput
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	requested, err := validateInspectRepositoryInput(in)
	if err != nil {
		return "", err
	}
	scopes, err := t.normalizeScopes(requested)
	if err != nil {
		return "", err
	}
	re, err := regexp.Compile(in.Query)
	if err != nil {
		return "", fmt.Errorf("invalid query: %w", err)
	}
	view := ignoreView{}
	if t.ignore != nil {
		view = t.ignore.snapshot()
	}
	out := newInspectOutput(in, scopes)
	envelopeBytes, err := inspectEnvelopeBytes(out)
	if err != nil {
		return "", err
	}
	eng := t.newInspectEngine(in, re, view, envelopeBytes)
	results, truncated, reason, err := eng.run(ctx, scopes)
	if err != nil {
		return "", err
	}
	out.Results, out.ResultCount = results, len(results)
	out.Truncated, out.TruncationReason = truncated, reason
	return marshalInspectOutput(out, t.maxBytes)
}

func validateInspectRepositoryInput(in inspectRepositoryInput) ([]string, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if in.MaxResults < 1 || in.MaxResults > 100 {
		return nil, fmt.Errorf("max_results must be between 1 and 100")
	}
	if in.ContextLines < 0 || in.ContextLines > 10 {
		return nil, fmt.Errorf("context_lines must be between 0 and 10")
	}
	if in.Paths != nil && len(in.Paths) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}
	requested := in.Paths
	if requested == nil {
		requested = []string{"."}
	}
	return requested, nil
}

func newInspectOutput(in inspectRepositoryInput, scopes []inspectScope) inspectOutput {
	relPaths := make([]string, len(scopes))
	for i, s := range scopes {
		relPaths[i] = s.rel
	}
	return inspectOutput{
		Version: inspectRepositoryVersion,
		Provenance: inspectProvenance{
			WorkspaceRoot:       ".",
			Paths:               relPaths,
			Glob:                in.Glob,
			QuerySHA256:         querySHA256(in.Query),
			IgnorePolicy:        "workspace-configured",
			SecretPathsExcluded: true,
		},
	}
}

func inspectEnvelopeBytes(out inspectOutput) (int, error) {
	raw, err := json.Marshal(out)
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}

func (t *inspectRepositoryTool) newInspectEngine(in inspectRepositoryInput, re *regexp.Regexp, view ignoreView, envelopeBytes int) *inspectEngine {
	return &inspectEngine{
		ws:                   t.ws,
		maxBytes:             t.maxBytes,
		envelopeReserve:      envelopeBytes,
		secretPathExceptions: t.secretPathExceptions,
		secretPathPatterns:   t.secretPathPatterns,
		view:                 view,
		re:                   re,
		glob:                 in.Glob,
		maxResults:           in.MaxResults,
		contextLines:         in.ContextLines,
	}
}

// marshalInspectOutput serializes out and, if it would exceed maxBytes,
// drops trailing results (marking the output truncated) until it fits. This
// is the hard guarantee behind "never exceeds its result byte cap": the
// engine's own byte accounting is an early-stop optimization, not the safety
// net. Once this loop actually removes a result the marshal-stage byte cap is
// what determined the final shape, so it - not whatever reason the engine
// recorded first - is the honest truncation_reason from that point on.
func marshalInspectOutput(out inspectOutput, maxBytes int) (string, error) {
	payload, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	for maxBytes > 0 && len(payload) > maxBytes && len(out.Results) > 0 {
		out.Results = out.Results[:len(out.Results)-1]
		out.ResultCount = len(out.Results)
		out.Truncated = true
		out.TruncationReason = inspectTruncByteLimit
		payload, err = json.Marshal(out)
		if err != nil {
			return "", err
		}
	}
	if maxBytes > 0 && len(payload) > maxBytes {
		return "", fmt.Errorf("inspect_repository: result exceeds configured byte cap (%d bytes)", maxBytes)
	}
	return string(payload), nil
}

// inspectEngine walks normalized scopes in deterministic order, applying the
// shared ignore/secret/glob policy once per scope, and bounds its own
// accumulation by result count and (approximately) by byte budget.
type inspectEngine struct {
	ws       *workspace.Root
	maxBytes int
	// envelopeReserve is the exact marshaled byte size of everything in the
	// output except Results (Execute measures it directly), so the budget
	// below accounts for Provenance.Paths regardless of how many/how long the
	// requested paths are.
	envelopeReserve      int
	secretPathExceptions []string
	secretPathPatterns   []string
	view                 ignoreView
	re                   *regexp.Regexp
	glob                 string
	maxResults           int
	contextLines         int
}

func (e *inspectEngine) run(ctx context.Context, scopes []inspectScope) ([]inspectResult, bool, string, error) {
	seen := make(map[string]bool)
	var collected []inspectResult
	truncated := false
	reason := ""
	budget := e.maxBytes - e.envelopeReserve - inspectEnvelopeSlack
	if budget < 0 {
		budget = 0
	}
	used := 0
	cappedScopes := 0

	stop := func(r string) error {
		if !truncated {
			truncated = true
			reason = r
		}
		if r == inspectTruncByteLimit {
			return errMaxBytes
		}
		return errMaxMatches
	}

	for _, scope := range scopes {
		if truncated {
			break
		}
		errs := &walkErrors{maxErrs: 10}
		walkErr := walkFilteredFiles(ctx, e.ws, scope.abs, e.glob, e.secretPathExceptions, e.secretPathPatterns, e.view, true, errs, func(path, rel string, _ os.FileInfo) error {
			for _, m := range e.scanFileMatches(ctx, path, rel) {
				key := m.Path + "\x00" + strconv.Itoa(m.Line)
				if seen[key] {
					continue
				}
				encoded, marshalErr := json.Marshal(m)
				if marshalErr != nil {
					continue
				}
				if e.maxBytes > 0 && used+len(encoded) > budget {
					return stop(inspectTruncByteLimit)
				}
				seen[key] = true
				collected = append(collected, m)
				used += len(encoded)
				if len(collected) >= e.maxResults {
					return stop(inspectTruncResultLimit)
				}
			}
			return ctx.Err()
		})
		if walkErr != nil && (errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded)) {
			return collected, truncated, reason, walkErr
		}
		if errs.count() >= errs.maxErrs {
			cappedScopes++
		}
	}
	if !truncated && cappedScopes > 0 {
		truncated = true
		reason = inspectTruncWalkErrors
	}

	sort.Slice(collected, func(i, j int) bool {
		if collected[i].Path != collected[j].Path {
			return collected[i].Path < collected[j].Path
		}
		return collected[i].Line < collected[j].Line
	})
	return collected, truncated, reason, nil
}

// pendingMatch is a match awaiting its trailing "after" context lines.
type pendingMatch struct {
	line      int
	text      string
	before    []string
	after     []string
	needAfter int
}

// scanFileMatches scans one file for regex matches and returns each with its
// exact requested context window. It streams the file once: "before" context
// is a bounded ring buffer and "after" context is a bounded pending queue, so
// memory use does not grow with file size (unlike buffering whole-file
// content, which read_file's line-window path does for its own, different,
// random-access use case).
func (e *inspectEngine) scanFileMatches(ctx context.Context, path, rel string) []inspectResult {
	f, _, err := openRegularFile(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var before []string
	var queue []pendingMatch
	var out []inspectResult
	lineNo := 0

	flush := func(p pendingMatch) {
		ctxLines := make([]string, 0, len(p.before)+len(p.after))
		ctxLines = append(ctxLines, p.before...)
		ctxLines = append(ctxLines, p.after...)
		out = append(out, inspectResult{Path: rel, Line: p.line, Text: p.text, Context: ctxLines})
	}

	for sc.Scan() {
		if lineNo&0xff == 0 && ctx != nil && ctx.Err() != nil {
			break
		}
		lineNo++
		line := sc.Text()

		remaining := queue[:0]
		for _, p := range queue {
			if p.needAfter > 0 {
				p.after = append(p.after, line)
				p.needAfter--
			}
			if p.needAfter == 0 {
				flush(p)
			} else {
				remaining = append(remaining, p)
			}
		}
		queue = remaining

		if e.re.MatchString(line) {
			text := line
			if len(text) > 200 {
				text = truncateUTF8(text, 200) + "..."
			}
			p := pendingMatch{line: lineNo, text: text, before: append([]string(nil), before...), needAfter: e.contextLines}
			if p.needAfter == 0 {
				flush(p)
			} else {
				queue = append(queue, p)
			}
		}

		if e.contextLines > 0 {
			before = append(before, line)
			if len(before) > e.contextLines {
				before = before[1:]
			}
		}
	}
	for _, p := range queue {
		flush(p)
	}
	return out
}
