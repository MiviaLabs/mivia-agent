package cli

// Workspace-declared verifier profiles: catalogue construction from resolved
// config, admission-time pinning of each referenced definition into the run
// snapshot, and resume-time verification against that pin. The host ships no
// built-in profiles; [verifiers.<name>] in mivia.toml is the only catalogue.

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// workflowVerifierDefPrefix namespaces pinned verifier definitions inside
// Snapshot.Verifiers. The prefix contains a colon, which no profile name may
// carry, so a declared profile can never overwrite or forge the module
// baseline refs (go-mod-baseline / go-sum-baseline) that share the map.
const workflowVerifierDefPrefix = "verifier-def:"

// workflowVerifierPinsVersion is written to Snapshot.VerifierPinsVersion at
// fresh admission. See that field's doc for the resume semantics.
const workflowVerifierPinsVersion = 1

// Legacy built-in profile names. Snapshots admitted before definitions were
// pinned carry no verifier-def entries; the baseline decision for those runs
// falls back to the historic name matching so an in-flight run keeps its
// pinned module baseline across the upgrade.
const (
	legacyGoTestName   = "go-test"
	legacyGoVerifyName = "go-verify"
	legacyGoFinalName  = "go-final"
)

// pinnedVerifierDefinition is the canonical, digestable form of one declared
// profile. Field order is fixed by the struct; Args is normalized to an empty
// slice so nil-vs-empty cannot produce two digests for one definition.
type pinnedVerifierDefinition struct {
	Name             string                  `json:"name"`
	GoModuleBaseline bool                    `json:"go_module_baseline"`
	Commands         []pinnedVerifierCommand `json:"commands"`
}

