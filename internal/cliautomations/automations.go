package cliautomations

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// RunAutomations dispatches `mivia automations <subcommand>`.
func RunAutomations(args []string) error {
	if len(args) == 0 {
		printAutomationsUsage()
		return fmt.Errorf("automations: expected a subcommand (list, show, run, runs, resume, serve)")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return runListCommand(rest)
	case "show":
		return runShowCommand(rest)
	case "run":
		return runRunCommand(rest)
	case "runs":
		return runRunsCommand(rest)
	case "resume":
		return runResumeCommand(rest)
	case "serve":
		return runServeCommand(rest)
	default:
		printAutomationsUsage()
		return fmt.Errorf("automations: unknown subcommand %q", sub)
	}
}

func printAutomationsUsage() {
	fmt.Fprintln(stderrWriter(), automationsUsageText())
}

func automationsUsageText() string {
	return strings.TrimRight(`Usage:
  mivia automations list [--workspace dir] [--config path]
  mivia automations show <id> [--workspace dir] [--config path]
  mivia automations run <id> [--wait] [--workspace dir] [--config path]
  mivia automations runs [--automation <id>] [--limit <n>] [--workspace dir] [--config path]
  mivia automations resume <run-id> [--workspace dir] [--config path]
  mivia automations serve [--workspace dir] [--config path]
`, "\n")
}

// resolveWorkspaceAndConfig opens the workspace root and loads its
// config, mirroring internal/cliworkflow's PrepareWorkflowRun idiom
// (workspace.Open + config.Load with AllowMissingConfig=true).
//
// configPath, when empty, is resolved to workspaceRoot's own
// .mivia/mivia.toml (if one exists) BEFORE calling config.Load - see
// automationConfigPath's own doc comment for why config.Load's own
// empty-ConfigPath fallback (DefaultConfigCandidates, which searches the
// PROCESS's cwd, never WorkspaceRoot) is unsuitable for a CLI command
// that takes an explicit --workspace flag.
func resolveWorkspaceAndConfig(workspaceRoot, configPath string) (string, *config.Resolved, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = "."
	}
	work, err := workspace.Open(workspaceRoot)
	if err != nil {
		return "", nil, err
	}
	configPath = automationConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return "", nil, err
	}
	clichat.ApplyPrivacyPolicy(res)
	return work.Abs, res, nil
}

// automationConfigPath resolves the config file this package's commands
// load: explicit wins; otherwise root's own .mivia/mivia.toml if it
// exists as a regular file; otherwise empty (config.Load's
// AllowMissingConfig then falls back to its own DefaultConfigCandidates
// search, e.g. ~/.mivia/mivia.toml).
//
// This mirrors internal/cliworkflow.WorkflowConfigPath exactly (same
// name, same behavior) because config.Load's own empty-ConfigPath
// resolution scans the PROCESS's current working directory
// (DefaultConfigCandidates -> os.Getwd()+".mivia/mivia.toml"), never the
// caller-supplied WorkspaceRoot. A `mivia automations --workspace <dir>`
// invocation from a different cwd (the common case: an operator's
// crontab entry, or any invocation not run from inside the target
// workspace) would otherwise silently load the WRONG project's config,
// or find none at all when none exists at the process cwd - not the
// workspace this command was explicitly pointed at.
func automationConfigPath(root, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	candidate := workspace.NamespacePath(root, "mivia.toml")
	info, err := os.Stat(candidate)
	if err == nil && info.Mode().IsRegular() {
		return candidate
	}
	return ""
}

// parseCommonFlags extracts --workspace and --config from args, returning
// the remaining positional args.
func parseCommonFlags(args []string) (workspaceRoot, configPath string, rest []string, err error) {
	rest = args
	workspaceRoot, rest, _, err = flagValueSeam(rest, "--workspace")
	if err != nil {
		return "", "", nil, err
	}
	configPath, rest, _, err = flagValueSeam(rest, "--config")
	if err != nil {
		return "", "", nil, err
	}
	return workspaceRoot, configPath, rest, nil
}

