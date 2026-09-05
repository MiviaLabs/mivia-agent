package definition

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
)

// ParseWorkflowTOML parses a single workflow definition body with unknown-key
// rejection. filename is the base name (e.g. "feature-delivery.toml").
func ParseWorkflowTOML(data []byte, filename string) (WorkflowFile, string, error) {
	canonical, err := workflowNameFromFilename(filename)
	if err != nil {
		return WorkflowFile{}, "", err
	}
	var wf WorkflowFile
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&wf); err != nil {
		return WorkflowFile{}, "", fmt.Errorf("workflow %q: %w", canonical, err)
	}
	if strings.TrimSpace(wf.Name) == "" {
		return WorkflowFile{}, "", fmt.Errorf("workflow %q: name is required", canonical)
	}
	if wf.Name != canonical {
		return WorkflowFile{}, "", fmt.Errorf(
			"workflow %q: in-file name %q does not match filename", canonical, wf.Name)
	}
	if err := applyStepDefaults(&wf); err != nil {
		return WorkflowFile{}, "", fmt.Errorf("workflow %q: %w", canonical, err)
	}
	if err := validateWorkflowBasics(&wf); err != nil {
		return WorkflowFile{}, "", fmt.Errorf("workflow %q: %w", canonical, err)
	}
	return wf, canonical, nil
}

func workflowNameFromFilename(filename string) (string, error) {
	base := filepath.Base(filename)
	if !strings.HasSuffix(base, ".toml") {
		return "", fmt.Errorf("workflow file %q must end in .toml", base)
	}
	name := strings.TrimSuffix(base, ".toml")
	if err := validateWorkflowName(name); err != nil {
		return "", err
	}
	return name, nil
}

func validateWorkflowName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("workflow name is empty")
	}
	if strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return fmt.Errorf("workflow name %q is invalid", name)
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("workflow name %q must be lowercase", name)
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("workflow name %q contains invalid character %q", name, r)
	}
	return nil
}

func validateWorkflowBasics(wf *WorkflowFile) error {
	if wf.Version != 1 {
		return fmt.Errorf("unsupported version %d (only version 1 is supported)", wf.Version)
	}
	if strings.TrimSpace(wf.InitialStep) == "" {
		return fmt.Errorf("initial_step is required")
	}
	if ReservedStepIDs[wf.InitialStep] {
		return fmt.Errorf("initial_step must not be a reserved ID (%q)", wf.InitialStep)
	}
	if len(wf.Steps) == 0 {
		return fmt.Errorf("at least one step is required")
	}
	// Validate step IDs.
	seen := make(map[string]bool)
	for i, s := range wf.Steps {
		if err := validateStep(i, &s); err != nil {
			return err
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate step ID %q", s.ID)
		}
		seen[s.ID] = true
	}
	// Validate transitions reference existing steps or terminals.
	for i, t := range wf.Transitions {
		if err := validateTransition(i, &t, seen); err != nil {
			return err
		}
	}
	// Validate input defs.
	for name, inp := range wf.Inputs {
		if err := validateInputDef(name, &inp); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(index int, s *Step) error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("step[%d]: id is required", index)
	}
	if ReservedStepIDs[s.ID] {
		return fmt.Errorf("step %q: id is reserved (must not be %q)", s.ID, s.ID)
	}
	if !ValidStepKinds[s.Kind] {
		return fmt.Errorf("step %q: unknown kind %q", s.ID, s.Kind)
	}
	// Agent steps require an agent field.
	if s.Kind == "agent" || s.Kind == "agent_gate" || s.Kind == "agent_panel" {
		if strings.TrimSpace(s.Agent) == "" {
			return fmt.Errorf("step %q: agent is required for kind %q", s.ID, s.Kind)
		}
	}
	if err := validatePanelStep(s); err != nil {
		return err
	}
	// Evidence gates require a verifier field or a sandboxed command, never both.
	if s.Kind == "evidence_gate" {
		hasVerifier := strings.TrimSpace(s.Verifier) != ""
		hasCommand := s.Command != nil
		switch {
		case hasVerifier && hasCommand:
			return fmt.Errorf("step %q: evidence_gate must not declare both verifier and command", s.ID)
		case !hasVerifier && !hasCommand:
			return fmt.Errorf("step %q: verifier or command is required for kind %q", s.ID, s.Kind)
		}
		if hasCommand {
			if strings.TrimSpace(s.Command.Check) == "" {
				return fmt.Errorf("step %q: command.check is required", s.ID)
			}
			if !IsBareProgramName(s.Command.Program) {
				return fmt.Errorf("step %q: command.program %q must be a bare executable name", s.ID, s.Command.Program)
			}
		}
	}
	return nil
}

func validateTransition(index int, t *Transition, stepIDs map[string]bool) error {
	if strings.TrimSpace(t.From) == "" {
		return fmt.Errorf("transition[%d]: from is required", index)
	}
	if strings.TrimSpace(t.To) == "" {
		return fmt.Errorf("transition[%d]: to is required", index)
	}
	if !stepIDs[t.From] && !ReservedStepIDs[t.From] {
		return fmt.Errorf("transition[%d]: from %q is not a declared step", index, t.From)
	}
	if !stepIDs[t.To] && !ReservedStepIDs[t.To] {
		return fmt.Errorf("transition[%d]: to %q is not a declared step or terminal", index, t.To)
	}
	status := strings.TrimSpace(t.Match.Status)
	if status == "" {
		return fmt.Errorf("transition[%d]: match.status is required", index)
	}
	// The runtime only ever matches against these two values (linear_route.go
	// routes a completed attempt with "succeeded" and a failed one with
	// "failed"), so any other status is an edge that can never fire. Left
	// unchecked, a natural typo like status = "success" compiled clean and
	// then routed the final step's success to zero_match - the run failed
	// having done all of its work.
	if !ValidTransitionStatuses[status] {
		return fmt.Errorf("transition[%d]: match.status %q is not one of %s", index, status, transitionStatusList())
	}
	return nil
}

func validateInputDef(name string, inp *InputDef) error {
	typ := strings.TrimSpace(inp.Type)
	if typ == "" {
		return fmt.Errorf("input %q: type is required", name)
	}
	// ParseInputValue supports exactly this set and errors on anything else -
	// but only once a value is actually supplied. Unchecked here, a typo like
	// type = "int" validated clean and shipped, then failed EVERY run at
	// admission for a required input, or silently never applied for an
	// optional one.
	if !ValidInputTypes[typ] {
		return fmt.Errorf("input %q: type %q is not one of %s", name, typ, inputTypeList())
	}
	if inp.MaxBytes < 0 {
		return fmt.Errorf("input %q: max_bytes must be >= 0 (got %d)", name, inp.MaxBytes)
	}
	if inp.MaxBytes > MaxInputBytes {
		return fmt.Errorf("input %q: max_bytes %d exceeds maximum of %d", name, inp.MaxBytes, MaxInputBytes)
	}
	return nil
}
