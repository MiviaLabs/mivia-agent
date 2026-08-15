package cli

// Read-only session-catalog commands: list, show, delete. These never need a
// live provider/API key, so they build a *chat.Session with a nil completer
// (safe for construction - chat.NewSession only branches on c != nil for a
// naming fallback, never calls anything on c) and wire it to exactly the
// context-catalog storage `mivia chat` uses (setupChatSessionContext), minus
// everything a live chat also sets up: no workspace chdir, no skills/agents,
// no hooks, no provider.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// sessionsShowPreviewLimit bounds a persisted message's content/tool-call-
// argument text in "sessions show" output. Larger than the live NDJSON
// preview's 256/512-byte bounds (internal/agent/loop_tool_preview.go) since
// this is a human reading past history, not a streaming progress indicator -
// but still bounded, so a pathologically large stored blob (a `write_file`
// call's content, say) can't dump unbounded output.
const sessionsShowPreviewLimit = 8192

// redactSessionMessagesForDisplay returns copies of msgs with tool-call
// arguments and message content scrubbed via the same redact.Text policy the
// live "chat --json" NDJSON preview uses for tool_start/tool_end (see
// redactToolInput/redactToolOutputForTool in
// internal/agent/loop_tool_preview.go) - persisted session history is raw,
// unredacted provider.Message data (arguments/content exactly as sent to/
// from the model), so a caller printing it (this command's own --json and
// text output, and external consumers like mivia-agent-desktop) must not
// see anything a live turn wouldn't have shown.
//
// Builds fresh ToolCalls slices rather than mutating msgs[i].ToolCalls[j] in
// place: MessagesCopy's copy() is a shallow slice-header copy, so the
// backing ToolCall array is still shared with the live chat.Session this was
// loaded from - mutating an element in place would corrupt that session's
// real history for any process still holding it.
func redactSessionMessagesForDisplay(msgs []provider.Message) []provider.Message {
	out := make([]provider.Message, len(msgs))
	for i, m := range msgs {
		m.Content = truncateForDisplay(redact.Text(m.Content))
		if len(m.ToolCalls) > 0 {
			toolCalls := make([]provider.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				tc.Function.Arguments = truncateForDisplay(redact.Text(tc.Function.Arguments))
				toolCalls[j] = tc
			}
			m.ToolCalls = toolCalls
		}
		out[i] = m
	}
	return out
}

// truncateForDisplay bounds s to sessionsShowPreviewLimit bytes, cutting on a
// UTF-8 rune boundary so a multi-byte character at the cut point is dropped
// whole rather than split into invalid bytes.
func truncateForDisplay(s string) string {
	if len(s) <= sessionsShowPreviewLimit {
		return s
	}
	cut := sessionsShowPreviewLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// defaultSessionsShowLimit bounds "sessions show" to recent history by
// default, matching the task's confirmed requirement: bounded recent
// history, not a full dump of a potentially very long transcript.
const defaultSessionsShowLimit = 50

func runSessions(args []string) error {
	return runSessionsWithIO(args, os.Stdout, os.Stderr)
}

func runSessionsWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("sessions: expected list, show, usage, rename, or delete")
	}
	subcommand, rest := args[0], args[1:]
	switch subcommand {
	case "list":
		return runSessionsList(rest, stdout)
	case "show":
		return runSessionsShow(rest, stdout)
	case "usage":
		return runSessionsUsage(rest, stdout)
	case "rename":
		return runSessionsRename(rest, stdout, stderr)
	case "delete":
		return runSessionsDelete(rest, stdout, stderr)
	default:
		return fmt.Errorf("sessions: unknown subcommand %q", safeCatalogText(subcommand, 80))
	}
}

// catalogReadOnlyCompleter satisfies provider.Completer for
// newCatalogSession's binding factory. It exists purely to be non-nil - see
// that factory's doc comment for why chat.Session.SwitchBinding requires one
// even for a session that will only ever be read. Every method here is
// unreachable from a read-only catalog command; each returns an error rather
// than silently succeeding or panicking, so a future code path that DOES try
// to dispatch through a catalog session fails loudly instead of pretending
// to work.
type catalogReadOnlyCompleter struct {
	providerName string
}

func (c catalogReadOnlyCompleter) Name() string { return c.providerName }

