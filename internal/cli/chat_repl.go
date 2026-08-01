package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func chatFlags(args []string) (noTools, plainUI, staleBypass bool, rest []string) {
	for _, arg := range args {
		switch arg {
		case "--no-tools":
			noTools = true
		case "--plain":
			plainUI = true
		case "--bypass-hook-trust":
			// Accepted and ignored. The flag existed to run hooks that were
			// never confirmed; there is no confirmation to bypass any more.
			// Rejecting it would break the CI configs it was written for, and
			// those are the runs least able to explain a startup failure.
			staleBypass = true
		default:
			rest = append(rest, arg)
		}
	}
	return noTools, plainUI, staleBypass, rest
}

func configureChatWorkspace(sess *chat.Session, root string, useTools bool, tavilyKey string, tc config.ToolsConfig) error {
	if !useTools {
		return nil
	}
	ws, err := workspace.Open(root)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	opts := tools.DefaultOptions{
		Workspace:                ws,
		TavilyAPIKey:             tavilyKey,
		RunAllowlist:             tc.RunAllowlist,
		RunAllowlistOnly:         tc.RunAllowlistOnly,
		RunBlocklist:             tc.RunBlocklist,
		DisableTools:             tc.DisableTools,
		EnvAllowlist:             tc.EnvAllowlist,
		EnvAllowlistOnly:         tc.EnvAllowlistOnly,
		EnvBlocklist:             tc.EnvBlocklist,
		EnvAllowKeywordBlocklist: tc.EnvAllowKeywordBlocklist,
		RunTimeoutSec:            tc.RunTimeoutSec,
		MaxReadBytes:             tc.MaxReadBytes,
		MaxWriteKB:               tc.MaxWriteKB,
		MaxOutputBytes:           tc.MaxOutputBytes,
		MaxListDirEntries:        tc.MaxListDirEntries,
		MaxToolResultBytes:       tc.MaxToolResultBytes,
		MaxTavilyResponseBytes:   tc.MaxTavilyResponseBytes,
		// RedactToolArgs is NOT plumbed here - the single source of truth
		// is the package atomic set by tools.SetRedactToolArgs at line 40.
		SecretPathPatterns:   tc.SecretPathPatterns,
		SecretPathExceptions: tc.SecretPathExceptions,
	}
	sess.Tools = tools.NewDefaultRegistry(opts)
	return nil
}

// attachSessionDispatcher wires NewSessionDispatcher onto the session using the
// shared agent-aware builder (same contract as model switch). skillReg may be
// pre-loaded by the caller so agent/skill collisions were already checked.
// When state is non-nil, ToolBase is captured before root agent scope so
// mid-session /agent can re-scope without losing tools. Agent scope is applied
// BEFORE building the dispatcher so the dispatcher and sess.Tools agree
// (INV-AG-29 execution denial).
// sessionRouting carries what a routed agent needs to bind its own provider:
// the catalog that authorizes a (provider, model) pair and the factory that
// constructs a completer for it. The zero value authorizes nothing, so an
// agent declaring a foreign binding fails closed rather than silently running
// on the session's provider.
type sessionRouting struct {
	Catalog          []config.ProviderModelGroup
	CompleterFactory func(providerName, model string) (provider.Completer, error)
}

