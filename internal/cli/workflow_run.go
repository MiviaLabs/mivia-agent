package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var (
	workflowRunBuild        = buildWorkflowController
	workflowRunSetAdmission = func(b workflowControllerBuild) error {
		return b.Controller.SetAdmission(b.Admission)
	}
	workflowBuildLoadSkills = loadChatSkills
	workflowBuildLoadAgents = loadAgentDefinitions
	workflowBuildRegistry   = workflowDefaultRegistry
	workflowBuildWorkspace  = selectWorkflowWorkspace
	workflowBuildProvider   = provider.New
	workflowBuildDispatcher = NewSessionDispatcher
	workflowBuildController = controller.NewLinearController
)

func runWorkflow(args []string) error {
	// fd-2 progress writes must degrade, not kill, the run: a broken stderr
	// pipe (mivia workflow run 2> >(head -1)) would otherwise raise SIGPIPE
	// and terminate the process before the run settles.
	signal.Ignore(syscall.SIGPIPE)
	return runWorkflowWithIO(args, os.Stdout, os.Stderr)
}

func runWorkflowWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("workflow: expected run, runs, resume, deliver, status, events, approve, reject, cancel, cleanup, or delete")
	}
	var workspaceRoot, configPath string
	var err error
	workspaceRoot, args, _, err = flagValue(args, "--workspace")
	if err != nil {
		return err
	}
	configPath, args, _, err = flagValue(args, "--config")
	if err != nil {
		return err
	}
	force := false
	filtered := args[:0]
	for _, arg := range args {
		if arg == "--force" {
			force = true
			continue
		}
		filtered = append(filtered, arg)
	}
	args = filtered
	if len(args) == 0 {
		return fmt.Errorf("workflow: expected run, runs, resume, deliver, status, events, approve, reject, cancel, cleanup, or delete")
	}
	switch args[0] {
	case "run":
		return runWorkflowCommandRun(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "runs":
		return runWorkflowCommandRuns(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "deliver":
		return runWorkflowCommandDeliver(args[1:], workspaceRoot, configPath, force, stdout, stderr)
	case "resume":
		return runWorkflowCommandResume(args[1:], workspaceRoot, configPath, force, stdout, stderr)
	case "status":
		return runWorkflowCommandStatus(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "events":
		return runWorkflowCommandEvents(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "approve":
		return runWorkflowCommandApprove(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "reject":
		return runWorkflowCommandReject(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "cancel":
		return runWorkflowCommandCancel(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "cleanup":
		return runWorkflowCommandCleanup(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "delete":
		return runWorkflowCommandDelete(args[1:], workspaceRoot, configPath, force, stdout, stderr)
	default:
		return fmt.Errorf("workflow: unknown subcommand %q", args[0])
	}
}

func executeWorkflowRun(name, root, configPath string, rawInputs []string, allowPublish bool, stdout, stderr io.Writer) error {
	prepared, err := prepareWorkflowRun(name, root, configPath, rawInputs)
	if err != nil {
		return err
	}
	logMCPWarnings(stderr, prepared.res)
	defer prepared.closeFn()
	runID := newCLIWorkflowRunID()
	finishExecution, err := beginWorkflowExecution(prepared.root, contextStorePath(prepared.root, prepared.res.Subagents), runID)
	if err != nil {
		return err
	}
	defer finishExecution()
	built, err := workflowRunBuild(prepared.root, prepared.res, prepared.store, prepared.repo, prepared.compiled, prepared.refBase, prepared.inputs, prepared.inputSnapshot, prepared.raw, runID, nil, nil)
	if err != nil {
		return err
	}
	defer built.Dispatcher.Close()
	admitted := false
	defer func() {
		if !admitted {
			built.Cleanup()
		}
	}()
	if err := workflowRunSetAdmission(built); err != nil {
		return err
	}
	wireCLIWorkflowProgress(&built, stderr)
	if err := built.Controller.Start(context.Background()); err != nil {
		return err
	}
	admitted = true
	snap, err := built.Controller.Run(context.Background())
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", built.Controller.RunID, snap.Status)
	if err != nil {
		// A genuine (non-deadline) fault that stops the controller must settle
		// the run: Controller.Run self-settles deadline errors, cancel owns
		// cancelled runs, but a raw storage/claim fault would otherwise leave
		// the run row `running` with no cause (DC-9).
		settleCLIRunFailure(prepared.repo, built.Controller.RunID, err)
		return err
	}
	if snap.Status == workflowledger.RunStatusDeliveryPending {
		mode := ""
		if prepared.compiled.Delivery != nil {
			mode = prepared.compiled.Delivery.Mode
		}
		return finishWorkflowRunDelivery(context.Background(), prepared.root, prepared.res, prepared.store, prepared.repo, built.Controller.RunID, name, mode, allowPublish, stdout, stderr)
	}
	return nil
}

// preparedWorkflowRun is the immutable input of one workflow run invocation:
// the opened workspace, store, and compiled definition.
type preparedWorkflowRun struct {
	root          string
	res           *config.Resolved
	store         *storage.SQLite
	repo          workflowledger.Repository
	closeFn       func()
	compiled      *compiler.CompiledWorkflow
	inputs        map[string]any
	inputSnapshot map[string]string
	refBase       string
	raw           []byte
}

// prepareWorkflowRun opens the workspace and store and compiles the named
// workflow with validated inputs, before any execution begins.
func prepareWorkflowRun(name, root, configPath string, rawInputs []string) (*preparedWorkflowRun, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return nil, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
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
	wf, _, err := definition.ParseWorkflowTOML(found.Raw, found.Name+".toml")
	if err != nil {
		closeFn()
		return nil, err
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		closeFn()
		return nil, err
	}
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
	return &preparedWorkflowRun{
		root: work.Abs, res: res, store: store, repo: repo, closeFn: closeFn,
		compiled: compiled, inputs: inputs, inputSnapshot: inputSnapshot,
		refBase: filepath.Dir(found.Path), raw: found.Raw,
	}, nil
}

// finishWorkflowRunDelivery completes a run that settled at delivery_pending:
// with --allow-publish it performs delivery and prints the settled status;
// without it prints the non-publication explanation.
func finishWorkflowRunDelivery(ctx context.Context, root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, runID, workflowName, mode string, allowPublish bool, stdout, stderr io.Writer) error {
	if allowPublish {
		if err := deliverRunWithStore(ctx, root, res, store, repo, runID, allowPublish, false, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "workflow delivery failed: %v\n", err)
			return err
		}
		settled, err := repo.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
		return nil
	}
	fmt.Fprintf(stdout, "workflow %s reached its success terminal; delivery mode=%s requires --allow-publish\n", workflowName, mode)
	fmt.Fprintf(stdout, "deliver with: mivia workflow deliver %s --allow-publish\n", runID)
	return nil
}

func openWorkflowStore(root string, cfg config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
	store, err := openContextStore(root, cfg)
	if err != nil {
		return nil, nil, func() {}, err
	}
	return store, workflowledger.NewStorageRepository(store), func() { _ = store.Close() }, nil
}

func workflowConfigPath(root, explicit string) string {
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

func applyWorkflowStoreRoot(res *config.Resolved, root string) {
	if res != nil && res.Subagents.StoreBackend == "sqlite" && !res.StorePathSet {
		res.Subagents.StorePath = workspace.ContextStorePath(root)
	}
}
