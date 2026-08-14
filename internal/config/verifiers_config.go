package config

// Workspace-declared verifier profiles: the [verifiers.<name>] tables in
// mivia.toml. The host ships no built-in verifier catalogue - a project
// declares its own evidence-gate profiles here, which keeps the workflow
// engine project- and language-generic.
//
// Parsing follows the [[hooks]] idiom, not the tolerant struct decode the
// rest of mivia.toml uses: these tables declare commands, so an unknown or
// misspelled key must be a hard error, never a silent no-op. Rejection is the
// deliverable.

import (
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// VerifierCommand is one sandboxed command of a declared profile. Program is
// a bare executable name; Args are argv verbatim, never a shell string.
type VerifierCommand struct {
	Check   string
	Program string
	Args    []string
}

// VerifierProfile is one [verifiers.<name>] table: an ordered command list
// and whether the profile needs the pinned Go module baseline.
type VerifierProfile struct {
	// GoModuleBaseline marks a profile whose commands read Go module files.
	// When any referenced profile sets it, admission captures go.mod/go.sum
	// and the sandbox pins them so a workflow agent cannot change what the
	// gate builds against.
	GoModuleBaseline bool
	Commands         []VerifierCommand
}

// verifierProfileNameRegex mirrors the workflow compiler's verifier name
// rule: lowercase alphanumeric with hyphens, not starting with a hyphen.
var verifierProfileNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// bareVerifierProgramRegex mirrors verifier.IsBareProgramName without
// importing the verifier package: no slashes, no dot-only names, no shell
// metacharacters. The verifier package re-validates at profile construction,
// so a drift between the two rules fails closed, never open.
var bareVerifierProgramRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// verifierTableKeys is the closed set of keys a [verifiers.<name>] table may
// carry.
var verifierTableKeys = map[string]bool{"go_module_baseline": true, "commands": true}

// verifierCommandKeys is the closed set of keys one commands entry may carry.
var verifierCommandKeys = map[string]bool{"check": true, "program": true, "args": true}

// LoadWorkspaceVerifiers parses ONLY the [verifiers] tables of one
// workspace's own .mivia/mivia.toml. `mivia workflows validate` uses it so
// validation of a foreign workspace neither requires a full provider config
// nor mixes in the invoking user's ~/.mivia profiles: what validates here is
// exactly what the workspace declares. A missing config file means no
// declared profiles, not an error.
func LoadWorkspaceVerifiers(workspaceRoot string) (map[string]VerifierProfile, error) {
	path := workspace.NamespacePath(workspaceRoot, "mivia.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return parseVerifiersLayer(data, path)
}

// parseVerifiersLayer parses one config layer's [verifiers] tables. Later
// layers overwrite earlier ones whole-profile (no per-command merging), which
// mergeVerifierLayer applies.
func parseVerifiersLayer(data []byte, path string) (map[string]VerifierProfile, error) {
	var file struct {
		Verifiers map[string]map[string]any `toml:"verifiers"`
	}
	if err := toml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: parse [verifiers]: %w", path, err)
	}
	if len(file.Verifiers) == 0 {
		return nil, nil
	}
	profiles := make(map[string]VerifierProfile, len(file.Verifiers))
	names := make([]string, 0, len(file.Verifiers))
	for name := range file.Verifiers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile, err := parseVerifierProfile(name, file.Verifiers[name])
		if err != nil {
			return nil, fmt.Errorf("%s: [verifiers.%s]: %w", path, name, err)
		}
		profiles[name] = profile
	}
	return profiles, nil
}

func parseVerifierProfile(name string, raw map[string]any) (VerifierProfile, error) {
	if !verifierProfileNameRegex.MatchString(name) {
		return VerifierProfile{}, fmt.Errorf("profile name must be lowercase alphanumeric with hyphens")
	}
	for key := range raw {
		if !verifierTableKeys[key] {
			return VerifierProfile{}, fmt.Errorf("unknown key %q; a [verifiers.<name>] table accepts go_module_baseline and commands", key)
		}
	}
	var profile VerifierProfile
	if value, ok := raw["go_module_baseline"]; ok {
		flag, ok := value.(bool)
		if !ok {
			return VerifierProfile{}, fmt.Errorf("go_module_baseline must be a boolean")
		}
		profile.GoModuleBaseline = flag
	}
	commands, err := parseVerifierCommands(raw["commands"])
	if err != nil {
		return VerifierProfile{}, err
	}
	profile.Commands = commands
	return profile, nil
}

func parseVerifierCommands(value any) ([]VerifierCommand, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("commands must be a non-empty array of {check, program, args} tables")
	}
	commands := make([]VerifierCommand, 0, len(list))
	for i, entry := range list {
		raw, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("commands[%d] must be a {check, program, args} table", i)
		}
		command, err := parseVerifierCommand(raw)
		if err != nil {
			return nil, fmt.Errorf("commands[%d]: %w", i, err)
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func parseVerifierCommand(raw map[string]any) (VerifierCommand, error) {
	for key := range raw {
		if !verifierCommandKeys[key] {
			return VerifierCommand{}, fmt.Errorf("unknown key %q; a command accepts check, program and args", key)
		}
	}
	check, ok := raw["check"].(string)
	if !ok || check == "" {
		return VerifierCommand{}, fmt.Errorf("check is required and must be a non-empty string")
	}
	program, ok := raw["program"].(string)
	if !ok || !isBareVerifierProgram(program) {
		return VerifierCommand{}, fmt.Errorf("program must be a bare executable name (no paths, no shell)")
	}
	command := VerifierCommand{Check: check, Program: program}
	if value, ok := raw["args"]; ok {
		list, ok := value.([]any)
		if !ok {
			return VerifierCommand{}, fmt.Errorf("args must be an array of strings")
		}
		for _, item := range list {
			arg, ok := item.(string)
			if !ok {
				return VerifierCommand{}, fmt.Errorf("args must be an array of strings")
			}
			command.Args = append(command.Args, arg)
		}
	}
	return command, nil
}

func isBareVerifierProgram(program string) bool {
	return program != "." && program != ".." && bareVerifierProgramRegex.MatchString(program)
}

// mergeVerifierLayer applies one layer's profiles over the accumulated set.
// A later layer wins whole-profile: declaring [verifiers.go-test] in the
// workspace file replaces every field of a base-layer go-test, so what runs
// is always exactly one file's declaration, never a field-level blend.
func mergeVerifierLayer(base, layer map[string]VerifierProfile) map[string]VerifierProfile {
	if len(layer) == 0 {
		return base
	}
	if base == nil {
		base = make(map[string]VerifierProfile, len(layer))
	}
	for name, profile := range layer {
		base[name] = profile
	}
	return base
}

// cloneVerifierProfiles deep-copies the declared profile set for Resolved.
func cloneVerifierProfiles(profiles map[string]VerifierProfile) map[string]VerifierProfile {
	if len(profiles) == 0 {
		return nil
	}
	out := make(map[string]VerifierProfile, len(profiles))
	for name, profile := range profiles {
		commands := make([]VerifierCommand, len(profile.Commands))
		for i, command := range profile.Commands {
			commands[i] = VerifierCommand{Check: command.Check, Program: command.Program, Args: append([]string(nil), command.Args...)}
		}
		out[name] = VerifierProfile{GoModuleBaseline: profile.GoModuleBaseline, Commands: commands}
	}
	return out
}
