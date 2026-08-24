package cliworkflow

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// RunWorkflows handles the workflow CLI commands.
func RunWorkflows(args []string) error {
	return RunWorkflowsWithIO(args, os.Stdout, os.Stderr)
}

func RunWorkflowsWithIO(args []string, stdout, stderr io.Writer) error {
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
	fmt.Fprint(stdout, definition.FormatWorkflowList(workflows))
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

	compiled, err := definition.Compile(&wf)
	if err != nil {
		return fmt.Errorf("workflows show: %w", err)
	}

	fmt.Fprint(stdout, definition.FormatWorkflowShow(compiled))
	return nil
}

func runWorkflowsValidate(args []string, stdout, stderr io.Writer) error {
	workspaceRoot, positional := parseWorkspaceFlag(args)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = "."
	}

	// If a specific file path is provided, validate it directly.
	if len(positional) > 0 {
		targetPath := positional[0]
		if strings.HasSuffix(targetPath, ".toml") {
			data, err := os.ReadFile(targetPath)
			if err != nil {
				return fmt.Errorf("workflows validate: %w", err)
			}
			base := filepath.Base(targetPath)
			parsed, name, err := definition.ParseWorkflowTOML(data, base)
			if err != nil {
				fmt.Fprint(stdout, definition.FormatWorkflowValidate(base, nil, err))
				return fmt.Errorf("workflows validate: one or more workflows are invalid")
			}
			compiled, err := definition.Compile(&parsed)
			if err == nil {
				err = validateWorkflowReferences(workspaceRoot, filepath.Dir(targetPath), compiled)
			}
			fmt.Fprint(stdout, definition.FormatWorkflowValidate(name, compiled, err))
			if err != nil {
				return fmt.Errorf("workflows validate: one or more workflows are invalid")
			}
			return nil
		}
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
			fmt.Fprint(stdout, definition.FormatWorkflowValidate(wf.Name, nil, err))
			hasError = true
			continue
		}
		compiled, err := definition.Compile(&parsed)
		if err == nil {
			err = validateWorkflowReferences(workspaceRoot, filepath.Dir(wf.Path), compiled)
		}
		fmt.Fprint(stdout, definition.FormatWorkflowValidate(wf.Name, compiled, err))
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
func validateWorkflowReferences(root, base string, wf *definition.CompiledWorkflow) error {
	skillRegistry, err := LoadChatSkillsFunc(root)
	if err != nil {
		return fmt.Errorf("load workflow skills: %w", err)
	}
	loaded, err := cliagents.LoadAgentDefinitions(root, "", skillRegistry)
	if err != nil {
		return fmt.Errorf("load workflow agents: %w", err)
	}
	if err := SliceErrorsFunc("workflow", definition.ValidateAgentSkillReferences(wf, loaded.Registry, skillRegistry)); err != nil {
		return err
	}
	if err := validateWorkflowSkillTools(wf, loaded.Registry, skillRegistry); err != nil {
		return err
	}
	if err := validateWorkflowFiles(base, wf); err != nil {
		return err
	}
	if err := validateWorkflowVerifiers(root, wf); err != nil {
		return err
	}
	if policy, active := delivery.FromCompiled(wf); active {
		if err := policy.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowSkillTools(wf *definition.CompiledWorkflow, registry *agents.AgentRegistry, skillRegistry *skills.Registry) error {
	for _, step := range wf.Steps {
		if step.Skill == "" {
			// Panel members carry independent skill bindings below.
		} else {
			agent, _ := registry.Get(step.Agent)
			skill, _ := skillRegistry.Get(step.Skill)
			if err := agents.CheckSkillInvocation(&agent, skill.Name, skill.Tools); err != nil {
				return fmt.Errorf("step %q: %w", step.ID, err)
			}
		}
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		for _, member := range step.Panel.Members {
			agent, _ := registry.Get(member.Agent)
			skill, _ := skillRegistry.Get(member.Skill)
			if err := agents.CheckSkillInvocation(&agent, skill.Name, skill.Tools); err != nil {
				return fmt.Errorf("step %q: panel member %q: %w", step.ID, member.ID, err)
			}
		}
	}
	return nil
}

func validateWorkflowFiles(base string, wf *definition.CompiledWorkflow) error {
	schemas := make(map[string][]byte)
	for _, step := range wf.Steps {
		if err := validateWorkflowFileReferences(base, wf, step, "", step.Template, step.OutputSchema, schemas); err != nil {
			return err
		}
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		for _, member := range step.Panel.Members {
			if err := validateWorkflowFileReferences(base, wf, step, member.ID, member.Template, member.OutputSchema, schemas); err != nil {
				return err
			}
		}
	}
	return SliceErrorsFunc("workflow", definition.ValidateSchemaReferenceBytes(&definition.WorkflowFile{Steps: wf.Steps}, schemas))
}

func validateWorkflowFileReferences(base string, wf *definition.CompiledWorkflow, step definition.Step, memberID, templateRef, schemaRef string, schemas map[string][]byte) error {
	prefix := fmt.Sprintf("step %q", step.ID)
	if memberID != "" {
		prefix += fmt.Sprintf(": panel member %q", memberID)
	}
	if templateRef != "" {
		data, err := ReadWorkflowRef(base, templateRef, delivery.MaxTemplateBytes)
		if err != nil {
			return fmt.Errorf("%s: template %q: %w", prefix, templateRef, err)
		}
		if err := validateWorkflowTemplateBindings(wf, step, string(data)); err != nil {
			return fmt.Errorf("%s: template %q: %w", prefix, templateRef, err)
		}
	}
	if schemaRef != "" {
		data, err := ReadWorkflowRef(base, schemaRef, definition.MaxSchemaBytes)
		if err != nil {
			return fmt.Errorf("%s: output_schema %q: %w", prefix, schemaRef, err)
		}
		schemas[schemaRef] = data
	}
	return nil
}

// validateWorkflowTemplateBindings verifies that each template reads only a
// value declared by its step context. It uses empty values because this check
// validates binding names, not runtime output.
//
// The host-injected delivery.failure and implement.touched_files bindings
// land in evidence exactly like a steps.<id>.output binding, mirroring the
// controller's runtime resolution.
//
// A loop-bound step also receives the synthetic inputs.round, which the
// controller injects from the loop's durable iteration counter rather than
// from a declared context binding (see roundInputForStep). Omitting it here
// reported a workflow that runs correctly as invalid, because a review
// template that mints per-round finding ids must read it.
func validateWorkflowTemplateBindings(wf *definition.CompiledWorkflow, step definition.Step, source string) error {
	inputs := make(map[string]any)
	evidence := make(map[string]any)
	if stepIsLoopBound(wf, step) {
		inputs["round"] = ""
	}
	for _, binding := range step.Context {
		if strings.HasPrefix(binding.From, "inputs.") {
			inputs[binding.As] = ""
			continue
		}
		if strings.HasPrefix(binding.From, "steps.") {
			evidence[binding.As] = ""
			continue
		}
		if strings.HasPrefix(binding.From, "delivery.") {
			evidence[binding.As] = ""
		}
		if strings.HasPrefix(binding.From, "implement.") {
			evidence[binding.As] = ""
		}
	}
	_, err := delivery.Render(source, inputs, evidence, delivery.MaxTemplateBytes, delivery.MaxTemplateBytes)
	return err
}

// stepIsLoopBound reports whether the step owns a loop back-edge, which is
// exactly the condition under which the controller supplies inputs.round.
func stepIsLoopBound(wf *definition.CompiledWorkflow, step definition.Step) bool {
	if wf == nil {
		return false
	}
	for _, t := range wf.Transitions {
		if t.From == step.ID && t.Loop != "" {
			return true
		}
	}
	return false
}

// validateWorkflowVerifiers checks that every evidence_gate resolves to a
// runnable profile.
//
// It resolves in the same order the controller does (see gateProfile): a step
// that declares a command uses that command, and only a step without one is
// looked up in the catalogue. The catalogue comes from the WORKSPACE's own
// [verifiers] tables (config.LoadWorkspaceVerifiers), never from the invoking
// user's ~/.mivia config: validation of a foreign workspace must give the
// same verdict on every machine.
func validateWorkflowVerifiers(root string, wf *definition.CompiledWorkflow) error {
	profiles, err := config.LoadWorkspaceVerifiers(root)
	if err != nil {
		return err
	}
	catalogue, err := workflowVerifierCatalogue(profiles, secretpath.Policy{})
	if err != nil {
		return err
	}
	for _, step := range wf.Steps {
		if step.Kind != "evidence_gate" {
			continue
		}
		if step.Command != nil {
			if _, err := definition.NewCommandProfile(step.Command.Check, step.Command.Program, step.Command.Args); err != nil {
				return fmt.Errorf("step %q: %w", step.ID, err)
			}
			continue
		}
		if _, err := catalogue.Lookup(step.Verifier); err != nil {
			return fmt.Errorf("step %q: declare a [verifiers.%s] table in the workspace config: %w", step.ID, step.Verifier, err)
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

	compiled, err := definition.Compile(&wf)
	if err != nil {
		return fmt.Errorf("workflows explain: %w", err)
	}

	// Build explain data from compiled workflow.
	baseDir := filepath.Dir(found.Path)
	cw := buildExplainView(compiled, baseDir)

	fmt.Fprint(stdout, definition.FormatWorkflowExplain(cw))
	return nil
}

// buildExplainView converts a CompiledWorkflow into a CompiledWorkflowExplain
// for the presentation layer.
func buildExplainView(c *definition.CompiledWorkflow, baseDir string) *definition.CompiledWorkflowExplain {
	cw := &definition.CompiledWorkflowExplain{
		Name:                    c.Name,
		Description:             c.Description,
		Version:                 c.Version,
		Digest:                  c.Digest,
		Steps:                   c.Steps,
		Transitions:             c.Transitions,
		InitialStep:             c.InitialStep,
		Delivery:                c.Delivery,
		MaxStepAttempts:         c.Limits.MaxStepAttempts,
		MaxDurationSeconds:      c.Limits.MaxDurationSeconds,
		MaxOnFailureReentries:   c.Limits.MaxOnFailureReentries,
		MaxTransientStepRetries: c.Limits.MaxTransientStepRetries,
	}

	// Loop names
	for name := range c.LoopNames {
		cw.LoopNames = append(cw.LoopNames, name)
	}
	sort.Strings(cw.LoopNames)

	// Agent names (unique, sorted)
	agentSeen := make(map[string]bool)
	for _, s := range c.Steps {
		if (s.Kind == "agent" || s.Kind == "agent_gate" || s.Kind == "agent_panel") && s.Agent != "" {
			agentSeen[s.Agent] = true
		}
		if s.Kind == "agent_panel" && s.Panel != nil {
			for _, member := range s.Panel.Members {
				if member.Agent != "" {
					agentSeen[member.Agent] = true
				}
			}
		}
	}
	for a := range agentSeen {
		cw.Agents = append(cw.Agents, a)
	}
	sort.Strings(cw.Agents)

	// References (templates + schemas, unique)
	refSeen := make(map[string]bool)
	for _, s := range c.Steps {
		addWorkflowExplainReference(cw, refSeen, "template", s.Template)
		addWorkflowExplainReference(cw, refSeen, "schema", s.OutputSchema)
		if s.Kind == "agent_panel" && s.Panel != nil {
			for _, member := range s.Panel.Members {
				addWorkflowExplainReference(cw, refSeen, "template", member.Template)
				addWorkflowExplainReference(cw, refSeen, "schema", member.OutputSchema)
			}
		}
	}

	return cw
}

func addWorkflowExplainReference(cw *definition.CompiledWorkflowExplain, seen map[string]bool, kind, reference string) {
	if reference == "" {
		return
	}
	key := kind + ": " + reference
	if seen[key] {
		return
	}
	cw.References = append(cw.References, key)
	seen[key] = true
}
