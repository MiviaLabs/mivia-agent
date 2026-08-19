package demoharness

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
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
	Title     string          `json:"title,omitempty"`
	Before    []uievent.Event `json:"before"`
	OnApprove []uievent.Event `json:"on_approve,omitempty"`
	OnDeny    []uievent.Event `json:"on_deny,omitempty"`

	// Subagents are the dispatched agents this turn's script carries:
	// the panel's subagents section feeds from the progress events in
	// Before, and each entry here backs a live thread Conversation
	// (Thread below) with its prior history and its scripted reply.
	Subagents []subagentFixture `json:"subagents,omitempty"`
}

// subagentFixture is one scripted subagent thread: the history it had
// when the dispatching turn ended, and the reply a Send into the thread
// streams back. Simple by design - the fixture proves the wiring, it
// does not model intelligence.
type subagentFixture struct {
	CallID  string          `json:"call_id"`
	History []ports.Message `json:"history,omitempty"`
	Reply   string          `json:"reply"`
}

type scenarioDef struct {
	Title string
	Files []string
}

type loadedScenario struct {
	Title     string
	Scripts   []turnScript
	Subagents map[string]subagentFixture
}

// scenarios maps a --scenario name to its ordered turn-script files and title.
// DefaultScenario is the one scenario that visits every turn shape the
// demo harness supports: small talk, a tool call, a diff, a failing
// tool, a plan, reasoning, a usage summary, an approval, and a
// diff-previewing approval.
var scenarios = map[string]scenarioDef{
	"full-tour": {
		Title: "Cockpit Feature Tour",
		Files: []string{
			"smalltalk.json", "tool_call.json", "diff.json", "tool_fail.json",
			"plan.json", "reasoning.json", "usage.json", "approval.json",
			"approval_diff.json", "delete.json",
		},
	},
	"smalltalk": {
		Files: []string{"smalltalk.json"},
	},
	"approval": {
		Files: []string{"approval.json"},
	},
	"approval-diff": {
		Files: []string{"approval_diff.json"},
	},
	"subagent": {
		Files: []string{"subagent.json"},
	},
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
func loadScenario(name string) (loadedScenario, error) {
	scen, ok := scenarios[name]
	if !ok {
		return loadedScenario{}, fmt.Errorf("demoharness: unknown scenario %q (known: %s)", name, strings.Join(Scenarios(), ", "))
	}
	scripts := make([]turnScript, 0, len(scen.Files))
	var subagents map[string]subagentFixture
	for _, f := range scen.Files {
		data, err := testdataFS.ReadFile("testdata/" + f)
		if err != nil {
			return loadedScenario{}, fmt.Errorf("demoharness: read %s: %w", f, err)
		}
		var ts turnScript
		if err := json.Unmarshal(data, &ts); err != nil {
			return loadedScenario{}, fmt.Errorf("demoharness: decode %s: %w", f, err)
		}
		scripts = append(scripts, ts)
		if len(ts.Subagents) > 0 {
			if subagents == nil {
				subagents = map[string]subagentFixture{}
			}
			for _, sa := range ts.Subagents {
				subagents[sa.CallID] = sa
			}
		}
	}
	title := scen.Title
	if title == "" && len(scripts) > 0 && scripts[0].Title != "" {
		title = scripts[0].Title
	}
	if title == "" {
		title = name
	}
	return loadedScenario{Title: title, Scripts: scripts, Subagents: subagents}, nil
}
