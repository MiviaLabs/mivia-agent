package cli

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

// hookScopeNotice states what configuring a hook commits the operator to.
//
// There is no confirmation step, so this listing is the only place the scope is
// ever stated - and the thing most likely to be assumed wrongly is that mivia
// watches the script. It does not: the config names a program, and whatever is
// in that file at call time is what runs.
const hookScopeNotice = "these run because a config declares them - there is no separate confirmation " +
	"step. mivia executes the program at argv[0] as it is on disk at call time; it does not track that " +
	"file's contents."

// hookProjectNotice is printed only when the workspace itself declared a hook.
//
// A project hook arrives with the repository, so the person running it may
// never have written it. Saying so once, next to the list of exactly which ones
// they are, is the whole of the disclosure - and it is why the listing marks
// provenance per hook rather than just counting them.
const hookProjectNotice = "hooks marked [project] came from this workspace's .mivia/mivia.toml, not from your " +
	"user config - if you cloned this repository, someone else wrote them."

// sessionHookState is the running session's lifecycle-hook state. /hooks reads
// it on both surfaces and the dispatcher's hook funcs read the same groups, so
// what the listing shows is what runs.
//
// It stays an atomic pointer with a mutex-guarded body because the readers sit
// on different goroutines: tool calls run in parallel while the UI goroutine
// may be rendering /hooks.
var sessionHookState atomic.Pointer[hookSession]

func currentHookSession() *hookSession { return sessionHookState.Load() }

// hookSessionConfigured reports whether any hook was discovered at all.
func hookSessionConfigured() bool {
	session := currentHookSession()
	if session == nil {
		return false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return len(session.groups) > 0
}

// handleSlashHooks serves /hooks.
func handleSlashHooks(fields []string, term *Terminal) (bool, bool, error) {
	term.WriteString("\n" + HooksSlashOutput(fields))
	return true, false, nil
}

// HooksSlashOutput is the surface-independent body of /hooks.
//
// `/hooks trust <n>` is answered rather than rejected as an unknown argument.
// It was a real subcommand, it will be in muscle memory and in notes, and
// "unknown argument" would read as a bug in the listing rather than as a
// removed concept.
func HooksSlashOutput(fields []string) string {
	if len(fields) > 1 && strings.EqualFold(fields[1], "trust") {
		return "hook trust confirmation was removed: a hook declared in your user config or in this " +
			"workspace's .mivia/mivia.toml runs. Delete or comment out the [[hooks]] entry to stop one."
	}
	if len(fields) > 1 {
		return fmt.Sprintf("usage: /hooks (unknown argument %q)", fields[1])
	}
	return renderHookList(currentHookSession())
}

// hookSession is the session's resolved lifecycle-hook state.
type hookSession struct {
	// mu guards every field below. Tool calls read the groups from parallel
	// goroutines while the UI goroutine renders /hooks.
	mu       sync.Mutex
	groups   []hooks.Group
	warnings []string
	// runWarnings are diagnostics from hooks that actually executed, kept
	// bounded and surfaced by /hooks rather than printed: a tool call runs
	// while the TUI owns the terminal.
	runWarnings []string
}

// maxRunWarnings bounds retained run-time diagnostics. A hook that warns on
// every tool call must not grow the session without limit.
const maxRunWarnings = 20

func (h *hookSession) noteRunWarnings(warnings []string) {
	if h == nil || len(warnings) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runWarnings = append(h.runWarnings, warnings...)
	if extra := len(h.runWarnings) - maxRunWarnings; extra > 0 {
		h.runWarnings = append([]string{}, h.runWarnings[extra:]...)
	}
}

// installHookSession resolves this session's lifecycle hooks, reports what was
// ignored and what is armed, and publishes the result for /hooks and the
// dispatcher wiring. The returned function releases the handle at session end.
// quiet (--quiet) suppresses the armed notice and the project-hook disclosure:
// the operator explicitly asked for quieter startup, and /hooks still lists
// every armed hook on demand. Genuine load warnings always print.
func installHookSession(workspaceRoot string, staleBypass, quiet bool) (func(), error) {
	state, err := loadHookSession(workspaceRoot)
	if err != nil {
		return nil, err
	}
	notices := append([]string{}, state.warnings...)
	if !quiet {
		notices = append(notices, state.armedNotice()...)
	}
	if staleBypass && !quiet {
		notices = append(notices, "--bypass-hook-trust no longer does anything and can be removed: "+
			"a hook declared in ~/.mivia/mivia.toml runs without confirmation.")
	}
	warnHookLoad(notices)
	sessionHookState.Store(state)
	return func() { sessionHookState.Store(nil) }, nil
}

// armedNotice names every hook that will run this session.
//
// It replaces the confirmation prompt, and it is not a lesser thing standing in
// for one: a prompt asks a question whose answer was already given by editing
// the config, while this states a fact the operator can act on. A session that
// executes programs on every tool call and says nothing about it is the actual
// hazard.
func (h *hookSession) armedNotice() []string {
	if h == nil || len(h.groups) == 0 {
		return nil
	}
	labels := make([]string, 0, len(h.groups))
	for _, group := range h.groups {
		labels = append(labels, hookGroupLabel(group))
	}
	notice := fmt.Sprintf("lifecycle hooks armed (%d): %s. Run /hooks for detail.",
		len(labels), strings.Join(labels, "; "))
	if !anyProjectHook(h.groups) {
		return []string{notice}
	}
	// A hook this workspace supplied is the one an operator did not choose, so
	// it is called out separately rather than left to be spotted in the list.
	return []string{notice, hookProjectNotice}
}

// loadHookSession discovers lifecycle hooks from both surfaces.
//
// The user config at its fixed path comes first, then this workspace's own
// .mivia/mivia.toml. They ADD: a project's formatter and a user's global gate
// are two hooks, not competing answers, and ordering the user's first means a
// PreToolUse gate they wrote answers before a repository's does.
//
// A project hook can therefore run code the operator did not write. What stands
// in for a confirmation is disclosure that cannot be missed - the startup
// notice, the [project] marker on every listed hook, and a transcript row per
// execution.
func loadHookSession(workspaceRoot string) (*hookSession, error) {
	source, err := config.LoadHooksSource(workspaceRoot)
	if err != nil {
		return nil, err
	}
	session := &hookSession{warnings: append([]string{}, source.Warnings...)}
	for _, file := range source.Files {
		groups, err := hooks.Parse(file.Data, file.Path)
		if err != nil {
			// The user's own config is theirs to fix, so a fault in it stops
			// startup. A workspace config is shipped by whoever wrote the repo,
			// and failing every session in a directory over it would hand any
			// clone a denial of service - so it is reported and contributes
			// nothing.
			if !file.Project {
				return nil, err
			}
			session.warnings = append(session.warnings, fmt.Sprintf(
				"ignoring lifecycle hooks in %s: %v", file.Path, err))
			continue
		}
		for i := range groups {
			groups[i].Project = file.Project
		}
		session.groups = append(session.groups, groups...)
	}
	return session, nil
}

// runnable returns the groups that may execute in this session.
func (h *hookSession) runnable() []hooks.Group {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hooks.Group(nil), h.groups...)
}