func (c catalogReadOnlyCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", fmt.Errorf("catalog session for provider %q is read-only: cannot dispatch", c.providerName)
}

func (c catalogReadOnlyCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", fmt.Errorf("catalog session for provider %q is read-only: cannot dispatch", c.providerName)
}

func (c catalogReadOnlyCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, fmt.Errorf("catalog session for provider %q is read-only: cannot dispatch", c.providerName)
}

// newCatalogSession builds a session bound to the same context-catalog
// storage a real `mivia chat` invocation under workspaceRoot would use: the
// repository-level store when workspaceRoot sits inside a git repo (mirroring
// runConfiguredChat's repositorySessionStorePath resolution and
// setupChatSessionContext's managed-worktree binding), falling back to the
// plain per-workspace store otherwise. Caller must close the returned store.
func newCatalogSession(workspaceRoot string) (*chat.Session, *storage.SQLite, error) {
	sess, store, _, _, err := newCatalogSessionAt(workspaceRoot)
	return sess, store, err
}

// newCatalogSessionAt is newCatalogSession plus the resolved workspace root
// and config - the extra outputs "sessions usage" needs to wire the same
// tool registry a live resume builds (see runSessionsUsage).
func newCatalogSessionAt(workspaceRoot string) (*chat.Session, *storage.SQLite, string, *config.Resolved, error) {
	root, err := chatWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, nil, "", nil, err
	}
	res, err := config.Load(config.LoadOptions{WorkspaceRoot: root, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, "", nil, err
	}
	// Resolve a REAL initial completer when the workspace's provider and
	// credential resolve, so summaryWiring (which reads binding.Completer)
	// can wire an LLM summarizer for `mivia compact --session` under a
	// [context.summary]-enabled policy. When no provider/key resolves (e.g. a
	// workspace with no API key), keep the structural fallback: a read-only
	// catalog command must still work, and summaryDisabledReason names the
	// cause when compaction stays structural-only. The SetBindingFactory below
	// still supplies catalogReadOnlyCompleter for Load-time SwitchBinding.
	var sess *chat.Session
	if comp, compErr := provider.New(res); compErr == nil {
		sess = chat.NewSession(res, comp)
	} else {
		sess = chat.NewSession(res, nil)
	}
	// Load (used by "show" and internally by DeleteSession's catalog paths)
	// requires a binding factory whenever the workspace configures a model
	// catalog (chat.Session.publishLoadedMessages fails closed otherwise -
	// "session binding factory is required for configured model catalogs").
	//
	// A read-only catalog command never actually dispatches to a provider,
	// but chat.Session.SwitchBinding - invoked here whenever the saved
	// session's provider/model differs from the config's currently active
	// one (see loadContextCatalog's fast-path check) - unconditionally
	// requires a non-nil Completer and a usable prompt budget, regardless of
	// whether the caller ever intends to dispatch through it. Without a real
	// Completer/Profile, "sessions show" on any session saved under a
	// non-default provider/model (e.g. after switching models mid-session)
	// failed outright with "incomplete model binding" - the raw messages
	// were never actually lost, this command just couldn't reach them.
	// catalogReadOnlyCompleter satisfies that non-nil check without ever
	// being invoked; configuredProfile fills in the prompt-budget inputs
	// from the same catalog bindingAllowsLocked already validates against.
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		profile, _ := configuredProfile(res, providerName, model)
		return chat.ModelBinding{
			ProviderName: providerName,
			Model:        model,
			Completer:    catalogReadOnlyCompleter{providerName: providerName},
			Profile:      profile,
		}, nil
	})
	invocation := chatInvocation{workspacePath: root}
	if repoRoot, repoErr := chatRepositoryRoot(root); repoErr == nil {
		storePath, spErr := repositorySessionStorePath(repoRoot, invocation, res)
		if spErr != nil {
			return nil, nil, "", nil, fmt.Errorf("resolve repository session store: %w", spErr)
		}
		invocation.repositorySessionStorePath = storePath
	}
	store, err := setupChatSessionContext(sess, root, invocation, res)
	if err != nil {
		return nil, nil, "", nil, err
	}
	return sess, store, root, res, nil
}

