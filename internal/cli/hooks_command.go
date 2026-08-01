package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

// trustScopeNotice states what a confirmation does and does not attest to.
// A reader who assumes the script body is covered has the wrong threat model,
// and this listing is where they will look.
const trustScopeNotice = "trust covers the hook definition shown above - event, matcher, argv, " +
	"timeout, on_timeout. It does NOT cover the contents of the script at argv[0]: " +
	"editing that file does not revoke a confirmation."

// sessionHookState is the running session's lifecycle-hook state, set once at
// startup. /hooks reads it on both surfaces, and the dispatcher wiring reads
// the same resolved decisions, so what the listing shows is what runs.
var sessionHookState *hookSession

// handleSlashHooks serves /hooks and /hooks trust <n>.
func handleSlashHooks(fields []string, term *Terminal) (bool, bool, error) {
	term.WriteString("\n" + hooksSlashOutput(fields))
	return true, false, nil
}

// hooksSlashOutput is the surface-independent body of /hooks.
func hooksSlashOutput(fields []string) string {
	session := sessionHookState
	if len(fields) > 1 && strings.EqualFold(fields[1], "trust") {
		if session == nil {
			return "no lifecycle hooks configured (they load from ~/.mivia/mivia.toml only)"
		}
		return session.trust(strings.Join(fields[2:], " "))
	}
	if len(fields) > 1 {
		return fmt.Sprintf("usage: /hooks | /hooks trust <number> (unknown argument %q)", fields[1])
	}
	return renderHookList(session)
}

// hookSession is the session's resolved lifecycle-hook state: every discovered
// group with its derived tier and trust status, plus the store that decides it.
type hookSession struct {
	decisions []hooks.Decision
	store     *hooks.Store
	warnings  []string
}

// installHookSession resolves this session's lifecycle hooks, reports what was
// ignored, and publishes the result for /hooks and the dispatcher wiring. The
// returned function releases the handle at session end.
func installHookSession(workspaceRoot string) (func(), error) {
	state, err := loadHookSession(workspaceRoot)
	if err != nil {
		return nil, err
	}
	warnHookLoad(state.warnings)
	sessionHookState = state
	return func() { sessionHookState = nil }, nil
}

// loadHookSession discovers lifecycle hooks and resolves their trust.
//
// Hooks come from the user config at its fixed path and from the operator's
// managed file; a workspace mivia.toml supplies none, and says so in a warning
// rather than leaving the author to conclude hooks are broken.
//
// An invalid hook config is an error, not a silent empty load - the same
// treatment skill frontmatter gets, and for the same reason.
func loadHookSession(workspaceRoot string) (*hookSession, error) {
	source, err := config.LoadHooksSource(workspaceRoot)
	if err != nil {
		return nil, err
	}
	session := &hookSession{warnings: append([]string{}, source.Warnings...)}
	managed, managedWarnings := hooks.ManagedGroups()
	session.warnings = append(session.warnings, managedWarnings...)

	groups := append([]hooks.Group{}, managed...)
	if len(source.Data) > 0 {
		user, err := hooks.Parse(source.Data, source.Path)
		if err != nil {
			return nil, err
		}
		groups = append(groups, user...)
	}
	session.store = hooks.OpenStore(hooks.StorePath())
	if err := session.store.Err(); err != nil {
		session.warnings = append(session.warnings, fmt.Sprintf(
			"%v; no lifecycle hooks will run until that trust store is repaired or removed", err))
	}
	session.decisions = hooks.Resolve(groups, session.store)
	return session, nil
}

// runnable returns the groups that may execute in an interactive session.
func (h *hookSession) runnable() []hooks.Group {
	if h == nil {
		return nil
	}
	return hooks.Runnable(h.decisions)
}

// renderHookList is the /hooks listing.
func renderHookList(session *hookSession) string {
	if session == nil || len(session.decisions) == 0 {
		return "no lifecycle hooks configured (they load from ~/.mivia/mivia.toml only)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "lifecycle hooks (%d)\n", len(session.decisions))
	for i, decision := range session.decisions {
		fmt.Fprintf(&b, "  [%d] %-12s %-12s %s\n", i+1, decision.Status, decision.Group.Event,
			hookTierLabel(decision.Tier))
		fmt.Fprintf(&b, "      matcher: %s\n", matcherLabel(decision.Group.Matcher))
		for _, handler := range decision.Group.Handlers {
			fmt.Fprintf(&b, "      run: %s  timeout=%s on_timeout=%s\n",
				strings.Join(handler.Argv, " "), handler.Timeout, handler.OnTimeout)
		}
	}
	b.WriteString("\n" + trustScopeNotice + "\n")
	b.WriteString("promote a pending or hash-changed hook with: /hooks trust <number>\n")
	for _, warning := range session.warnings {
		fmt.Fprintf(&b, "warning: %s\n", warning)
	}
	return b.String()
}

func hookTierLabel(tier hooks.Tier) string {
	if tier == hooks.TierManaged {
		return "(managed, operator-set)"
	}
	return "(user)"
}

// hookArgvLabel is nil-safe: a Decision can be built outside the parser, and a
// message about trust must not be the thing that panics.
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

// trust promotes one pending or changed hook by its listed number.
func (h *hookSession) trust(arg string) string {
	index, err := h.hookIndex(arg)
	if err != nil {
		return err.Error()
	}
	decision := h.decisions[index]
	if decision.Tier == hooks.TierManaged {
		return fmt.Sprintf("hook %d is operator-set in %s; managed hooks are not promoted or disabled here",
			index+1, filepath.Base(decision.Group.Source))
	}
	if decision.Status == hooks.StatusActive {
		return fmt.Sprintf("hook %d is already trusted", index+1)
	}
	if err := h.store.Confirm(decision.Group); err != nil {
		return fmt.Sprintf("could not record the confirmation: %v", err)
	}
	h.decisions[index].Status = h.store.Status(decision.Group)
	return fmt.Sprintf("hook %d trusted: %s on %s. Editing its definition revokes this.",
		index+1, hookArgvLabel(decision.Group), decision.Group.Event)
}

func (h *hookSession) hookIndex(arg string) (int, error) {
	if len(h.decisions) == 0 {
		return 0, fmt.Errorf("no lifecycle hooks configured; usage: /hooks trust <number>")
	}
	number, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || number < 1 || number > len(h.decisions) {
		return 0, fmt.Errorf("usage: /hooks trust <number> in 1-%d", len(h.decisions))
	}
	return number - 1, nil
}
