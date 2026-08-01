// Package hooks owns mivia's deterministic lifecycle-hook layer: the config
// shape, the definition hash trust is keyed on, and the isolated execution path
// hook commands run through.
//
// The package deliberately imports neither internal/runtime nor internal/tools.
// Hooks are out-of-band process execution: they never construct a
// runtime.Request and never call Dispatcher.Invoke, so a PreToolUse hook that
// matches a tool cannot dispatch that tool and recurse. The boundary is pinned
// by a test rather than stated in a comment, because a comment does not survive
// a refactor.
package hooks

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Event is a lifecycle point a hook may be attached to.
type Event string

const (
	// EventPreToolUse fires after the dispatcher reserved an invocation and
	// before the handler executes. It is the only event that can block.
	EventPreToolUse Event = "PreToolUse"
	// EventPostToolUse fires after the handler returned. Reactive only.
	EventPostToolUse Event = "PostToolUse"
	// EventStop fires when the root loop's turn ends. Observation only.
	EventStop Event = "Stop"
)

// TimeoutVerdict is what a handler's timeout means for the event's decision.
type TimeoutVerdict string

const (
	// OnTimeoutBlock denies the call. It is PreToolUse's default: a hung gate
	// must not be an open gate, or an attacker who can make a hook hang - and
	// so can an ordinary flaky script - has disabled the control.
	OnTimeoutBlock TimeoutVerdict = "block"
	// OnTimeoutAllow warns and continues. Reactive events default to it: a slow
	// formatter must not stop work.
	OnTimeoutAllow TimeoutVerdict = "allow"
)

// HandlerTypeCommand is the only handler type v1 implements.
const HandlerTypeCommand = "command"

// Timeout bounds. A handler may not opt out of having one.
const (
	MinTimeout = time.Second
	MaxTimeout = 600 * time.Second
)

// Handler is one command a matching event runs.
type Handler struct {
	// Type is always HandlerTypeCommand in v1.
	Type string
	// Argv is an explicit argument vector, never a shell string. argv[0] is a
	// filesystem path resolved against the declaring config file's directory.
	Argv []string
	// Timeout is the wall-clock bound for one execution.
	Timeout time.Duration
	// OnTimeout is the verdict when Timeout expires.
	OnTimeout TimeoutVerdict
}

// Group is one [[hooks]] table: an event, a tool-name matcher, and handlers.
type Group struct {
	Event    Event
	Matcher  string
	Handlers []Handler
	// Source is the config file that declared this group.
	Source string
	// Project marks a group declared by the WORKSPACE's config rather than the
	// user's. It changes nothing about execution and everything about display:
	// "this hook came with the repository" is the one fact a reader needs, and
	// deriving it from the Source path at each surface would put the answer in
	// several places at once.
	Project bool
	// Index is the group's position in the [[hooks]] array, for error messages.
	Index int
	// compiled is Matcher compiled once at load. nil means match every tool.
	compiled *regexp.Regexp
}

// Matches reports whether a tool name is covered by this group's matcher.
//
// The pattern is compiled at load, not here: a matcher that failed to compile
// on the hot path of a security gate would have no honest verdict to return -
// allowing would open the gate, denying would break every tool call.
//
// Matching is deliberately unanchored, as it is in every harness whose configs
// users will copy from. `matcher = "run"` therefore covers `run_command`, and
// over-matching a gate errs toward more hooks firing, not fewer.
func (g Group) Matches(toolName string) bool {
	if g.compiled == nil {
		return true
	}
	return g.compiled.MatchString(toolName)
}

// V1Events are the lifecycle events this version implements.
func V1Events() []Event { return []Event{EventPreToolUse, EventPostToolUse, EventStop} }

