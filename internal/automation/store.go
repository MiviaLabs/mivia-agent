package automation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// automationsFileName is the fixed TOML filename under each scope's
// .mivia directory, mirroring internal/config's mivia.toml naming
// convention (paths.go).
const automationsFileName = "automations.toml"

// idPattern is the ID validation regex ("Identification Rules" in
// docs/design/automations.md), applied to both automationID
// and any runID this package generates or accepts, at TOML load and at
// run creation (createRun, runstore.go).
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ErrInvalidID is the named error "Identification Rules" requires:
// any automationID/runID failing idPattern is rejected with
// this error (wrapped with the offending value).
var ErrInvalidID = errors.New("automation: invalid id")

// ErrInvalidUnattendedPolicy is returned when spec.Unattended is a
// non-empty value other than UnattendedDeny/UnattendedAuto: an
// automation's unattended posture is a closed two-value choice, so an
// unrecognized value (e.g. "yolo") must be refused at validation time
// rather than silently treated as one of the two known policies.
var ErrInvalidUnattendedPolicy = errors.New("automation: invalid unattended policy")

// ValidateID reports whether id matches the charset/length rule
// (^[a-z0-9][a-z0-9_-]{0,63}$), returning a wrapped ErrInvalidID when it
// does not. Called by store load (below) and by run creation
// (createRun, runstore.go), so both call sites share one rule.
func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}

// automationsFilePath returns the automations.toml path for scope under
// root, mirroring internal/config/paths.go's UserConfigPath/
// ProjectConfigPath split: project is <workspaceRoot>/.mivia/
// automations.toml, user is ~/.mivia/automations.toml. ScopeBuiltin has
// no file behind it (ports.Scope's own doc comment: "Builtin rows are
// read-only: there is no file behind them to write or remove") and
// returns an error.
func automationsFilePath(scope ports.Scope, workspaceRoot string) (string, error) {
	switch scope {
	case ports.ScopeProject:
		if workspaceRoot == "" {
			return "", fmt.Errorf("automation: project scope requires a workspace root")
		}
		return workspace.NamespacePath(workspaceRoot, automationsFileName), nil
	case ports.ScopeUser:
		home, err := workspace.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("automation: resolve user home: %w", err)
		}
		return workspace.NamespacePath(home, automationsFileName), nil
	default:
		return "", fmt.Errorf("automation: scope %s has no automations.toml", scope)
	}
}

// fileShape is the on-disk TOML document: a flat table of automations
// keyed by ID, so automations.toml stays diff-friendly (one
// [automations.<id>] table per automation) rather than an ordered array
// that reshuffles on edit.
type fileShape struct {
	Automations map[string]Spec `toml:"automations"`
}

