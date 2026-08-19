package demoharness

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

//go:embed testdata/*.json
var testdataFS embed.FS

// turnScript is one scripted turn. Before plays in full, in order. When
// its last event is a tool.pending event, playback pauses for an
// approval decision and then plays OnApprove or OnDeny. A turn with no
// tool.pending leaves both empty: the data alone decides whether a turn
// needs a decision, not a code branch per turn.
type turnScript struct {
	Before    []uievent.Event `json:"before"`
	OnApprove []uievent.Event `json:"on_approve,omitempty"`
	OnDeny    []uievent.Event `json:"on_deny,omitempty"`
}

// scenarios maps a --scenario name to its ordered turn-script files.
// DefaultScenario is the one scenario that visits every turn shape the
// demo harness supports: small talk, a tool call, a diff, a failing
// tool, a plan, reasoning, a usage summary, and an approval.
var scenarios = map[string][]string{
	"full-tour": {
		"smalltalk.json", "tool_call.json", "diff.json", "tool_fail.json",
		"plan.json", "reasoning.json", "usage.json", "approval.json",
	},
	"smalltalk": {"smalltalk.json"},
	"approval":  {"approval.json"},
}

// DefaultScenario names the scenario New uses when no --scenario flag
// is given.
const DefaultScenario = "full-tour"

// Scenarios lists every known --scenario name, sorted, for --help text
// and flag validation.
func Scenarios() []string {
	out := make([]string, 0, len(scenarios))
	for name := range scenarios {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// loadScenario reads and decodes every turn-script file for name, in
// order. An unknown name is an error naming the known set.
func loadScenario(name string) ([]turnScript, error) {
	files, ok := scenarios[name]
	if !ok {
		return nil, fmt.Errorf("demoharness: unknown scenario %q (known: %s)", name, strings.Join(Scenarios(), ", "))
	}
	out := make([]turnScript, 0, len(files))
	for _, f := range files {
		data, err := testdataFS.ReadFile("testdata/" + f)
		if err != nil {
			return nil, fmt.Errorf("demoharness: read %s: %w", f, err)
		}
		var ts turnScript
		if err := json.Unmarshal(data, &ts); err != nil {
			return nil, fmt.Errorf("demoharness: decode %s: %w", f, err)
		}
		out = append(out, ts)
	}
	return out, nil
}
