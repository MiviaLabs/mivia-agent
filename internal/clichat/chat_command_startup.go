package clichat

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"golang.org/x/term"
)

// applyPrivacyPolicy installs the process-wide privacy settings.
//
// Tool-argument redaction is opt-in and read from BOTH [privacy] and [tools]
// so either TOML path works. The redaction policy is nil when the workspace
// configured no patterns, which redacts nothing - see rule 10.
func applyPrivacyPolicy(res *config.Resolved) {
	tools.SetRedactToolArgs(res.Privacy.RedactToolArgs || res.Tools.RedactToolArgs)
	redact.SetPolicy(res.RedactionPolicy)
	applyContextLimits(res)
}

// applyContextLimits installs the operator's durable ceilings process-wide.
// It sits beside the redaction policy deliberately: both are workspace policy
// this binary must not invent, and a process that configures neither runs
// uncapped and unredacted rather than under a compiled-in guess.
func applyContextLimits(res *config.Resolved) {
	contextstate.SetLimits(contextstate.Limits{
		SourceEventBytes:        res.Context.MaxSourceEventBytes,
		CheckpointBytes:         res.Context.MaxCheckpointBytes,
		CommitEvents:            res.Context.MaxCommitEvents,
		CommitEventBytes:        res.Context.MaxCommitEventBytes,
		SessionStateBytes:       res.Context.MaxSessionStateBytes,
		ExportBytes:             res.Context.MaxExportBytes,
		SummaryMetadataBytes:    res.Context.SummaryMetadataBytes,
		CheckpointMetadataBytes: res.Context.CheckpointMetadataBytes,
	})
}

type chatInvocation struct {
	prompt, provider, model, configPath, workspacePath, resumeSessionName, repositorySessionStorePath string
	agent                                                                                             string
	// session is --session <name>: resume a saved session (by the session_id
	// or snapshot name `mivia sessions list` reports) before the surface
	// starts. An unknown name fails closed - see runConfiguredChatOnce -
	// rather than silently starting a fresh session under that name, because
	// this codebase never lets a caller choose a session's identity (new
	// sessions always mint a fresh id via RotateSessionID); --session only
	// resumes an id/name that already exists.
	session                  string
	expectedWorktreeInstance contextstate.WorktreeInstance
	// staleBypass records that the removed --bypass-hook-trust flag was passed,
	// so the session can say the flag no longer does anything.
	staleBypass                            bool
	allowProgram, denyProgram, disableTool []string
	allowEnvVar, denyEnvVar                []string
	noTools, plainUI                       bool
	// quiet is --quiet: suppress informational startup notices on stderr
	// (limits summary, lifecycle-hooks armed notice, diagnostics commands
	// line, one-shot/REPL banner). Genuine config warnings and workflow
	// session-recovery diagnostics still print.
	quiet bool
	// jsonMode is --json: reframe line-mode's stdout as NDJSON events instead
	// of raw streamed text. Only valid for the non-interactive piped-stdin
	// line-mode path - runConfiguredChatOnce rejects it for one-shot -p and
	// for the interactive TUI/classic-REPL paths.
	jsonMode bool
	// fullDisk is --full-disk: lift workspace confinement so file tools may
	// access anywhere on the filesystem. Operator-invocation only.
	fullDisk       bool
	approvalPolicy string
	yolo           bool
}

func parseChatInvocation(args []string) (chatInvocation, error) {
	var invocation chatInvocation
	var err error
	invocation.prompt, args, _, err = FlagValueFunc(args, "-p", "--prompt")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.provider, args, _, err = FlagValueFunc(args, "--provider")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.model, args, _, err = FlagValueFunc(args, "--model")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.configPath, args, _, err = FlagValueFunc(args, "--config")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.workspacePath, args, _, err = FlagValueFunc(args, "--workspace")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.agent, args, _, err = FlagValueFunc(args, "--agent")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.session, args, _, err = FlagValueFunc(args, "--session")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.allowProgram, args, _, err = FlagVarFunc(args, "--allow-program")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.denyProgram, args, _, err = FlagVarFunc(args, "--deny-program")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.disableTool, args, _, err = FlagVarFunc(args, "--disable-tool")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.allowEnvVar, args, _, err = FlagVarFunc(args, "--allow-env-var")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.denyEnvVar, args, _, err = FlagVarFunc(args, "--deny-env-var")
	if err != nil {
		return chatInvocation{}, err
	}
	// --approval-policy speaks the LEGACY write-only/auto/always vocabulary
	// (config.NormalizeApprovalPolicy), not the TUI settings screen's
	// once/always/deny vocabulary (config.NormalizeDefaultMode) - "always"
	// means opposite things in the two: here it means "prompt for every
	// call, including reads" (paranoid mode); in the TUI it means "accept
	// every call". See internal/config/approvals_config.go's package
	// comment for the full collision rationale. Use --yolo or
	// --approval-policy auto for "accept everything" from the CLI.
	invocation.approvalPolicy, args, _, err = FlagValueFunc(args, "--approval-policy")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.noTools, invocation.plainUI, invocation.staleBypass, invocation.jsonMode, invocation.quiet, invocation.fullDisk, invocation.yolo, args = chatFlags(args)
	if len(args) > 0 {
		return chatInvocation{}, fmt.Errorf("chat: unexpected arguments: %v", args)
	}
	return invocation, nil
}

