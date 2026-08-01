package agents

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// InspectionReport is the provider-independent catalog projection input. It
// contains all independently readable definitions and safe file-state rows.
type InspectionReport struct {
	Registry    *AgentRegistry
	Global      config.AgentsGlobal
	Collection  config.AgentCollectionState
	Diagnostics []config.AgentFileDiagnostic
	Warnings    []string
}

// Inspect discovers and resolves as much of the agent collection as possible.
// Resolution failures are attached to the affected file as malformed rows;
// unrelated valid definitions remain selectable in the returned registry.
func Inspect(workspaceRoot string, o LoadResolveOptions) (InspectionReport, error) {
	global, err := config.LoadAgentsGlobal(workspaceRoot)
	if err != nil {
		return InspectionReport{}, err
	}
	discovered, err := config.DiscoverAgentFilesReport(workspaceRoot, global.LoadWorkspaceConfig)
	if err != nil {
		return InspectionReport{Global: global, Collection: discovered.Collection, Diagnostics: discovered.Diagnostics, Warnings: discovered.Warnings}, err
	}
	if o.SkillCatalogue != nil {
		o.AllowProjectSkills = global.LoadWorkspaceConfig
	}
	opts := ResolveOptions{
		Global:             global,
		KnownTools:         knownToolSet(tools.AllToolNames()),
		SkillNames:         o.SkillNames,
		ReservedHandlers:   subagents.ReservedHandlerNames(),
		SkillCatalogue:     o.SkillCatalogue,
		AllowProjectSkills: o.AllowProjectSkills,
	}
	inputs := make([]ResolveInput, 0, len(discovered.Files))
	for _, file := range discovered.Files {
		inputs = append(inputs, ResolveInput{Name: file.Name, Source: file.Source, Path: file.Path, Spec: file.Spec})
	}
	byName, err := indexInputs(inputs)
	if err != nil {
		return InspectionReport{Global: global, Collection: discovered.Collection, Diagnostics: discovered.Diagnostics, Warnings: discovered.Warnings}, err
	}
	state := &resolveState{byName: byName, opts: opts, resolved: map[string]ResolvedAgent{}, visiting: map[string]bool{}}
	registry := NewRegistry()
	diagnostics := append([]config.AgentFileDiagnostic(nil), discovered.Diagnostics...)
	for _, name := range orderedNames(byName) {
		resolved, resolveErr := state.resolveOne(name)
		if resolveErr != nil {
			replaceInspectionRow(&diagnostics, config.AgentFileDiagnostic{
				Name: name, Source: byName[name].Source, Path: byName[name].Path, State: config.AgentFileMalformed,
			})
			continue
		}
		if publishErr := registry.Publish(resolved); publishErr != nil {
			return InspectionReport{}, publishErr
		}
	}
	warnings := append(append([]string{}, global.Warnings...), discovered.Warnings...)
	warnings = append(warnings, state.warnings...)
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].Name != diagnostics[j].Name {
			return diagnostics[i].Name < diagnostics[j].Name
		}
		return diagnostics[i].Path < diagnostics[j].Path
	})
	sort.Strings(warnings)
	return InspectionReport{Registry: registry, Global: global, Collection: discovered.Collection, Diagnostics: diagnostics, Warnings: warnings}, nil
}

func replaceInspectionRow(rows *[]config.AgentFileDiagnostic, replacement config.AgentFileDiagnostic) {
	for i, row := range *rows {
		if row.Name == replacement.Name && row.Path == replacement.Path {
			(*rows)[i] = replacement
			return
		}
	}
	*rows = append(*rows, replacement)
}

// DiagnosticSummary returns a bounded class/count summary suitable for errors.
func (r InspectionReport) DiagnosticSummary() string {
	counts := map[config.AgentFileState]int{}
	for _, row := range r.Diagnostics {
		if row.State == config.AgentFileLoaded {
			continue
		}
		counts[row.State]++
	}
	parts := make([]string, 0, len(counts))
	for state, count := range counts {
		parts = append(parts, fmt.Sprintf("%d %s", count, state))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