// deferredEvents are names the field uses that mivia has deliberately not
// shipped. They are rejected as DEFERRED rather than as unknown, so a config
// copied from the Claude Code or Codex docs fails legibly instead of reading
// like a typo.
var deferredEvents = map[Event]string{
	"SessionStart":        "no session-start publish site exists yet",
	"SessionEnd":          "no session-end publish site exists yet",
	"Setup":               "not implemented in v1",
	"UserPromptSubmit":    "not implemented in v1",
	"UserPromptExpansion": "not implemented in v1",
	"StopFailure":         "not implemented in v1",
	"PostToolUseFailure":  "not implemented in v1",
	"PostToolBatch":       "not implemented in v1",
	"PermissionRequest":   "mivia has no dispatcher-layer permission prompt",
	"PermissionDenied":    "mivia has no dispatcher-layer permission prompt",
	"SubagentStart":       "not implemented in v1",
	"SubagentStop":        "not implemented in v1",
	"TaskCreated":         "not implemented in v1",
	"TaskCompleted":       "not implemented in v1",
	"TeammateIdle":        "not implemented in v1",
	"FileChanged":         "not implemented in v1",
	"CwdChanged":          "not implemented in v1",
	"ConfigChange":        "not implemented in v1",
	"InstructionsLoaded":  "not implemented in v1",
	"PreCompact":          "not implemented in v1",
	"PostCompact":         "not implemented in v1",
	"Elicitation":         "not implemented in v1",
	"ElicitationResult":   "not implemented in v1",
	"MessageDisplay":      "not implemented in v1",
	"Notification":        "not implemented in v1",
	"WorktreeCreate":      "not implemented in v1",
	"WorktreeRemove":      "not implemented in v1",
}

// eventDefaults resolve timeout and on_timeout from the EVENT, so an author who
// omits them gets the safe value and /hooks can display what actually applies
// rather than a blank.
var eventDefaults = map[Event]struct {
	Timeout   time.Duration
	OnTimeout TimeoutVerdict
}{
	EventPreToolUse:  {10 * time.Second, OnTimeoutBlock},
	EventPostToolUse: {10 * time.Second, OnTimeoutAllow},
	EventStop:        {5 * time.Second, OnTimeoutAllow},
}

// Parse validates the [[hooks]] tables in a mivia config file.
//
// Rejection is the deliverable: no value is ever coerced onto the permissive
// branch, because coercion toward permissive is precisely how a hook config
// fails open. An unrecognised on_timeout is an error, not "allow"; an
// unrecognised event is an error, not a skip.
func Parse(data []byte, sourcePath string) ([]Group, error) {
	var file struct {
		Hooks []map[string]any `toml:"hooks"`
	}
	if err := toml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: parse hooks: %w", sourcePath, err)
	}
	groups := make([]Group, 0, len(file.Hooks))
	for i, raw := range file.Hooks {
		group, err := parseGroup(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: hooks[%d]: %w", sourcePath, i, err)
		}
		group.Source = sourcePath
		group.Index = i
		groups = append(groups, group)
	}
	return groups, nil
}

// groupKeys is the closed set of keys a [[hooks]] table may carry.
var groupKeys = map[string]bool{"event": true, "matcher": true, "handlers": true}

func parseGroup(raw map[string]any) (Group, error) {
	var group Group
	if err := rejectRetiredKeys(raw); err != nil {
		return Group{}, err
	}
	for key := range raw {
		if !groupKeys[key] {
			return Group{}, fmt.Errorf("unknown key %q; a [[hooks]] table accepts event, matcher and handlers", key)
		}
	}
	event, err := parseEvent(raw["event"])
	if err != nil {
		return Group{}, err
	}
	group.Event = event
	matcher, compiled, err := parseMatcher(raw["matcher"])
	if err != nil {
		return Group{}, err
	}
	group.Matcher, group.compiled = matcher, compiled
	handlers, err := parseHandlers(raw["handlers"], event)
	if err != nil {
		return Group{}, err
	}
	group.Handlers = handlers
	return group, nil
}

func parseEvent(value any) (Event, error) {
	name, ok := value.(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("event is required; one of %s", eventList())
	}
	event := Event(strings.TrimSpace(name))
	if _, ok := eventDefaults[event]; ok {
		return event, nil
	}
	if reason, deferred := deferredEvents[event]; deferred {
		return "", fmt.Errorf("event %q is deferred, not unknown: %s. v1 implements %s", event, reason, eventList())
	}
	return "", fmt.Errorf("unknown event %q; v1 implements %s", event, eventList())
}

func eventList() string {
	names := make([]string, 0, len(V1Events()))
	for _, e := range V1Events() {
		names = append(names, string(e))
	}
	return strings.Join(names, ", ")
}

func parseMatcher(value any) (string, *regexp.Regexp, error) {
	if value == nil {
		return "", nil, nil
	}
	pattern, ok := value.(string)
	if !ok {
		return "", nil, fmt.Errorf("matcher must be a string regular expression on the tool name")
	}
	if pattern == "" {
		return "", nil, nil
	}
	// Compile at load, not at the first tool call: a matcher that does not
	// compile is a hook that never fires, and discovering that mid-turn is
	// indistinguishable from the hook deciding to allow.
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return "", nil, fmt.Errorf("matcher %q is not a valid regular expression: %w", pattern, err)
	}
	return pattern, compiled, nil
}
