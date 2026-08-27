package cliagents

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// maxLoadToolsErrorCandidates bounds how many valid names an error lists back.
const maxLoadToolsErrorCandidates = 20

// maxLoadToolsErrorUnknown bounds how many rejected names the error echoes.
const maxLoadToolsErrorUnknown = 20

// maxLoadToolsErrorNameLen caps a single echoed name.
const maxLoadToolsErrorNameLen = 64

// loadToolsTool lets the model pull a deferred tool's schema into the
// advertised surface. It is a privileged session tool; a nested agent must
// never reach it. It only records intent — publication happens at the turn
// boundary (plan tools/05 D6/F2).
// See cli/dispatcher.go for registration; see NewLoadToolsTool for construction.
type loadToolsTool struct {
	session    *chat.Session
	candidates []tools.TierCandidate
}

var (
	_ tools.Tool           = (*loadToolsTool)(nil)
	_ tools.PrivilegedTool = (*loadToolsTool)(nil)
	_ tools.CapableTool    = (*loadToolsTool)(nil)
)

// NewLoadToolsTool constructs a load_tools tool wired to sess.
// Pass nil, nil to build a schema-only instance for the tool catalog.
func NewLoadToolsTool(sess *chat.Session, candidates []tools.TierCandidate) tools.Tool {
	return &loadToolsTool{session: sess, candidates: candidates}
}

func (t *loadToolsTool) Name() string { return tools.LoadToolsToolName }

func (t *loadToolsTool) Description() string {
	return "Enable additional tools for execution. The tools listed as not currently loaded in your instructions are authorized and their schemas are visible to you, but they are locked: calling one directly before loading it is refused. Name them exactly with \"names\", or describe what you need with \"query\" to match against tool names and descriptions. Loaded tools become callable on your NEXT turn, not the current one: finish this turn, then call them. Calling a locked tool directly also queues it to load automatically, so you can just retry the call next turn instead."
}

func (t *loadToolsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"names": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"maxItems":    float64(tools.MaxAdmissionNamesPerCall),
				"description": "Exact names of tools to load.",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Case-insensitive substring matched against the names and descriptions of tools that are not loaded.",
			},
		},
		"required":             []string{},
		"additionalProperties": false,
	}
}

func (t *loadToolsTool) Privileged() {}

func (t *loadToolsTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "session:tool-surface"}
}

type loadToolsArgs struct {
	Names []string `json:"names"`
	Query string   `json:"query"`
}

func (t *loadToolsTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := t.session.ChargeAdmissionAttempt(); err != nil {
		return "", err
	}
	var args loadToolsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	requested, err := t.resolveRequested(args)
	if err != nil {
		return "", err
	}
	turnID, _ := chat.TurnIDFromContext(ctx)
	result, err := t.session.StageToolAdmission(requested, turnID)
	if err != nil {
		return "", err
	}
	return t.render(result), nil
}

// resolveRequested turns names and query into a deduplicated request list.
func (t *loadToolsTool) resolveRequested(args loadToolsArgs) ([]string, error) {
	known := make(map[string]struct{}, len(t.candidates))
	for _, candidate := range t.candidates {
		known[candidate.Name] = struct{}{}
	}
	selected := make(map[string]struct{}, len(args.Names))
	var unknown []string
	for _, name := range args.Names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
			continue
		}
		selected[name] = struct{}{}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("not loadable: %s. Loadable tools are: %s",
			boundedNameList(unknown), strings.Join(t.candidateNames(), ", "))
	}
	for _, name := range tools.MatchDeferred(args.Query, t.candidates) {
		selected[name] = struct{}{}
	}
	if len(selected) == 0 {
		if strings.TrimSpace(args.Query) != "" {
			return nil, fmt.Errorf("no loadable tool matches %q. Loadable tools are: %s",
				args.Query, strings.Join(t.candidateNames(), ", "))
		}
		return nil, fmt.Errorf("name at least one tool in \"names\" or describe one in \"query\". Loadable tools are: %s",
			strings.Join(t.candidateNames(), ", "))
	}
	var out []string
	for _, candidate := range t.candidates {
		if _, ok := selected[candidate.Name]; ok {
			out = append(out, candidate.Name)
		}
	}
	return out, nil
}

// boundedNameList renders model-supplied names for an error message.
func boundedNameList(names []string) string {
	var b strings.Builder
	for i, name := range names {
		if i == maxLoadToolsErrorUnknown {
			fmt.Fprintf(&b, ", ... (%d more)", len(names)-i)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		if len(name) > maxLoadToolsErrorNameLen {
			name = truncatePreviewUTF8(name, maxLoadToolsErrorNameLen) + "..."
		}
		fmt.Fprintf(&b, "%q", name)
	}
	return b.String()
}

func (t *loadToolsTool) candidateNames() []string {
	out := make([]string, 0, min(len(t.candidates), maxLoadToolsErrorCandidates))
	for _, candidate := range t.candidates {
		if len(out) == maxLoadToolsErrorCandidates {
			out = append(out, "...")
			break
		}
		out = append(out, candidate.Name)
	}
	return out
}

func (t *loadToolsTool) render(result chat.AdmissionStageResult) string {
	var b strings.Builder
	if len(result.Staged) > 0 {
		b.WriteString("loaded: ")
		b.WriteString(strings.Join(result.Staged, ", "))
		b.WriteString("\n")
		for _, name := range result.Staged {
			if line := t.describe(name); line != "" {
				b.WriteString("- ")
				b.WriteString(name)
				b.WriteString(": ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}
	if len(result.AlreadyStaged) > 0 {
		b.WriteString("already staged: ")
		b.WriteString(strings.Join(result.AlreadyStaged, ", "))
		b.WriteString("\n")
	}
	if len(result.Staged) > 0 || len(result.AlreadyStaged) > 0 {
		b.WriteString("These are available from your next turn, not this one. Publication happens at the turn boundary and can be deferred while other work is active. Finish this turn first.")
	}
	if len(result.AlreadyStaged) > 0 {
		if _, reason, ok := t.session.PendingAdmissionStatus(); ok && reason != "" {
			fmt.Fprintf(&b, " Publication is deferred because %s.", reason)
		}
		b.WriteString(" The list of not-loaded tools in your instructions is frozen from when this agent was bound and is NOT updated as tools load, so do not re-request these.")
	}
	if len(result.Already) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("already loaded: ")
		b.WriteString(strings.Join(result.Already, ", "))
		b.WriteString("\nThese are callable now. The list of not-loaded tools in your instructions is frozen from when this agent was bound and is NOT updated as tools load, so do not re-request these.")
	}
	return b.String()
}

func (t *loadToolsTool) describe(name string) string {
	idx := slices.IndexFunc(t.candidates, func(c tools.TierCandidate) bool { return c.Name == name })
	if idx < 0 {
		return ""
	}
	return t.candidates[idx].Description
}

// truncatePreviewUTF8 cuts s to at most maxBytes bytes on a valid UTF-8 boundary.
func truncatePreviewUTF8(s string, maxBytes int) string {
	if maxBytes >= len(s) {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	for maxBytes > 0 && !utf8.ValidString(s[:maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
