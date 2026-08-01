package cli

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

type agentCatalogView struct {
	Report agents.InspectionReport
	Rows   []agentCatalogRow
}

type agentCatalogRow struct {
	Name   string
	Source string
	State  string
	Tools  string
	Model  string
	Turns  string
}

func loadAgentCatalog(workspaceRoot string) (agentCatalogView, error) {
	catalogue, skillWarnings := buildSkillCatalogue(workspaceRoot)
	globalPreview, _ := config.LoadAgentsGlobal(workspaceRoot)
	skillNames := make(map[string]struct{}, len(catalogue))
	for name, entry := range catalogue {
		if !globalPreview.LoadWorkspaceConfig && !entry.User {
			continue
		}
		skillNames[name] = struct{}{}
	}
	report, err := agents.Inspect(workspaceRoot, agents.LoadResolveOptions{
		SkillNames: skillNames, SkillCatalogue: catalogue,
	})
	if err != nil {
		return agentCatalogView{}, fmt.Errorf("unable to inspect agent definitions")
	}
	report.Warnings = append(skillWarnings, report.Warnings...)
	view := agentCatalogView{Report: report}
	for _, name := range report.Registry.Names() {
		a, ok := report.Registry.Get(name)
		if !ok {
			continue
		}
		view.Rows = append(view.Rows, agentCatalogRow{
			Name: name, Source: string(a.Provenance.Source), State: "selectable",
			Tools: formatAgentTools(a.EffectiveTools), Model: formatAgentModel(a.Provider, a.Model), Turns: formatAgentTurns(a.MaxTurns),
		})
	}
	sort.Slice(view.Rows, func(i, j int) bool { return view.Rows[i].Name < view.Rows[j].Name })
	return view, nil
}

func formatAgentTools(tools []string) string {
	if len(tools) == 0 {
		return "(none)"
	}
	clean := make([]string, 0, len(tools))
	for _, name := range tools {
		clean = append(clean, safeCatalogText(name, 80))
	}
	return strings.Join(clean, ",")
}

// formatAgentModel renders the resolved execution binding. A provider is shown
// qualified so the operator can tell a session-local model choice apart from
// one that routes to a different vendor entirely.
func formatAgentModel(providerName, model string) string {
	if strings.TrimSpace(model) == "" {
		return "(inherit session)"
	}
	if strings.TrimSpace(providerName) == "" {
		return safeCatalogText(model, 200) + " (session provider)"
	}
	return safeCatalogText(providerName, 64) + "/" + safeCatalogText(model, 200)
}

func formatAgentTurns(turns *int) string {
	if turns == nil {
		return "(inherit session)"
	}
	if *turns == 0 {
		return "unlimited"
	}
	return strconv.Itoa(*turns)
}

func writeAgentCatalog(w io.Writer, view agentCatalogView, diagnostics io.Writer) {
	fmt.Fprintln(w, "agents:")
	fmt.Fprintf(w, "  collection: %s\n", view.ReportCollection())
	if len(view.Rows) == 0 {
		fmt.Fprintln(w, "  name: (none)")
		fmt.Fprintln(w, "  source: (none)")
		fmt.Fprintln(w, "  state: no definitions")
		fmt.Fprintln(w, "  tools: (none)")
		fmt.Fprintln(w, "  model: (none)")
		fmt.Fprintln(w, "  turns: (none)")
	} else {
		for _, row := range view.Rows {
			writeAgentRow(w, row)
		}
	}
	fmt.Fprintln(w, "  name: root fallback")
	fmt.Fprintln(w, "  source: compiled")
	fmt.Fprintln(w, "  state: fallback (not selectable)")
	fmt.Fprintln(w, "  tools: session defaults")
	fmt.Fprintln(w, "  model: session binding")
	fmt.Fprintln(w, "  turns: session default")
	fmt.Fprintln(w, "workspace agent files: always loaded")
	fmt.Fprintf(w, "workspace prompts/project skills: %s\n", enabledDisabled(view.Report.Global.LoadWorkspaceConfig))
	if diagnostics == nil {
		diagnostics = w
	}
	writeAgentDiagnostics(diagnostics, view.Report.Diagnostics)
}

