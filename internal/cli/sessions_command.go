package cli

// Read-only session-catalog commands: list, show, delete. These never need a
// live provider/API key, so they build a *chat.Session with a nil completer
// (safe for construction - chat.NewSession only branches on c != nil for a
// naming fallback, never calls anything on c) and wire it to exactly the
// context-catalog storage `mivia chat` uses (setupChatSessionContext), minus
// everything a live chat also sets up: no workspace chdir, no skills/agents,
// no hooks, no provider.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// defaultSessionsShowLimit bounds "sessions show" to recent history by
// default, matching the task's confirmed requirement: bounded recent
// history, not a full dump of a potentially very long transcript.
const defaultSessionsShowLimit = 50

func runSessions(args []string) error {
	return runSessionsWithIO(args, os.Stdout, os.Stderr)
}

func runSessionsWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("sessions: expected list, show, rename, or delete")
	}
	subcommand, rest := args[0], args[1:]
	switch subcommand {
	case "list":
		return runSessionsList(rest, stdout)
	case "show":
		return runSessionsShow(rest, stdout)
	case "rename":
		return runSessionsRename(rest, stderr)
	case "delete":
		return runSessionsDelete(rest, stderr)
	default:
		return fmt.Errorf("sessions: unknown subcommand %q", safeCatalogText(subcommand, 80))
	}
}

// newCatalogSession builds a session bound to the same context-catalog
// storage a real `mivia chat` invocation under workspaceRoot would use: the
// repository-level store when workspaceRoot sits inside a git repo (mirroring
// runConfiguredChat's repositorySessionStorePath resolution and
// setupChatSessionContext's managed-worktree binding), falling back to the
// plain per-workspace store otherwise. Caller must close the returned store.
func newCatalogSession(workspaceRoot string) (*chat.Session, *storage.SQLite, error) {
	root, err := chatWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, nil, err
	}
	res, err := config.Load(config.LoadOptions{WorkspaceRoot: root, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, err
	}
	sess := chat.NewSession(res, nil)
	// Load (used by "show" and internally by DeleteSession's catalog paths)
	// requires a binding factory whenever the workspace configures a model
	// catalog (chat.Session.publishLoadedMessages fails closed otherwise -
	// "session binding factory is required for configured model catalogs").
	// A read-only catalog command never actually dispatches to a provider, so
	// this factory only needs to name the binding, not construct a working
	// completer.
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		return chat.ModelBinding{ProviderName: providerName, Model: model}, nil
	})
	invocation := chatInvocation{workspacePath: root}
	if repoRoot, repoErr := chatRepositoryRoot(root); repoErr == nil {
		storePath, spErr := repositorySessionStorePath(repoRoot, invocation, res)
		if spErr != nil {
			return nil, nil, fmt.Errorf("resolve repository session store: %w", spErr)
		}
		invocation.repositorySessionStorePath = storePath
	}
	store, err := setupChatSessionContext(sess, root, invocation, res)
	if err != nil {
		return nil, nil, err
	}
	return sess, store, nil
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
// defaultSessionsShowLimit) as JSON. Messages are the raw
// provider.Message values (role, content, tool_calls, tool_call_id, name,
// reasoning_content, created_at - see internal/provider/provider.go), printed
// unfiltered: tool-call/tool-result and any leading system-prompt message are
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
	if err := sess.Load(name); err != nil {
		return fmt.Errorf("sessions show: %w", err)
	}
	msgs := sess.MessagesCopy()
	if limit > 0 && len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	if jsonFlag {
		return writeSessionsJSON(stdout, msgs)
	}
	writeSessionsShowText(stdout, msgs)
	return nil
}

// runSessionsRename sets a saved session's display title. The session's own
// id/name is never changed - only chat.SessionInfo.Title, the human-facing
// label "sessions list" and a sidebar would show in place of the raw id.
func runSessionsRename(args []string, stderr io.Writer) error {
	workspaceRoot, _, positional, err := parseSessionsWorkspaceAndJSON("sessions rename", args, 2)
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
	return nil
}

func runSessionsDelete(args []string, stderr io.Writer) error {
	workspaceRoot, _, positional, err := parseSessionsWorkspaceAndJSON("sessions delete", args, 1)
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