// renderHookList is the /hooks listing.
func renderHookList(session *hookSession) string {
	if session != nil {
		session.mu.Lock()
		defer session.mu.Unlock()
	}
	if session == nil || len(session.groups) == 0 {
		return "no lifecycle hooks configured (they load from ~/.mivia/mivia.toml and <workspace>/.mivia/mivia.toml)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "lifecycle hooks (%d)\n", len(session.groups))
	for i, group := range session.groups {
		fmt.Fprintf(&b, "  [%d] %-9s %-12s %s\n", i+1, hookOriginLabel(group), "active", group.Event)
		fmt.Fprintf(&b, "      matcher: %s\n", matcherLabel(group.Matcher))
		for _, handler := range group.Handlers {
			fmt.Fprintf(&b, "      run: %s  timeout=%s on_timeout=%s\n",
				strings.Join(handler.Argv, " "), handler.Timeout, handler.OnTimeout)
		}
	}
	b.WriteString("\n" + hookScopeNotice + "\n")
	if anyProjectHook(session.groups) {
		b.WriteString(hookProjectNotice + "\n")
	}
	for _, warning := range append(append([]string{}, session.warnings...), session.runWarnings...) {
		b.WriteString(formatHookWarning(warning) + "\n")
	}
	return b.String()
}

func hookGroupLabel(group hooks.Group) string {
	return fmt.Sprintf("%s %s %s", hookOriginLabel(group), group.Event, hookArgvLabel(group))
}

// hookOriginLabel is where a hook came from, in the two words that matter.
func hookOriginLabel(group hooks.Group) string {
	if group.Project {
		return "[project]"
	}
	return "[user]"
}

func anyProjectHook(groups []hooks.Group) bool {
	for _, group := range groups {
		if group.Project {
			return true
		}
	}
	return false
}

// hookArgvLabel is nil-safe: a Group can be built outside the parser, and a
// message about what is armed must not be the thing that panics.
func hookArgvLabel(group hooks.Group) string {
	if len(group.Handlers) == 0 {
		return "(no handlers)"
	}
	return strings.Join(group.Handlers[0].Argv, " ")
}

func matcherLabel(matcher string) string {
	if matcher == "" {
		return "* (every tool)"
	}
	return matcher
}