func writeAgentRow(w io.Writer, row agentCatalogRow) {
	fmt.Fprintf(w, "  name: %s\n", row.Name)
	fmt.Fprintf(w, "  source: %s\n", row.Source)
	fmt.Fprintf(w, "  state: %s\n", row.State)
	fmt.Fprintf(w, "  tools: %s\n", row.Tools)
	fmt.Fprintf(w, "  model: %s\n", row.Model)
	fmt.Fprintf(w, "  turns: %s\n", row.Turns)
}

func writeAgentDiagnostics(w io.Writer, rows []config.AgentFileDiagnostic) {
	for _, row := range rows {
		if row.State == config.AgentFileLoaded {
			continue
		}
		fmt.Fprintf(w, "  diagnostic: %s (%s, %s)\n", safeCatalogText(row.Name, 80), row.Source, row.State)
	}
}

func enabledDisabled(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func findCatalogAgent(view agentCatalogView, name string) (agents.ResolvedAgent, bool) {
	if view.Report.Registry == nil {
		return agents.ResolvedAgent{}, false
	}
	return view.Report.Registry.Get(strings.TrimSpace(name))
}

func writeAgentExplain(w io.Writer, a agents.ResolvedAgent) {
	fmt.Fprintln(w, "agent:")
	fmt.Fprintf(w, "  name: %s\n", a.Name)
	fmt.Fprintf(w, "  source: %s\n", a.Provenance.Source)
	fmt.Fprintln(w, "  state: selectable")
	fmt.Fprintf(w, "  path: %s\n", safeCatalogText(a.Provenance.Path, 240))
	fmt.Fprintf(w, "  parent_chain: %s\n", formatTraceChain(a.Trace.ParentChain))
	fmt.Fprintln(w, "  field_winners:")
	for _, field := range a.Trace.Fields {
		winner := string(field.Source)
		if winner == "" {
			winner = "session default"
		}
		if field.Path != "" {
			winner += " (" + safeCatalogText(field.Path, 240) + ")"
		}
		if field.Name == "prompt" {
			winner += "; present=" + strconv.FormatBool(field.ValuePresent)
		}
		fmt.Fprintf(w, "    %s: %s\n", field.Name, winner)
	}
	fmt.Fprintln(w, "  tool_operations:")
	if len(a.Trace.ToolOperations) == 0 {
		fmt.Fprintln(w, "    (none)")
	}
	for _, operation := range a.Trace.ToolOperations {
		fmt.Fprintf(w, "    %s: %s\n", operation.Kind, formatAgentTools(operation.Tools))
	}
	fmt.Fprintf(w, "  guardrail_removals: %s\n", formatAgentTools(a.Trace.GuardrailRemovals))
	fmt.Fprintf(w, "  effective_denylist: %s\n", formatAgentTools(a.Trace.EffectiveDenylist))
	fmt.Fprintf(w, "  skill_scope: %s\n", a.Trace.SkillScope)
	fmt.Fprintf(w, "  skill_allowlist: %s\n", formatAgentTools(a.Trace.SkillNames))
}

func formatTraceChain(chain []string) string {
	if len(chain) == 0 {
		return "(none)"
	}
	clean := make([]string, 0, len(chain))
	for _, name := range chain {
		clean = append(clean, safeCatalogText(name, 80))
	}
	return strings.Join(clean, " -> ")
}

func (v agentCatalogView) ReportCollection() string {
	return string(v.ReportCollectionState())
}

func (v agentCatalogView) ReportCollectionState() config.AgentCollectionState {
	return v.Report.Collection
}

func safeCatalogText(value string, max int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return value
}
