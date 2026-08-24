package demoharness

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// demoModels is the fake model roster /model offers. The first entry is
// the model a new Harness starts on.
var demoModels = []string{"mivia-fast", "mivia-standard", "mivia-deep"}

// demoContextWindow is the token capacity the demo model reports, so
// the cockpit's persistent context percentage has a denominator to
// divide by. 125k matches the wireframes' "62% (78k of 125k)" example
// scale.
const demoContextWindow int64 = 125_000

// demoAgent is one row of the fake agent roster /agents lists.
type demoAgent struct {
	Name string
	Role string
}

var demoAgents = []demoAgent{
	{Name: ports.DefaultAgentName, Role: "general purpose orchestrator (default)"},
	{Name: "go-engineer", Role: "implements Go changes"},
	{Name: "reviewer", Role: "reviews structure and architecture"},
	{Name: "auditor", Role: "hunts bugs in changed code"},
}

// compactRatio is how much /compact shrinks the recorded token counts.
// A quarter is a plausible compaction result for demo purposes; it is
// not a claim about any real compaction algorithm's ratio.
const compactRatio = 4

// Run executes one slash command. Every branch here is data lookup or
// simple arithmetic over Harness state; no branch calls out to another
// system, which is what keeps this fake usable with no network and no
// process.
func (h *Harness) Run(_ context.Context, name, args string) ports.CommandOutcome {
	switch name {
	case "theme":
		return ports.CommandOutcome{OpenTheme: true}
	case "help":
		return ports.CommandOutcome{OpenHelp: true}
	case "model":
		return ports.CommandOutcome{ModelChoices: append([]string(nil), demoModels...)}
	case "context":
		return ports.CommandOutcome{Notice: formatContext(h.ContextUsage())}
	case "cost":
		return ports.CommandOutcome{Notice: formatCost(h.ContextUsage())}
	case "agents":
		names := make([]string, 0, len(demoAgents))
		for _, a := range demoAgents {
			names = append(names, a.Name)
		}
		return ports.CommandOutcome{AgentChoices: names}
	case "resume":
		return ports.CommandOutcome{SessionChoices: append([]ports.SessionSummary(nil), demoSessions...)}
	case "clear":
		h.mu.Lock()
		h.history = nil
		h.mu.Unlock()
		return ports.CommandOutcome{ClearTranscript: true}
	case "compact":
		return ports.CommandOutcome{Notice: h.compact()}
	case "settings":
		return ports.CommandOutcome{OpenSettings: true, SettingsSection: args}
	case "quit":
		return ports.CommandOutcome{Quit: true}
	default:
		label := "/" + name
		if args != "" {
			label += " " + args
		}
		return ports.CommandOutcome{Err: "unknown command " + label}
	}
}

// demoSessions is the fake session list /resume offers.
var demoSessions = []ports.SessionSummary{
	{
		ID:            "sess-1",
		Title:         "Cockpit Feature Tour",
		UpdatedAt:     time.Now().Add(-2 * time.Minute),
		Active:        true,
		State:         "running",
		Turns:         6,
		ContextTokens: 41_000,
		Lines: []string{
			"> explore the new cockpit interface",
			"Starting the interactive tour of cockpit components...",
			"◈ running tool: spawn_agent (scout)",
			"  [step 2/3] listed 14 exported constants",
			"Streaming summary of UI thresholds and responsive layout...",
		},
	},
	{
		ID:            "sess-2",
		Title:         "Refactor Config Constant",
		UpdatedAt:     time.Now().Add(-25 * time.Minute),
		Active:        false,
		State:         "done",
		Turns:         3,
		ContextTokens: 18_400,
		Lines: []string{
			"> refactor DemoScenarioPace to defaults.go",
			"Moved DemoScenarioPace constant to internal/uikit/config/defaults.go.",
			"✓ edited internal/uikit/config/defaults.go (+14 -2)",
			"All unit and invariant tests passing.",
		},
	},
	{
		ID:            "sess-3",
		Title:         "Delete Cache Directory",
		UpdatedAt:     time.Now().Add(-2 * time.Hour),
		Active:        false,
		State:         "done",
		Turns:         2,
		ContextTokens: 8_100,
		Lines: []string{
			"> clean up temporary build artifacts in .cache",
			"Found 18 stale cache entries totaling 42 MB.",
			"✓ removed directory .cache/build (18 files deleted)",
			"Cache cleanup completed successfully.",
		},
	},
	{
		ID:            "sess-4",
		Title:         "Morning Check-in",
		UpdatedAt:     time.Now().Add(-24 * time.Hour),
		Active:        false,
		State:         "done",
		Turns:         1,
		ContextTokens: 2_300,
		Lines: []string{
			"> what tasks are pending for today?",
			"Reviewed open PRs and issues across the repository.",
			"1. Review cockpit breadcrumb and session restore PR.",
			"2. Run verification suite on release candidate.",
		},
	},
}

