package cliworkflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// PreparedWorkflowRun is the immutable input of one workflow run invocation:
// the opened workspace, store, and compiled definition.
type PreparedWorkflowRun struct {
	Root          string
	Res           *config.Resolved
	Store         *storage.SQLite
	Repo          workflowledger.Repository
	CloseFn       func()
	Compiled      *definition.CompiledWorkflow
	Inputs        map[string]any
	InputSnapshot map[string]string
	RefBase       string
	Raw           []byte
}

// PrepareWorkflowRun opens the workspace and store and compiles the named
// workflow with validated inputs, before any execution begins.
func PrepareWorkflowRun(name, root, configPath string, rawInputs []string) (*PreparedWorkflowRun, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, err
	}
	configPath = WorkflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return nil, err
	}
	ApplyPrivacyPolicyFunc(res)
	ApplyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := OpenWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, err
	}
	workflows, err := definition.DiscoverWorkflows(work.Abs)
	if err != nil {
		closeFn()
		return nil, err
	}
	var found *definition.DiscoveredWorkflow
	for i := range workflows {
		if workflows[i].Name == name {
			found = &workflows[i]
			break
		}
	}
	if found == nil {
		closeFn()
		return nil, fmt.Errorf("workflow %q was not found", name)
	}
	// A discovered-but-unusable file (symlink, oversize, unreadable) carries
	// its reason and no bytes; parsing it would report a confusing TOML error
	// instead of the real one.
	if found.Err != nil {
		closeFn()
		return nil, found.Err
	}
	wf, _, err := definition.ParseWorkflowTOML(found.Raw, found.Name+".toml")
	if err != nil {
		closeFn()
		return nil, err
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		closeFn()
		return nil, err
	}
	// A stacking workflow accepts the engine-reserved inputs (stack_mode,
	// chunk, ...) at admission too, so the operator override the controller
	// supports (e.g. --input stack_mode=single) validates against the same
	// input contract as resume. A no-op for non-stacking workflows.
	definition.MergeStackingInputs(compiled)
	inputs, inputSnapshot, err := parseWorkflowInputs(rawInputs, compiled.Inputs)
	if err != nil {
		closeFn()
		return nil, err
	}
	// Fail fast before any agent runs: a fresh run whose inputs instruct a
	// write to a host write-blocklisted path can never satisfy itself (the
	// write tools refuse), so it would spin implement -> review -> blocked
	// implement until a misattributed failure. Refuse admission instead and
	// route the change through the root session or a host-owned process.
	if err := workflowBlockedInputAdmission(effectiveWorkflowWriteDenylist(res), compiled.Name, inputs); err != nil {
		closeFn()
		return nil, err
	}
	return &PreparedWorkflowRun{
		Root: work.Abs, Res: res, Store: store, Repo: repo, CloseFn: closeFn,
		Compiled: compiled, Inputs: inputs, InputSnapshot: inputSnapshot,
		RefBase: filepath.Dir(found.Path), Raw: found.Raw,
	}, nil
}

func OpenWorkflowStore(root string, cfg config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
	store, err := OpenContextStoreFunc(root, cfg)
	if err != nil {
		return nil, nil, func() {}, err
	}
	return store, workflowledger.NewStorageRepository(store), func() { _ = store.Close() }, nil
}

// The pure path comparison lives in cliagents.SameFilePath.

func WorkflowConfigPath(root, explicit string) string {
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

// ApplyWorkflowStoreRoot pins workflow execution state to the workspace,
// regardless of the chat/session store's default. Workflow run IDs and
// locks are not namespaced across projects the way chat sessions are (see
// contextWorkspaceID), so letting them fall back to the shared global store
// would let unrelated workflow runs in different repos collide.
//
// An explicitly set RELATIVE store_path is .mivia namespace notation for the
// workspace, not process-cwd notation: anchor it to the resolved root too,
// so every surface that loads config through here opens one store per
// workspace instead of one per working directory.
//
// A leading ~ expands before relativity is classified: "~/..." names the
// user-global store shared across workspaces (docs/product/config.md), so
// it never lands under <root>/~/. Expansion is tilde-only; $VAR text stays
// literal.
func ApplyWorkflowStoreRoot(res *config.Resolved, root string) {
	if res == nil {
		return
	}
	if !res.StorePathSet {
		if config.ProjectConfigExists(root) {
			res.Subagents.StorePath = workspace.ContextStorePath(root)
		} else {
			res.Subagents.StorePath = config.TempStorePath(root, "orchestration")
		}
		return
	}
	if p := res.Subagents.StorePath; p != "" {
		p = config.ExpandPath(p)
		res.Subagents.StorePath = p
		// A separator-rooted path ("/abs/x.db") carries no volume letter, so
		// filepath.IsAbs reports false for it on Windows. A user who wrote a
		// rooted path means an anchored location; joining it under the store
		// root would silently turn it into "<root>/abs/x.db". Unix behavior
		// is unchanged: IsAbs already covers rooted paths there.
		rooted := strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\")
		if root != "" && !filepath.IsAbs(p) && !rooted {
			res.Subagents.StorePath = filepath.Join(root, p)
		}
	}
}