// LoadSpecs reads and validates every automation definition at scope
// under workspaceRoot. A missing file is not an error: it returns an
// empty slice, mirroring config.Load's found=false-is-fine precedent for
// an absent optional file. Every other error (unreadable file, malformed
// TOML, or a Spec failing Validate) is returned; a single bad automation
// fails the whole load rather than silently dropping it, matching the
// stated reason for "Atomic Writes" (a partially-written file must not
// silently disable every automation, and a per-entry error must not
// either - the operator learns about it, not the empty list).
//
// registry is variadic (0 or 1 value) so every two-argument call site
// (store_test.go, service.go, and every other package that already
// calls LoadSpecs with two arguments) keeps compiling unchanged; a
// caller that HAS a live *skills.Registry (the executor, or a
// future Service wired with one via Config.Registry) passes it so a
// StepSlash referencing a project/user skill command resolves and
// validates correctly on load, not just at Apply time.
func LoadSpecs(scope ports.Scope, workspaceRoot string, registry ...*skills.Registry) ([]Spec, error) {
	reg := firstRegistry(registry)
	path, err := automationsFilePath(scope, workspaceRoot)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("automation: read %s: %w", path, err)
	}
	var shape fileShape
	if err := toml.Unmarshal(data, &shape); err != nil {
		return nil, fmt.Errorf("automation: parse %s: %w", path, err)
	}
	specs := make([]Spec, 0, len(shape.Automations))
	for id, spec := range shape.Automations {
		if spec.ID == "" {
			spec.ID = id
		}
		if err := ValidateSpec(spec, reg); err != nil {
			return nil, fmt.Errorf("automation: %s: %w", path, err)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// firstRegistry returns the first value of a variadic *skills.Registry
// slice, or nil when none was supplied. See LoadSpecs/SaveSpecs's doc
// comments for why the parameter is variadic rather than required.
func firstRegistry(registry []*skills.Registry) *skills.Registry {
	if len(registry) == 0 {
		return nil
	}
	return registry[0]
}

// SaveSpecs atomically writes specs as automations.toml at scope under
// workspaceRoot, validating every spec first so a bad in-memory Spec
// never reaches disk. Follows the exact "Atomic Writes" sequence -
// internal/chatsync/delivered_ledger.go:30-37's open-tmp/write/fsync/
// close/rename, not internal/cliworktree/worktree_marker.go:81's
// (which omits the fsync): os.CreateTemp in the target directory,
// Chmod(0600), write, Sync, Close, then os.Rename over the destination.
//
// registry is variadic for the same reason as LoadSpecs's - see its doc
// comment.
func SaveSpecs(scope ports.Scope, workspaceRoot string, specs []Spec, registry ...*skills.Registry) error {
	reg := firstRegistry(registry)
	for _, spec := range specs {
		if err := ValidateSpec(spec, reg); err != nil {
			return err
		}
	}
	path, err := automationsFilePath(scope, workspaceRoot)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("automation: create dir %s: %w", dir, err)
	}
	shape := fileShape{Automations: make(map[string]Spec, len(specs))}
	for _, spec := range specs {
		shape.Automations[spec.ID] = spec
	}
	data, err := toml.Marshal(shape)
	if err != nil {
		return fmt.Errorf("automation: marshal: %w", err)
	}
	return writeFileAtomic(dir, filepath.Base(path), data)
}

// writeFileAtomic implements the "Atomic Writes" sequence: os.CreateTemp in
// dir (never a fixed sibling name - CreateTemp's own randomized suffix
// guards concurrent writers), Chmod(0600), write, Sync, Close, then
// os.Rename over dir/name. A crash or a caller that stops before the
// final Rename leaves the previous dir/name (if any) intact and
// parseable, and the abandoned temp file never becomes the visible file
// - see the atomic-write test in store_test.go, which asserts exactly
// that.
func writeFileAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return fmt.Errorf("automation: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("automation: chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("automation: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("automation: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("automation: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("automation: rename temp file: %w", err)
	}
	return nil
}

// ValidateSpec runs the load-time validation the plan's Scope section
// requires: reject an unknown Step.Kind, reject BaseRef set when
// Worktree=WorktreeNone, reject empty Steps, reject a malformed
// automation ID, reject an unrecognized Unattended value, and reject a
// StepSlash step whose Ref falls outside the headless-safe "Slash
// Command Allowlist". Cron string validation is not done here
// (internal/cronschedule does not exist yet): a cron/recurring
// trigger's raw Cron/TZ strings are stored as-is here, unparsed.
//
// registry may be nil: a caller with no live *skills.Registry (most of
// this package's own tests) still gets every OTHER validation, but a
// StepSlash whose Ref would only resolve through a registry entry
// (a SlashKindSkill command) is then unresolvable and rejected -
// validateStepSlash's own "not recognized" branch, not a special case
// here.
func ValidateSpec(spec Spec, registry *skills.Registry) error {
	if err := ValidateID(spec.ID); err != nil {
		return err
	}
	switch spec.Unattended {
	case "", UnattendedDeny, UnattendedAuto:
	default:
		return fmt.Errorf("automation %q: %w: %q", spec.ID, ErrInvalidUnattendedPolicy, spec.Unattended)
	}
	if len(spec.Steps) == 0 {
		return fmt.Errorf("automation %q: steps must be non-empty", spec.ID)
	}
	for i, step := range spec.Steps {
		if !validStepKind(step.Kind) {
			return fmt.Errorf("automation %q: step %d: unknown step kind %d", spec.ID, i, int(step.Kind))
		}
		if step.Kind == StepSlash {
			if err := validateStepSlash(spec.ID, i, step.Ref, registry); err != nil {
				return err
			}
		}
		if step.Kind == StepSkill {
			if err := validateStepSkill(spec.ID, i, step.Ref, registry); err != nil {
				return err
			}
		}
	}
	if spec.Worktree == WorktreeNone && spec.BaseRef != "" {
		return fmt.Errorf("automation %q: base_ref is set but worktree is none", spec.ID)
	}
	return nil
}
