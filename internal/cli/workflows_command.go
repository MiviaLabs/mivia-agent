package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/presentation"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// runWorkflows handles the workflow CLI commands.
func runWorkflows(args []string) error {
	return runWorkflowsWithIO(args, os.Stdout, os.Stderr)
}

func runWorkflowsWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("workflows: expected list, show, validate, or explain")
	}
	subcommand := args[0]
	remaining := args[1:]

	switch subcommand {
	case "list":
		return runWorkflowsList(remaining, stdout, stderr)
	case "show":
		return runWorkflowsShow(remaining, stdout, stderr)
	case "validate":
		return runWorkflowsValidate(remaining, stdout, stderr)
	case "explain":
		return runWorkflowsExplain(remaining, stdout, stderr)
	default:
		return fmt.Errorf("workflows: unknown subcommand %q (try list, show, validate, explain)", subcommand)
	}
}

func parseWorkspaceFlag(args []string) (workspaceRoot string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--workspace" && i+1 < len(args):
			workspaceRoot = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--workspace="):
			workspaceRoot = strings.TrimPrefix(args[i], "--workspace=")
		default:
			rest = append(rest, args[i])
		}
	}
	return
}

func runWorkflowsList(args []string, stdout, stderr io.Writer) error {
	workspaceRoot, _ := parseWorkspaceFlag(args)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = "."
	}
	workflows, err := definition.DiscoverWorkflows(workspaceRoot)
	if err != nil {
		return fmt.Errorf("workflows list: %w", err)
	}
	fmt.Fprint(stdout, presentation.FormatWorkflowList(workflows))
	return nil
}

func runWorkflowsShow(args []string, stdout, stderr io.Writer) error {
	workspaceRoot, positional := parseWorkspaceFlag(args)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = "."
	}
	if len(positional) != 1 {
		return fmt.Errorf("workflows show: expected exactly one workflow name")
	}
	name := positional[0]

	workflows, err := definition.DiscoverWorkflows(workspaceRoot)
	if err != nil {
		return fmt.Errorf("workflows show: %w", err)
	}

	var found *definition.DiscoveredWorkflow
	for i := range workflows {
		if workflows[i].Name == name {
			found = &workflows[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("workflows show: unknown workflow %q", name)
	}

	wf, _, err := definition.ParseWorkflowTOML(found.Raw, found.Name+".toml")
	if err != nil {
		return fmt.Errorf("workflows show: %w", err)
	}

	compiled, err := compiler.Compile(&wf)
	if err != nil {
		return fmt.Errorf("workflows show: %w", err)
	}

	fmt.Fprint(stdout, presentation.FormatWorkflowShow(compiled))
	return nil
}

func runWorkflowsValidate(args []string, stdout, stderr io.Writer) error {
	workspaceRoot, positional := parseWorkspaceFlag(args)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = "."
	}

	workflows, err := definition.DiscoverWorkflows(workspaceRoot)
	if err != nil {
		return fmt.Errorf("workflows validate: %w", err)
	}

	// If a name is given, validate only that workflow.
	targetName := ""
	if len(positional) > 0 {
		targetName = positional[0]
	}

	hasError := false
	for _, wf := range workflows {
		if targetName != "" && wf.Name != targetName {
			continue
		}
		parsed, _, err := definition.ParseWorkflowTOML(wf.Raw, wf.Name+".toml")
		if err != nil {
			fmt.Fprint(stdout, presentation.FormatWorkflowValidate(wf.Name, nil, err))
			hasError = true
			continue
		}
		compiled, err := compiler.Compile(&parsed)
		if err == nil {
			err = validateWorkflowReferences(workspaceRoot, filepath.Dir(wf.Path), compiled)
		}
		fmt.Fprint(stdout, presentation.FormatWorkflowValidate(wf.Name, compiled, err))
		if err != nil {
			hasError = true
		}
	}

	if !hasError && len(workflows) == 0 {
		fmt.Fprintln(stdout, "No workflows found.")
	}

	if hasError {
		return fmt.Errorf("workflows validate: one or more workflows are invalid")
	}
	return nil
}