func parseSessionsWorkspaceAndJSON(cmdLabel string, args []string, allowPositional int) (workspaceRoot string, jsonFlag bool, positional []string, err error) {
	rest := args
	workspaceRoot, rest, _, err = flagValue(rest, "--workspace")
	if err != nil {
		return "", false, nil, fmt.Errorf("%s: %w", cmdLabel, err)
	}
	for _, arg := range rest {
		switch {
		case arg == "--json":
			jsonFlag = true
		case strings.HasPrefix(arg, "-"):
			return "", false, nil, fmt.Errorf("%s: unknown flag %q", cmdLabel, safeCatalogText(arg, 80))
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) != allowPositional {
		return "", false, nil, fmt.Errorf("%s: expected %d positional argument(s), got %d", cmdLabel, allowPositional, len(positional))
	}
	return workspaceRoot, jsonFlag, positional, nil
}

func runSessionsList(args []string, stdout io.Writer) error {
	workspaceRoot, jsonFlag, _, err := parseSessionsWorkspaceAndJSON("sessions list", args, 0)
	if err != nil {
		return err
	}
	sess, store, err := newCatalogSession(workspaceRoot)
	if err != nil {
		return fmt.Errorf("sessions list: %w", err)
	}
	defer store.Close()
	infos, err := sess.ListSessions()
	if err != nil {
		return fmt.Errorf("sessions list: %w", err)
	}
	if jsonFlag {
		return writeSessionsJSON(stdout, infos)
	}
	writeSessionsTable(stdout, infos)
	return nil
}

// runSessionsShow loads a saved session by name (the session_id returned by
// "sessions list" for an auto-persisted live session, or an explicit snapshot
// name) and prints the last --limit messages (default
// defaultSessionsShowLimit) as JSON. Messages are provider.Message values
// (role, content, tool_calls, tool_call_id, name, reasoning_content,
// created_at - see internal/provider/provider.go), redacted and bounded via
// redactSessionMessagesForDisplay (same redact.Text policy the live
// "chat --json" NDJSON preview applies) but otherwise unfiltered:
// tool-call/tool-result and any leading system-prompt message are
// included rather than stripped, so a caller reconstructing chat turns (e.g.
// mivia-agent-desktop) gets the full shape it needs instead of a lossy
// user/assistant-only view.
func runSessionsShow(args []string, stdout io.Writer) error {
	rest := args
	var limitStr string
	var err error
	limitStr, rest, _, err = flagValue(rest, "--limit")
	if err != nil {
		return fmt.Errorf("sessions show: %w", err)
	}
	limit := defaultSessionsShowLimit
	if limitStr != "" {
		limit, err = strconv.Atoi(limitStr)
		if err != nil || limit < 0 {
			return fmt.Errorf("sessions show: --limit requires a non-negative integer")
		}
	}
	workspaceRoot, jsonFlag, positional, err := parseSessionsWorkspaceAndJSON("sessions show", rest, 1)
	if err != nil {
		return err
	}
	name := positional[0]
	sess, store, err := newCatalogSession(workspaceRoot)
	if err != nil {
		return fmt.Errorf("sessions show: %w", err)
	}
	defer store.Close()
	if err := sess.LoadReadOnly(name); err != nil {
		return fmt.Errorf("sessions show: %w", err)
	}
	msgs := sess.MessagesCopy()
	if limit > 0 && len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	msgs = redactSessionMessagesForDisplay(msgs)
	if jsonFlag {
		return writeSessionsJSON(stdout, msgs)
	}
	writeSessionsShowText(stdout, msgs)
	return nil
}

// runSessionsUsage reports a saved session's context accounting - the same
// ContextUsage numbers a resumed chat session's TUI status dialog shows,
// computed over the loaded (post-compaction) messages with the session's
// own model binding and tool schemas. The desktop app seeds its context
// indicator from this when a saved thread is reopened; the catalog's stored
// token_count is a whole-session estimate that goes stale the moment
// compaction rewrites the conversation.
func runSessionsUsage(args []string, stdout io.Writer) error {
	workspaceRoot, jsonFlag, positional, err := parseSessionsWorkspaceAndJSON("sessions usage", args, 1)
	if err != nil {
		return err
	}
	sess, store, root, res, err := newCatalogSessionAt(workspaceRoot)
	if err != nil {
		return fmt.Errorf("sessions usage: %w", err)
	}
	defer store.Close()
	if err := sess.LoadReadOnly(positional[0]); err != nil {
		return fmt.Errorf("sessions usage: %w", err)
	}
	// The same tool registry a live resume builds (configureChatWorkspace),
	// so the estimate includes tool schemas exactly like the TUI's number
	// instead of silently under-counting by their cost. quiet=true: a
	// usage query is not a session start and must not print startup
	// notices into the JSON stream.
	cleanup, err := configureChatWorkspace(sess, root, true, res, &agentSessionState{}, true, false)
	defer cleanup()
	if err != nil {
		return fmt.Errorf("sessions usage: %w", err)
	}
	usage := sess.ContextUsage()
	if jsonFlag {
		return writeSessionsJSON(stdout, map[string]any{
			"used_tokens":           usage.UsedTokens,
			"budget_tokens":         usage.BudgetTokens,
			"context_window_tokens": usage.ContextWindowTokens,
			"output_reserve_tokens": usage.OutputReserveTokens,
			"percent":               usage.Percent,
		})
	}
	fmt.Fprintf(stdout, "context: %d%% used, %s/%s prompt, window %s, output reserve %s\n",
		usage.Percent, chat.FormatTokenK(usage.UsedTokens), chat.FormatTokenK(usage.BudgetTokens),
		chat.FormatTokenK(usage.ContextWindowTokens), chat.FormatTokenK(usage.OutputReserveTokens))
	return nil
}

// runSessionsRename sets a saved session's display title. The session's own
// id/name is never changed - only chat.SessionInfo.Title, the human-facing
// label "sessions list" and a sidebar would show in place of the raw id.
// With --json a success writes {"renamed":{"session":...,"title":...}} to
// stdout: a frontend confirms the stored title without inferring success
// from the exit code.
func runSessionsRename(args []string, stdout, stderr io.Writer) error {
	workspaceRoot, jsonFlag, positional, err := parseSessionsWorkspaceAndJSON("sessions rename", args, 2)
	if err != nil {
		return err
	}
	name, title := positional[0], positional[1]
	sess, store, err := newCatalogSession(workspaceRoot)
	if err != nil {
		return fmt.Errorf("sessions rename: %w", err)
	}
	defer store.Close()
	if err := sess.SetContextSessionTitle(name, title); err != nil {
		fmt.Fprintf(stderr, "sessions rename: %v\n", err)
		return fmt.Errorf("sessions rename: %w", err)
	}
	if jsonFlag {
		return writeSessionsJSON(stdout, map[string]any{
			"renamed": map[string]string{"session": name, "title": title},
		})
	}
	return nil
}

// runSessionsDelete removes a saved session. With --json a success writes
// {"deleted":"<name>"} to stdout - same frontend contract as rename.
func runSessionsDelete(args []string, stdout, stderr io.Writer) error {
	workspaceRoot, jsonFlag, positional, err := parseSessionsWorkspaceAndJSON("sessions delete", args, 1)
	if err != nil {
		return err
	}
	name := positional[0]
	sess, store, err := newCatalogSession(workspaceRoot)
	if err != nil {
		return fmt.Errorf("sessions delete: %w", err)
	}
	defer store.Close()
	if err := sess.DeleteSession(name); err != nil {
		fmt.Fprintf(stderr, "sessions delete: %v\n", err)
		return fmt.Errorf("sessions delete: %w", err)
	}
	if jsonFlag {
		return writeSessionsJSON(stdout, map[string]any{"deleted": name})
	}
	return nil
}

func writeSessionsJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

func writeSessionsTable(w io.Writer, infos []chat.SessionInfo) {
	if len(infos) == 0 {
		fmt.Fprintln(w, "(no saved sessions)")
		return
	}
	for _, info := range infos {
		label := info.Name
		if info.Title != "" {
			label = fmt.Sprintf("%s (%s)", info.Name, info.Title)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\tmsgs=%d\tupdated=%s\n", label, info.Provider, info.Model, info.MessageCount, info.UpdatedAt.Format("2006-01-02 15:04:05"))
	}
}

func writeSessionsShowText(w io.Writer, msgs []provider.Message) {
	for _, m := range msgs {
		fmt.Fprintf(w, "[%s] %s\n", m.Role, m.Content)
	}
}