// SelectSession applies a /resume choice.
func (h *Harness) SelectSession(_ context.Context, id string) ports.CommandOutcome {
	for _, s := range demoSessions {
		if s.ID == id {
			h.mu.Lock()
			h.title = s.Title
			h.mu.Unlock()
			return ports.CommandOutcome{Notice: "resumed session: " + s.Title}
		}
	}
	return ports.CommandOutcome{Err: "unknown session " + id}
}

// SelectModel applies a /model choice. An unlisted name is an error:
// the picker only ever offers names from demoModels, so this path is
// reached only if a future caller invents a name the roster does not
// have.
func (h *Harness) SelectModel(_ context.Context, name string) ports.CommandOutcome {
	for _, m := range demoModels {
		if m == name {
			h.mu.Lock()
			h.model = ports.ModelInfo{Name: name, Provider: "demo", ContextWindow: demoContextWindow}
			h.mu.Unlock()
			return ports.CommandOutcome{Notice: "model set to " + name}
		}
	}
	return ports.CommandOutcome{Err: "unknown model " + name}
}

// SelectAgent applies a /agents choice. The switch is session state the
// demo records but nothing else reads yet - the same shape /model had
// before the top bar started showing it.
func (h *Harness) SelectAgent(_ context.Context, name string) ports.CommandOutcome {
	for _, a := range demoAgents {
		if a.Name == name {
			h.mu.Lock()
			h.agent = name
			h.mu.Unlock()
			return ports.CommandOutcome{Notice: "agent set to " + name}
		}
	}
	return ports.CommandOutcome{Err: "unknown agent " + name}
}

// compact halves - by compactRatio - the recorded token counts and
// reports the before/after numbers. It mutates Harness state, so a
// later /context or /cost shows the reduced totals.
func (h *Harness) compact() string {
	h.mu.Lock()
	before := h.usage
	h.usage.InputTokens /= compactRatio
	h.usage.OutputTokens /= compactRatio
	h.usage.CachedTokens = 0
	after := h.usage
	h.mu.Unlock()
	return fmt.Sprintf(
		"Compacted the context. Tokens went from %d in / %d out to %d in / %d out.",
		before.InputTokens, before.OutputTokens, after.InputTokens, after.OutputTokens,
	)
}

func formatContext(u ports.Usage) string {
	return fmt.Sprintf("Context usage: %d input tokens, %d output tokens, %d cached tokens.",
		u.InputTokens, u.OutputTokens, u.CachedTokens)
}

func formatCost(u ports.Usage) string {
	return fmt.Sprintf("Session cost so far: $%.3f.", u.CostUSD)
}

func formatAgents(agents []demoAgent) string {
	lines := make([]string, 0, len(agents)+1)
	lines = append(lines, "Available agents:")
	for _, a := range agents {
		lines = append(lines, fmt.Sprintf("  %s - %s", a.Name, a.Role))
	}
	return strings.Join(lines, "\n")
}