// prepareChatStartup runs the pre-session startup policy: the API key gate,
// the tool/override application, the privacy policy, and the once-per-process
// effective-limits notice. Split out of runConfiguredChatOnce to keep the setup
// path under the per-function line budget.
func prepareChatStartup(res *config.Resolved, invocation chatInvocation) (bool, error) {
	if (!res.APIKeySet || strings.TrimSpace(res.APIKey) == "") && !(res.ProviderName == "ollama" && config.IsOllamaLoopback(res.BaseURL)) {
		return false, fmt.Errorf("missing API key: set %s in environment or env file (see mivia doctor)", res.APIKeyEnv)
	}
	applyChatToolOverrides(res, invocation.allowProgram, invocation.denyProgram, invocation.disableTool, invocation.allowEnvVar, invocation.denyEnvVar)
	useTools := !invocation.noTools
	applyPrivacyPolicy(res)
	logEffectiveLimitsOnce(os.Stderr, res, invocation.quiet)
	effectiveFullDisk := chatFullDisk(invocation, invocation.workspacePath)
	if effectiveFullDisk && !invocation.quiet {
		fmt.Fprintln(os.Stderr, "workspace: FULL DISK ACCESS — file tools are not confined to the workspace")
	}
	return useTools, nil
}

// chatFullDisk resolves the session's effective full-disk request: the
// invocation flag, OR the operator's own user config ([workspace_access]
// full_disk, read from UserConfigPath ONLY - config.UserFullDiskAccessForWorkspace
// fails closed and refuses a workspace-controlled file). A workspace's own
// .mivia/mivia.toml can never lift its own confinement, and the loud
// startup notice above fires for EITHER source: lifting confinement is
// never silent, no matter which provenance granted it.
func chatFullDisk(invocation chatInvocation, workspaceRoot string) bool {
	if invocation.fullDisk {
		return true
	}
	return config.UserFullDiskAccessForWorkspace(workspaceRoot)
}

// validateJSONModeInvocation fails closed on --json combined with any path
// other than non-interactive piped line-mode: one-shot -p mode never reaches
// the REPL at all, and the interactive TUI/classic-REPL paths (stdin is a
// real terminal) print prompts, banners and rendered UI to stdout that would
// be interleaved with - and indistinguishable from - the NDJSON stream.
// --json's NDJSON framing (see ndjsonEvent) is only meaningful when stdout is
// nothing but that stream, which line-mode is the one path that guarantees.
func validateJSONModeInvocation(invocation chatInvocation) error {
	if invocation.prompt != "" {
		return fmt.Errorf("chat: --json is not supported with -p/--prompt (one-shot mode); it only applies to non-interactive piped chat")
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("chat: --json is not supported for the interactive REPL/TUI; pipe input via stdin (non-interactive) to use --json")
	}
	return nil
}

func applyChatToolOverrides(res *config.Resolved, allow, deny, disable, allowEnv, denyEnv []string) {
	res.Tools.RunAllowlist = append(res.Tools.RunAllowlist, allow...)
	res.Tools.RunBlocklist = append(res.Tools.RunBlocklist, deny...)
	res.Tools.DisableTools = append(res.Tools.DisableTools, disable...)
	res.Tools.EnvAllowlist = append(res.Tools.EnvAllowlist, allowEnv...)
	res.Tools.EnvBlocklist = append(res.Tools.EnvBlocklist, denyEnv...)
}