type pinnedVerifierCommand struct {
	Check   string   `json:"check"`
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

func verifierDefinitionBytes(name string, profile config.VerifierProfile) ([]byte, error) {
	pinned := pinnedVerifierDefinition{Name: name, GoModuleBaseline: profile.GoModuleBaseline, Commands: make([]pinnedVerifierCommand, 0, len(profile.Commands))}
	for _, command := range profile.Commands {
		args := command.Args
		if args == nil {
			args = []string{}
		}
		pinned.Commands = append(pinned.Commands, pinnedVerifierCommand{Check: command.Check, Program: command.Program, Args: args})
	}
	return json.Marshal(pinned)
}

// workflowVerifierCatalogue builds the run's verifier catalogue from the
// workspace-declared profiles. An empty declaration set yields an empty
// catalogue: unknown names still fail closed at Lookup.
func workflowVerifierCatalogue(profiles map[string]config.VerifierProfile, policy secretpath.Policy) (*definition.Catalogue, error) {
	catalogue := definition.NewCatalogue()
	for name, profile := range profiles {
		declared, err := declaredVerifierProfile(name, profile, policy)
		if err != nil {
			return nil, err
		}
		if err := catalogue.Register(declared); err != nil {
			return nil, err
		}
	}
	return catalogue, nil
}

func declaredVerifierProfile(name string, profile config.VerifierProfile, policy secretpath.Policy) (definition.Profile, error) {
	commands := make([]definition.DeclaredCommand, 0, len(profile.Commands))
	for _, command := range profile.Commands {
		commands = append(commands, definition.DeclaredCommand{Check: command.Check, Program: command.Program, Args: command.Args})
	}
	return definition.NewDeclaredProfile(name, commands, policy)
}

// workflowReferencedVerifiers returns the unique catalogue names the
// workflow's evidence_gate steps reference, sorted. Steps with an inline
// command reference no catalogue name.
func workflowReferencedVerifiers(wf *definition.CompiledWorkflow) []string {
	if wf == nil {
		return nil
	}
	seen := make(map[string]bool)
	for _, step := range wf.Steps {
		if step.Kind == "evidence_gate" && step.Verifier != "" {
			seen[step.Verifier] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validateWorkflowVerifierReferences fails admission early - before any agent
// step burns tokens - when a referenced profile is not declared, naming the
// missing table so the fix is obvious.
func validateWorkflowVerifierReferences(wf *definition.CompiledWorkflow, profiles map[string]config.VerifierProfile) error {
	for _, name := range workflowReferencedVerifiers(wf) {
		if _, ok := profiles[name]; !ok {
			return fmt.Errorf("workflow references verifier %q but the workspace config declares no [verifiers.%s] table", name, name)
		}
	}
	return nil
}

// pinWorkflowVerifierDefinitions writes each referenced profile's canonical
// definition into the fresh run's snapshot, so a resumed run's gate executes
// exactly what admission saw regardless of later config or binary changes.
func pinWorkflowVerifierDefinitions(raw []byte, wf *definition.CompiledWorkflow, profiles map[string]config.VerifierProfile) ([]byte, error) {
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return nil, err
	}
	// Mark the snapshot generation even when no step references a profile:
	// on resume, version >= 1 with a missing verifier-def key means the key
	// was stripped, never that this binary did not write it.
	snapshot.VerifierPinsVersion = workflowVerifierPinsVersion
	names := workflowReferencedVerifiers(wf)
	if snapshot.Verifiers == nil {
		snapshot.Verifiers = make(map[string]workflowledger.RefSnapshot)
	}
	for _, name := range names {
		profile, ok := profiles[name]
		if !ok {
			return nil, fmt.Errorf("workflow references verifier %q but the workspace config declares no [verifiers.%s] table", name, name)
		}
		bytes, err := verifierDefinitionBytes(name, profile)
		if err != nil {
			return nil, err
		}
		snapshot.Verifiers[workflowVerifierDefPrefix+name] = workflowledger.RefSnapshot{Digest: digestBytes(bytes), Bytes: bytes}
	}
	return workflowledger.MarshalSnapshot(snapshot)
}

// snapshotHasVerifierDefinitions reports whether the prior snapshot carries
// pinned verifier definitions. The version marker is authoritative; the key
// scan keeps older marker-less snapshots that DO carry pins (written between
// the pinning and marker changes) on the strict path. A snapshot with
// neither predates definition pinning and resumes without definition
// verification instead of failing on a pin that never existed.
func snapshotHasVerifierDefinitions(prior *workflowledger.Snapshot) bool {
	if prior == nil {
		return false
	}
	if prior.VerifierPinsVersion >= workflowVerifierPinsVersion {
		return true
	}
	for key := range prior.Verifiers {
		if strings.HasPrefix(key, workflowVerifierDefPrefix) {
			return true
		}
	}
	return false
}

// verifyWorkflowVerifierSnapshot re-resolves every referenced profile from
// the current config and fails closed when one is missing or drifted from
// its admission-time pin. Mirrors verifyWorkflowSkillSnapshot.
func verifyWorkflowVerifierSnapshot(wf *definition.CompiledWorkflow, profiles map[string]config.VerifierProfile, prior *workflowledger.Snapshot) error {
	if prior == nil || !snapshotHasVerifierDefinitions(prior) {
		return nil
	}
	for _, name := range workflowReferencedVerifiers(wf) {
		pinned, ok := prior.Verifiers[workflowVerifierDefPrefix+name]
		if !ok {
			return fmt.Errorf("workflow verifier %q was not pinned at admission", name)
		}
		if pinned.Digest == "" || digestBytes(pinned.Bytes) != pinned.Digest {
			return fmt.Errorf("workflow verifier %q pin digest is invalid", name)
		}
		profile, ok := profiles[name]
		if !ok {
			return fmt.Errorf("workflow verifier %q is missing on resume; restore its [verifiers.%s] table", name, name)
		}
		bytes, err := verifierDefinitionBytes(name, profile)
		if err != nil {
			return err
		}
		if pinned.Digest != digestBytes(bytes) || string(pinned.Bytes) != string(bytes) {
			return fmt.Errorf("workflow verifier %q changed since admission; recover with: --accept-verifier-change, restore its [verifiers.%s] table, or start a fresh run", name, name)
		}
	}
	return nil
}

// acceptWorkflowVerifierChanges is the operator's explicit re-admit path for
// a run whose declared verifier definitions changed after admission
// (`workflow resume --accept-verifier-change`). It rewrites the IN-MEMORY
// prior snapshot's pins to the current declarations and returns the names
// that drifted. The durable admission record is never touched: the snapshot
// is immutable and digest-checked, so acceptance is per-invocation - a later
// resume needs the flag again unless the config change is reverted.
//
// One change cannot be accepted mid-run: turning go_module_baseline ON for a
// profile whose run pinned no module baseline at admission. There is no
// admitted baseline to enforce, and capturing one mid-run would pin whatever
// the workflow already wrote.
func acceptWorkflowVerifierChanges(prior *workflowledger.Snapshot, wf *definition.CompiledWorkflow, profiles map[string]config.VerifierProfile) ([]string, error) {
	if prior == nil || !snapshotHasVerifierDefinitions(prior) {
		return nil, nil
	}
	_, hadBaseline := prior.Verifiers[workflowGoModBaselineRef]
	var drifted []string
	for _, name := range workflowReferencedVerifiers(wf) {
		profile, ok := profiles[name]
		if !ok {
			return nil, fmt.Errorf("cannot accept verifier change: %q is not declared; restore its [verifiers.%s] table", name, name)
		}
		bytes, err := verifierDefinitionBytes(name, profile)
		if err != nil {
			return nil, err
		}
		pinned, ok := prior.Verifiers[workflowVerifierDefPrefix+name]
		if ok && string(pinned.Bytes) == string(bytes) {
			continue
		}
		var wasBaseline bool
		if ok {
			var previous pinnedVerifierDefinition
			if err := json.Unmarshal(pinned.Bytes, &previous); err != nil {
				return nil, fmt.Errorf("workflow verifier %q pin is invalid: %w", name, err)
			}
			wasBaseline = previous.GoModuleBaseline
		}
		if profile.GoModuleBaseline && !wasBaseline && !hadBaseline {
			return nil, fmt.Errorf("cannot accept verifier change: %q now requires the Go module baseline but the run pinned none at admission", name)
		}
		drifted = append(drifted, name)
		if prior.Verifiers == nil {
			prior.Verifiers = make(map[string]workflowledger.RefSnapshot)
		}
		prior.Verifiers[workflowVerifierDefPrefix+name] = workflowledger.RefSnapshot{Digest: digestBytes(bytes), Bytes: bytes}
	}
	return drifted, nil
}

// applyAcceptedVerifierChanges runs the acceptance rewrite for a resume that
// passed --accept-verifier-change and reports each accepted profile to the
// operator.
func applyAcceptedVerifierChanges(prior *workflowledger.Snapshot, wf *definition.CompiledWorkflow, profiles map[string]config.VerifierProfile, stderr io.Writer) error {
	drifted, err := acceptWorkflowVerifierChanges(prior, wf, profiles)
	if err != nil {
		return err
	}
	for _, name := range drifted {
		fmt.Fprintf(stderr, "accepting changed verifier %q for this resume; the admission pin is unchanged\n", name)
	}
	if len(drifted) == 0 {
		fmt.Fprintln(stderr, "no verifier definitions drifted; --accept-verifier-change had no effect")
	}
	return nil
}

// workflowNeedsGoBaseline decides whether the run needs the pinned Go module
// baseline. Fresh runs read the workspace-declared profiles. Resumed runs
// read the PINNED definitions - never live config - so flipping
// go_module_baseline mid-run can neither drop the pin silently nor brick the
// resume. Pre-pinning snapshots fall back to the legacy built-in names.
func workflowNeedsGoBaseline(wf *definition.CompiledWorkflow, profiles map[string]config.VerifierProfile, prior *workflowledger.Snapshot) (bool, error) {
	names := workflowReferencedVerifiers(wf)
	if prior == nil {
		for _, name := range names {
			if profiles[name].GoModuleBaseline {
				return true, nil
			}
		}
		return false, nil
	}
	if !snapshotHasVerifierDefinitions(prior) {
		for _, name := range names {
			switch name {
			case legacyGoTestName, legacyGoVerifyName, legacyGoFinalName:
				return true, nil
			}
		}
		return false, nil
	}
	for _, name := range names {
		pinned, ok := prior.Verifiers[workflowVerifierDefPrefix+name]
		if !ok {
			return false, fmt.Errorf("workflow verifier %q was not pinned at admission", name)
		}
		var definition pinnedVerifierDefinition
		if err := json.Unmarshal(pinned.Bytes, &definition); err != nil {
			return false, fmt.Errorf("workflow verifier %q pin is invalid: %w", name, err)
		}
		if definition.GoModuleBaseline {
			return true, nil
		}
	}
	return false, nil
}
