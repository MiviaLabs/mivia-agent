package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// maxLoadToolsErrorCandidates bounds how many valid names an error lists back.
// The point is to redirect a wrong guess, not to re-send the whole index.
const maxLoadToolsErrorCandidates = 20

// loadToolsTool lets the model pull a deferred tool's schema into the advertised
// surface. It is a privileged session tool: it mutates the session's own tool
// surface, so a nested agent must never reach it.
//
// It only records intent. The tool executes inside a turn whose loop already
// hoisted its tool list and whose dispatcher is executing this very batch, so
// widening now would either be invisible or would close the dispatcher running
// the call (plan tools/05 D6/F2). The publication happens at the turn boundary
// and the result says so plainly.
type loadToolsTool struct {
	session *chat.Session
	// candidates is the binding's frozen deferred set. It is the authority for
	// what this tool may stage: a name outside it is refused, so admission can
	// never widen past the agent's effective tools.
	candidates []tools.TierCandidate
}

var (
	_ tools.Tool           = (*loadToolsTool)(nil)
	_ tools.PrivilegedTool = (*loadToolsTool)(nil)
	_ tools.CapableTool    = (*loadToolsTool)(nil)
)

func (t *loadToolsTool) Name() string { return tools.LoadToolsToolName }

func (t *loadToolsTool) Description() string {
	return "Load additional tools into your tool surface. The tools listed as not currently loaded in your instructions are authorized but their schemas are withheld to keep each request small. Name them exactly with \"names\", or describe what you need with \"query\" to match against tool names and descriptions. Loaded tools become callable on your NEXT turn, not the current one: finish this turn, then call them."
}

func (t *loadToolsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"names": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
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
	// Staging is an in-memory session-state write; it touches no workspace
	// resource and needs no scheduling key.
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "session:tool-surface"}
}

type loadToolsArgs struct {
	Names []string `json:"names"`
	Query string   `json:"query"`
}

func (t *loadToolsTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	// Charged before anything can reject the call. The bound exists to stop a
	// model looping on load_tools, and a model that loops does so on names it
	// keeps getting wrong - the calls that never reach staging at all.
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
	// The stage belongs to the turn EXECUTING this call, which under force-send
	// is not the session's current turn: a superseding turn has already bumped
	// that. Ownership decides which boundary may discard the stage, so it has
	// to come from the dispatcher's caller frame.
	turnID, _ := chat.TurnIDFromContext(ctx)
	result, err := t.session.StageToolAdmission(requested, turnID)
	if err != nil {
		return "", err
	}
	return t.render(result), nil
}

// resolveRequested turns names and query into a deduplicated request list in
// deferred-set order. Unknown or unauthorized names are refused outright rather
// than silently dropped: a model that misremembered a name must see that.
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
			strings.Join(unknown, ", "), strings.Join(t.candidateNames(), ", "))
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
	// Emit in deferred-set order so the same request always stages the same
	// sequence, which keeps the resulting registry deterministic.
	var out []string
	for _, candidate := range t.candidates {
		if _, ok := selected[candidate.Name]; ok {
			out = append(out, candidate.Name)
		}
	}
	return out, nil
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
	// Staged-again names sit with the newly staged ones, not with the loaded
	// ones: both become callable at the same boundary, and saying otherwise
	// sends the model into an unknown-tool failure (plan tools/05 D6).
	if len(result.AlreadyStaged) > 0 {
		b.WriteString("already staged: ")
		b.WriteString(strings.Join(result.AlreadyStaged, ", "))
		b.WriteString("\n")
	}
	if len(result.Staged) > 0 || len(result.AlreadyStaged) > 0 {
		b.WriteString("These are available from your next turn, not this one. Finish this turn first.")
	}
	if len(result.AlreadyStaged) > 0 {
		b.WriteString(" The list of not-loaded tools in your instructions is frozen from when this agent was bound and is NOT updated as tools load, so do not re-request these.")
	}
	if len(result.Already) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("already loaded: ")
		b.WriteString(strings.Join(result.Already, ", "))
		// The index in the instructions is frozen at bind time (plan tools/05
		// D8) and still lists these as not loaded. Saying so here is the only
		// way the model learns the list is not the record of what it has.
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
