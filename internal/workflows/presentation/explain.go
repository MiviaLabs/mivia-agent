package presentation

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// FormatWorkflowExplain formats a compiled workflow as an explanatory view
// showing the state graph, loop caps, delivery policy, resolved references,
// and declared authority. No secret values or transition coverage analysis
// (deferred to Phase 4 matcher).
func FormatWorkflowExplain(cw *CompiledWorkflowExplain) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Workflow: %s (v%d)\n", cw.Name, cw.Version))
	if cw.Description != "" {
		b.WriteString(fmt.Sprintf("  %s\n", cw.Description))
	}
	b.WriteString(fmt.Sprintf("Digest: %s\n", cw.Digest))

	// State graph
	b.WriteString("\n── State Graph ──\n")
	formatStateGraph(&b, cw)

	// Loop caps
	if len(cw.LoopNames) > 0 {
		b.WriteString("\n── Loop Caps ──\n")
		sort.Strings(cw.LoopNames)
		formatLoopCaps(&b, cw)
	}

	// Declared authority (agents referenced by agent/agent_gate steps)
	if len(cw.Agents) > 0 {
		b.WriteString("\n── Declared Authority ──\n")
		sort.Strings(cw.Agents)
		for _, a := range cw.Agents {
			fmt.Fprintf(&b, "  agent: %s\n", a)
		}
	}

	// Resolved references
	if len(cw.References) > 0 {
		b.WriteString("\n── References ──\n")
		sort.Strings(cw.References)
		for _, r := range cw.References {
			fmt.Fprintf(&b, "  %s\n", r)
		}
	}

	// Delivery policy
	if cw.Delivery != nil && cw.Delivery.Kind != "" {
		b.WriteString("\n── Delivery ──\n")
		fmt.Fprintf(&b, "  kind:    %s\n", cw.Delivery.Kind)
		fmt.Fprintf(&b, "  mode:    %s\n", cw.Delivery.Mode)
		fmt.Fprintf(&b, "  provider: %s\n", cw.Delivery.Provider)
		fmt.Fprintf(&b, "  base:    %s\n", cw.Delivery.Base)
	}

	// Limits
	if cw.MaxStepAttempts > 0 || cw.MaxDurationSeconds > 0 {
		b.WriteString("\n── Limits ──\n")
		if cw.MaxStepAttempts > 0 {
			fmt.Fprintf(&b, "  max_step_attempts:    %d\n", cw.MaxStepAttempts)
		}
		if cw.MaxDurationSeconds > 0 {
			fmt.Fprintf(&b, "  max_duration_seconds: %d\n", cw.MaxDurationSeconds)
		}
	}

	return b.String()
}

// CompiledWorkflowExplain holds the data needed for the explain presentation.
// It avoids exposing the full CompiledWorkflow to the presentation layer.
type CompiledWorkflowExplain struct {
	Name               string
	Description        string
	Version            int
	Digest             string
	Steps              []definition.Step
	Transitions        []definition.Transition
	LoopNames          []string
	Agents             []string
	References         []string
	InitialStep        string
	Delivery           *definition.Delivery
	MaxStepAttempts    int
	MaxDurationSeconds int
}

// formatStateGraph renders the step→transition→target graph.
func formatStateGraph(b *strings.Builder, cw *CompiledWorkflowExplain) {
	fromTransitions := make(map[string][]definition.Transition)
	for _, t := range cw.Transitions {
		fromTransitions[t.From] = append(fromTransitions[t.From], t)
	}

	for _, s := range cw.Steps {
		marker := " "
		if s.ID == cw.InitialStep {
			marker = "→"
		}
		fmt.Fprintf(b, "%s [%s] %s\n", marker, s.Kind, s.ID)

		transitions := fromTransitions[s.ID]
		for _, t := range transitions {
			matchParts := []string{fmt.Sprintf("status=%s", t.Match.Status)}
			for k, v := range t.Match.Output {
				matchParts = append(matchParts, fmt.Sprintf("%s=%s", k, v))
			}
			loopInfo := ""
			if t.Loop != "" {
				loopInfo = fmt.Sprintf(" (loop=%s, max %d)", t.Loop, t.MaxIterations)
			}
			fmt.Fprintf(b, "    ├─ when %s → %s%s\n", strings.Join(matchParts, ", "), t.To, loopInfo)
		}
	}
}

// formatLoopCaps lists named loops with their max_iterations.
func formatLoopCaps(b *strings.Builder, cw *CompiledWorkflowExplain) {
	seen := make(map[string]bool)
	for _, t := range cw.Transitions {
		if t.Loop != "" && !seen[t.Loop] {
			fmt.Fprintf(b, "  %s: max %d iterations\n", t.Loop, t.MaxIterations)
			seen[t.Loop] = true
		}
	}
}