// validateWorkflowReferences checks every external dependency that a workflow
// needs at admission. It never creates a run, worktree, provider, or command.
func validateWorkflowReferences(root, base string, wf *compiler.CompiledWorkflow) error {
	skillRegistry, err := loadChatSkills(root)
	if err != nil {
		return fmt.Errorf("load workflow skills: %w", err)
	}
	loaded, err := loadAgentDefinitions(root, "", skillRegistry)
	if err != nil {
		return fmt.Errorf("load workflow agents: %w", err)
	}
	if err := compiler.ValidateAgentSkillReferences(wf, loaded.Registry, skillRegistry); err != nil {
		return err
	}
	if err := validateWorkflowSkillTools(wf, loaded.Registry, skillRegistry); err != nil {
		return err
	}
	if err := validateWorkflowFiles(base, wf); err != nil {
		return err
	}
	if err := validateWorkflowVerifiers(wf); err != nil {
		return err
	}
	if policy, active := delivery.FromCompiled(wf); active {
		if err := policy.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowSkillTools(wf *compiler.CompiledWorkflow, registry *agents.AgentRegistry, skillRegistry *skills.Registry) error {
	for _, step := range wf.Steps {
		if step.Skill == "" {
			continue
		}
		agent, _ := registry.Get(step.Agent)
		skill, _ := skillRegistry.Get(step.Skill)
		if err := agents.CheckSkillInvocation(&agent, skill.Name, skill.Tools); err != nil {
			return fmt.Errorf("step %q: %w", step.ID, err)
		}
	}
	return nil
}

func validateWorkflowFiles(base string, wf *compiler.CompiledWorkflow) error {
	schemas := make(map[string][]byte)
	for _, step := range wf.Steps {
		if step.Template != "" {
			data, err := readWorkflowRef(base, step.Template, template.MaxTemplateBytes)
			if err != nil {
				return fmt.Errorf("step %q: template %q: %w", step.ID, step.Template, err)
			}
			if err := validateWorkflowTemplateBindings(step, string(data)); err != nil {
				return fmt.Errorf("step %q: template %q: %w", step.ID, step.Template, err)
			}
		}
		if step.OutputSchema != "" {
			data, err := readWorkflowRef(base, step.OutputSchema, compiler.MaxSchemaBytes)
			if err != nil {
				return fmt.Errorf("step %q: output_schema %q: %w", step.ID, step.OutputSchema, err)
			}
			schemas[step.OutputSchema] = data
		}
	}
	return compiler.ValidateSchemaReferenceBytes(&definition.WorkflowFile{Steps: wf.Steps}, schemas)
}

// validateWorkflowTemplateBindings verifies that each template reads only a
// value declared by its step context. It uses empty values because this check
// validates binding names, not runtime output.
func validateWorkflowTemplateBindings(step definition.Step, source string) error {
	inputs := make(map[string]any)
	evidence := make(map[string]any)
	for _, binding := range step.Context {
		if strings.HasPrefix(binding.From, "inputs.") {
			inputs[binding.As] = ""
			continue
		}
		if strings.HasPrefix(binding.From, "steps.") {
			evidence[binding.As] = ""
		}
	}
	_, err := template.Render(source, inputs, evidence, template.MaxTemplateBytes, template.MaxTemplateBytes)
	return err
}

func validateWorkflowVerifiers(wf *compiler.CompiledWorkflow) error {
	catalogue := verifier.DefaultCatalogue(secretpath.Policy{})
	for _, step := range wf.Steps {
		if step.Kind != "evidence_gate" {
			continue
		}
		if _, err := catalogue.Lookup(step.Verifier); err != nil {
			return fmt.Errorf("step %q: %w", step.ID, err)
		}
	}
	return nil
}

func runWorkflowsExplain(args []string, stdout, stderr io.Writer) error {
	workspaceRoot, positional := parseWorkspaceFlag(args)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = "."
	}
	if len(positional) != 1 {
		return fmt.Errorf("workflows explain: expected exactly one workflow name")
	}
	name := positional[0]

	workflows, err := definition.DiscoverWorkflows(workspaceRoot)
	if err != nil {
		return fmt.Errorf("workflows explain: %w", err)
	}

	var found *definition.DiscoveredWorkflow
	for i := range workflows {
		if workflows[i].Name == name {
			found = &workflows[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("workflows explain: unknown workflow %q", name)
	}

	wf, _, err := definition.ParseWorkflowTOML(found.Raw, found.Name+".toml")
	if err != nil {
		return fmt.Errorf("workflows explain: %w", err)
	}

	compiled, err := compiler.Compile(&wf)
	if err != nil {
		return fmt.Errorf("workflows explain: %w", err)
	}

	// Build explain data from compiled workflow.
	baseDir := filepath.Dir(found.Path)
	cw := buildExplainView(compiled, baseDir)

	fmt.Fprint(stdout, presentation.FormatWorkflowExplain(cw))
	return nil
}

// buildExplainView converts a CompiledWorkflow into a CompiledWorkflowExplain
// for the presentation layer.
func buildExplainView(c *compiler.CompiledWorkflow, baseDir string) *presentation.CompiledWorkflowExplain {
	cw := &presentation.CompiledWorkflowExplain{
		Name:               c.Name,
		Description:        c.Description,
		Version:            c.Version,
		Digest:             c.Digest,
		Steps:              c.Steps,
		Transitions:        c.Transitions,
		InitialStep:        c.InitialStep,
		Delivery:           c.Delivery,
		MaxStepAttempts:    c.Limits.MaxStepAttempts,
		MaxDurationSeconds: c.Limits.MaxDurationSeconds,
	}

	// Loop names
	for name := range c.LoopNames {
		cw.LoopNames = append(cw.LoopNames, name)
	}
	sort.Strings(cw.LoopNames)

	// Agent names (unique, sorted)
	agentSeen := make(map[string]bool)
	for _, s := range c.Steps {
		if (s.Kind == "agent" || s.Kind == "agent_gate") && s.Agent != "" {
			agentSeen[s.Agent] = true
		}
	}
	for a := range agentSeen {
		cw.Agents = append(cw.Agents, a)
	}
	sort.Strings(cw.Agents)

	// References (templates + schemas, unique)
	refSeen := make(map[string]bool)
	for _, s := range c.Steps {
		if s.Template != "" {
			key := "template: " + s.Template
			if !refSeen[key] {
				cw.References = append(cw.References, key)
				refSeen[key] = true
			}
		}
		if s.OutputSchema != "" {
			key := "schema: " + s.OutputSchema
			if !refSeen[key] {
				cw.References = append(cw.References, key)
				refSeen[key] = true
			}
		}
	}

	return cw
}