func attachSessionDispatcher(sess *chat.Session, root, model string, cfg config.SubagentConfig, state *agentSessionState, skillReg *skills.Registry, routing sessionRouting) (func(), error) {
	if sess == nil {
		return func() {}, nil
	}
	sess.SetSwitchGuard(orchestrationSwitchGuard(sess.SessionID))
	binding := sess.CurrentBinding()
	if binding.Completer == nil {
		return nil, fmt.Errorf("dispatcher: nil completer")
	}
	ctx := agentSessionContext{}
	if state != nil {
		ctx = state.context()
	}
	if skillReg == nil {
		var warnings []string
		var err error
		skillReg, warnings, err = loadSessionSkills(root, ctx.AllowProjectSkills)
		if err != nil {
			return nil, fmt.Errorf("load skills: %w", err)
		}
		warnSkillLoad(warnings)
	}
	skillReg = filterSkillRegistryForGate(skillReg, ctx.AllowProjectSkills)
	skillScope := skillScopeFromAgent(ctx.Selected)
	modelCatalog := routing.Catalog
	// The TUI binding must reflect the root agent's policy. Keep skillReg itself
	// complete for explicitly routed task agents, which validate their own scope.
	sess.SetBindingSkillRegistry(filterSkillsForScope(skillReg, skillScope))
	if sess.Tools == nil {
		return func() {}, nil
	}
	// Snapshot the full post-registration registry BEFORE root agent scope.
	// This is the base for mid-session /agent re-scope; it must include all
	// tools so switching to a wider agent can regain them.
	if state != nil {
		state.ToolBase = sess.Tools.Clone()
	}
	// Apply root agent scope BEFORE building the dispatcher so the dispatcher
	// captures a scoped registry. This keeps the dispatcher and sess.Tools in
	// agreement - a tool absent from sess.Tools is also absent from the
	// dispatcher's executable registry (INV-AG-29 execution denial).
	applyRootAgentScope(sess, ctx.Selected, ctx.Global.MandatoryToolDenylistAdditions)
	// Rebuild the skill policy against the final live registry (plan 43) so a
	// skill requiring a disabled/denied tool cannot activate, and store it for
	// the TUI slash path.
	liveScope := skillScopeFromAgentAndRegistry(ctx.Selected, sess.Tools)
	if state != nil {
		state.setSkillScope(liveScope)
	}
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:            sess.Tools,
		Completer:           binding.Completer,
		Model:               model,
		ProviderName:        binding.ProviderName,
		ModelGeneration:     binding.ModelGeneration,
		ModelGenerationFunc: sess.CurrentModelGeneration,
		ModelCatalog:        modelCatalog,
		CompleterFactory:    routing.CompleterFactory,
		Config:              cfg,
		ToolResultCapBytes:  sess.MaxToolResultChars,
		WorkspaceRoot:       root,
		MaxContextTokens:    sess.PromptBudget(),
		MaxTokens:           sess.MaxTokens,
		Budget:              sess.PromptBudget,
		SkillReg:            skillReg,
		SkillScope:          liveScope,
		AgentRegistry:       ctx.Registry,
	})
	if err != nil {
		return nil, fmt.Errorf("dispatcher: %w", err)
	}
	sess.SetDispatcher(dispatcher)
	return func() { dispatcher.Close() }, nil
}

func repl(sess *chat.Session, res *config.Resolved, toolsOn bool, _ *agentSessionState) error {
	printReplBanner(sess, toolsOn)
	defer autoSaveREPL(sess)
	term, err := NewTerminal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: not a terminal (%v), falling back to line mode\n", err)
		return replLineMode(sess, res, toolsOn)
	}
	defer term.Close()
	r := newREPLRuntime(sess, res, toolsOn, term)
	return r.run()
}

func printReplBanner(sess *chat.Session, toolsOn bool) {
	mode := "chat"
	if toolsOn {
		mode = "agent"
	}
	fmt.Fprintf(os.Stderr, "mivia %s  provider=%s model=%s%s\n", mode, sess.CurrentSelection().ProviderName, sess.CurrentBinding().Model, formatSessionAgentStatus(classicAgentState, sess))
	if toolsOn {
		fmt.Fprintln(os.Stderr, "Tools on. /tools /workspace /help - Ctrl-C cancel or exit at prompt.")
	} else {
		fmt.Fprintln(os.Stderr, "Tools off (--no-tools). /help - Ctrl-C cancel or exit at prompt.")
	}
}

func autoSaveREPL(sess *chat.Session) {
	err := sess.SaveLast()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ auto-save failed: %v\n", err)
	}
	writeAutosaveStatus(sess.SessionDir, err)
}

func replLineMode(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if handled, exit, herr := handleSlash(line, sess, res, toolsOn, nil); handled {
			if herr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", herr)
			}
			if exit {
				return nil
			}
			continue
		}
		if line == "exit" || line == "quit" {
			return nil
		}
		if err := sendLineMode(sess, line, sigCh); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
	return sc.Err()
}

func sendLineMode(sess *chat.Session, line string, sigCh <-chan os.Signal) error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go cancelOnInterrupt(ctx, cancel, done, sigCh)
	fmt.Fprintf(os.Stderr, "  (~%d tokens in history)\n", provider.MessagesTokens(sess.Messages))
	_, err := sess.SendUser(ctx, line, os.Stdout)
	close(done)
	cancel()
	fmt.Fprintln(os.Stdout)
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "(cancelled)")
		return nil
	}
	return err
}

func cancelOnInterrupt(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, sigCh <-chan os.Signal) {
	select {
	case <-sigCh:
		cancel()
	case <-done:
	case <-ctx.Done():
	}
}