// flagValueSeam mirrors internal/cli's flagValue helper (space or "="
// form, refuses a missing/dash-prefixed space value) without importing
// internal/cli, which would create a cycle (internal/cli imports this
// package). A small local re-implementation, not a shared seam: the
// contract is simple enough that duplication is cheaper than a new
// shared leaf package for one helper.
func flagValueSeam(args []string, name string) (string, []string, bool, error) {
	out := make([]string, 0, len(args))
	var val string
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == name:
			if i+1 >= len(args) || (len(args[i+1]) > 0 && args[i+1][0] == '-') {
				return "", nil, found, fmt.Errorf("%s requires a value", name)
			}
			val = args[i+1]
			found = true
			i++
		case len(a) > len(name)+1 && a[:len(name)+1] == name+"=":
			val = a[len(name)+1:]
			found = true
		default:
			out = append(out, a)
		}
	}
	return val, out, found, nil
}

// applyPrivacyPolicyFunc is a thin indirection over clichat's privacy
// policy application, matching cliworkflow's own
// ApplyPrivacyPolicyFunc seam shape. Assigned by cli/root.go's wiring
// in the real binary; a test binary that never wires it gets a safe
// no-op default.
var applyPrivacyPolicyFunc = func(*config.Resolved) {}

// openAutomationStore opens (creating if needed) the SQLite store the
// automation Service uses for run records and fenced claims.
//
// This is the SAME store composition.BuildSession opens for a chat
// session (clichat.ContextStorePath(root, cfg.Subagents), honoring an
// operator-configured [subagents] store_path and otherwise defaulting to
// workspace.GlobalContextStorePath - one store shared by every workspace
// on the machine), not a package-private "automations.db". Before this,
// the CLI opened its own separate automations.db while the TUI wired
// wireAutomationBackend to sess.ContextStore() (also resolved through
// ContextStorePath) - two different SQLite files recording the SAME
// kind of run, so `mivia automations runs` could never see a run the
// TUI started and the `serve` sweep could never see a run the TUI left
// interrupted, or vice versa. Sharing the resolver unifies both
// surfaces' run history in one file.
func openAutomationStore(root string, cfg *config.Resolved) (*storage.SQLite, error) {
	return storage.OpenSQLite(clichat.ContextStorePath(root, cfg.Subagents))
}

// buildService constructs the automation.Service used by every
// subcommand, backed by a real HeadlessSpawner. Callers that only need
// read-only access (list/show) still get a full spawner: automation.New
// does not validate spawn eagerly, but building one uniformly keeps this
// helper simple and correct for run/serve too. Returns the concrete
// *HeadlessSpawner alongside the Service so run/serve can call
// CloseLastRun directly without a type assertion.
func buildService(workspaceRoot, configPath string) (*automation.Service, *HeadlessSpawner, func(), error) {
	root, res, err := resolveWorkspaceAndConfig(workspaceRoot, configPath)
	if err != nil {
		return nil, nil, nil, err
	}
	db, err := openAutomationStore(root, res)
	if err != nil {
		return nil, nil, nil, err
	}
	spawn, err := NewHeadlessSpawner(root, res)
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	svc, err := automation.New(root, db, spawn, automation.Config{
		SkillRegistry: skillRegistrySource(root),
	})
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	cleanup := func() { _ = db.Close() }
	return svc, spawn, cleanup, nil
}

// skillRegistrySource builds automation.Config.SkillRegistry's source:
// the SAME loader the interactive session's own binding freezes in
// (cliagents.LoadSessionSkills - the launch attach's skillRegFull path),
// project skills allowed because an automation's spec lives IN the
// project and names project skills. The headless session composition
// builds carries no skill registry of its own (composition.BuildSession
// wires none), so without this fallback every StepSkill dispatch in
// `automations run`/`serve` failed with "no skill registry available"
// while the TUI path - whose pooled sessions carry a binding registry -
// resolved the same step fine.
func skillRegistrySource(root string) func() *skills.Registry {
	return func() *skills.Registry {
		reg, warnings, err := cliagents.LoadSessionSkills(root, true)
		if err != nil {
			return nil
		}
		cliagents.WarnSkillLoad(warnings)
		return reg
	}
}
