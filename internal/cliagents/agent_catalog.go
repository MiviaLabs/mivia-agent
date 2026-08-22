package cliagents

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

type AgentCatalogView struct {
	Report agents.InspectionReport
	Rows   []AgentCatalogRow
}

type AgentCatalogRow struct {
	Name        string
	Source      string
	State       string
	Tools       string
	Model       string
	Turns       string
	Description string
	// Limits renders the per-agent resource ceilings that bound an agent even
	// when its turns are unlimited.
	Limits string
}

func LoadAgentCatalog(workspaceRoot string) (AgentCatalogView, error) {
	catalogue, skillWarnings := BuildSkillCatalogue(workspaceRoot)
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
		return AgentCatalogView{}, fmt.Errorf("unable to inspect agent definitions")
	}
	report.Warnings = append(skillWarnings, report.Warnings...)
	view := AgentCatalogView{Report: report}
	for _, name := range report.Registry.Names() {
		a, ok := report.Registry.Get(name)
		if !ok {
			continue
		}
		view.Rows = append(view.Rows, AgentCatalogRow{
			Name: name, Source: string(a.Provenance.Source), State: "selectable",
			Tools: formatAgentTools(a.EffectiveTools), Model: formatAgentModel(a.Provider, a.Model), Turns: formatAgentTurns(a.MaxTurns), Limits: formatAgentLimits(a.TimeoutSeconds, a.MaxTokens),
			Description: agents.SanitizeDescription(a.Description),
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
		clean = append(clean, SafeCatalogText(name, 80))
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
		return SafeCatalogText(model, 200) + " (session provider)"
	}
	return SafeCatalogText(providerName, 64) + "/" + SafeCatalogText(model, 200)
}

// formatAgentLimits renders the ceilings that bound an agent independently of
// its turn count. "(inherit session)" means the agent declares none of its
// own, not that it is unbounded: the session's caps still apply.
func formatAgentLimits(timeoutSeconds, maxTokens *int) string {
	parts := []string{}
	if timeoutSeconds != nil {
		parts = append(parts, fmt.Sprintf("timeout %ds", *timeoutSeconds))
	}
	if maxTokens != nil {
		parts = append(parts, fmt.Sprintf("max_tokens %d", *maxTokens))
	}
	if len(parts) == 0 {
		return "(inherit session)"
	}
	return strings.Join(parts, ", ")
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

func WriteAgentCatalog(w io.Writer, view AgentCatalogView, diagnostics io.Writer) {
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

func writeAgentRow(w io.Writer, row AgentCatalogRow) {
	fmt.Fprintf(w, "  name: %s\n", row.Name)
	fmt.Fprintf(w, "  source: %s\n", row.Source)
	fmt.Fprintf(w, "  state: %s\n", row.State)
	fmt.Fprintf(w, "  tools: %s\n", row.Tools)
	fmt.Fprintf(w, "  model: %s\n", row.Model)
	fmt.Fprintf(w, "  turns: %s\n", row.Turns)
	fmt.Fprintf(w, "  limits: %s\n", row.Limits)
}

func writeAgentDiagnostics(w io.Writer, rows []config.AgentFileDiagnostic) {
	for _, row := range rows {
		if row.State == config.AgentFileLoaded {
			continue
		}
		fmt.Fprintf(w, "  diagnostic: %s (%s, %s)\n", SafeCatalogText(row.Name, 80), row.Source, row.State)
	}
}

func enabledDisabled(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

// agentJSONEntry is a JSON-serializable agent entry for the --json flag.
// Only safe, selectable fields are included.
type agentJSONEntry struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	State       string `json:"state"`
	Tools       string `json:"tools"`
	Model       string `json:"model"`
	Turns       string `json:"turns"`
	Limits      string `json:"limits"`
	Description string `json:"description"`
}

// writeAgentCatalogJSON encodes the selectable agent rows as a JSON array.
// Description is sourced from the resolved agent (pre-sanitized at resolve time
// by SanitizeDescription). All other fields use pre-formatted row strings from
// view.Rows (already processed through safeCatalogText). Returns nil error on
// success; caller should NOT emit partial output on error.
func writeAgentCatalogJSON(w io.Writer, view AgentCatalogView) error {
	entries := make([]agentJSONEntry, 0, len(view.Rows))
	for _, row := range view.Rows {
		desc := ""
		if view.Report.Registry != nil {
			if a, ok := view.Report.Registry.Get(row.Name); ok {
				desc = a.Description
			}
		}
		entries = append(entries, agentJSONEntry{
			Name:        row.Name,
			Source:      row.Source,
			State:       row.State,
			Tools:       row.Tools,
			Model:       row.Model,
			Turns:       row.Turns,
			Limits:      row.Limits,
			Description: desc,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(entries); err != nil {
		return fmt.Errorf("json encode failed: %w", err)
	}
	return nil
}

func findCatalogAgent(view AgentCatalogView, name string) (agents.ResolvedAgent, bool) {
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
	fmt.Fprintf(w, "  path: %s\n", SafeCatalogText(a.Provenance.Path, 240))
	fmt.Fprintf(w, "  parent_chain: %s\n", formatTraceChain(a.Trace.ParentChain))
	fmt.Fprintln(w, "  field_winners:")
	for _, field := range a.Trace.Fields {
		winner := string(field.Source)
		if winner == "" {
			winner = "session default"
		}
		if field.Path != "" {
			winner += " (" + SafeCatalogText(field.Path, 240) + ")"
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
		clean = append(clean, SafeCatalogText(name, 80))
	}
	return strings.Join(clean, " -> ")
}

func (v AgentCatalogView) ReportCollection() string {
	return string(v.ReportCollectionState())
}

func (v AgentCatalogView) ReportCollectionState() config.AgentCollectionState {
	return v.Report.Collection
}

func SafeCatalogText(value string, max int) string {
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
