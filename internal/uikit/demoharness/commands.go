package demoharness

import (
	"context"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// demoModels is the fake model roster /model offers. The first entry is
// the model a new Harness starts on.
var demoModels = []string{"mivia-fast", "mivia-standard", "mivia-deep"}

// demoAgent is one row of the fake agent roster /agents lists.
type demoAgent struct {
	Name string
	Role string
}

var demoAgents = []demoAgent{
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
		return ports.CommandOutcome{Notice: formatAgents(demoAgents)}
	case "clear":
		h.mu.Lock()
		h.history = nil
		h.mu.Unlock()
		return ports.CommandOutcome{ClearTranscript: true}
	case "compact":
		return ports.CommandOutcome{Notice: h.compact()}
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

// SelectModel applies a /model choice. An unlisted name is an error:
// the picker only ever offers names from demoModels, so this path is
// reached only if a future caller invents a name the roster does not
// have.
func (h *Harness) SelectModel(_ context.Context, name string) ports.CommandOutcome {
	for _, m := range demoModels {
		if m == name {
			h.mu.Lock()
			h.model = ports.ModelInfo{Name: name, Provider: "demo"}
			h.mu.Unlock()
			return ports.CommandOutcome{Notice: "model set to " + name}
		}
	}
	return ports.CommandOutcome{Err: "unknown model " + name}
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
