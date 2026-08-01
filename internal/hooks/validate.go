package hooks

import (
	"fmt"
	"strings"
	"time"
)

// retiredKeys are keys that carry a specific, correctable meaning. They are
// rejected with their own message rather than as "unknown key", because each is
// something a user will plausibly write by copying a working config from
// another harness, and "unknown key" would not tell them what to write instead.
var retiredKeys = map[string]string{
	"trust": "trust is derived, never declared: a file cannot name its own tier. " +
		"Tier comes from which fixed path the hook loaded from plus the definition " +
		"hash recorded in the trust store",
	"run": "run was removed; use argv = [\"./hook.sh\", \"--flag\"]. A single command " +
		"string implies a shell and a quoting pass, and hooks execute an explicit " +
		"argv with no shell and no interpolation",
	"updatedInput": "updatedInput is not supported: the dispatcher computes the " +
		"invocation's input hash and dedup fingerprint before the hook point, so " +
		"rewriting tool arguments afterwards would produce an audit record " +
		"describing input that was never executed",
}

func rejectRetiredKeys(raw map[string]any) error {
	for key := range raw {
		if reason, retired := retiredKeys[key]; retired {
			return fmt.Errorf("key %q is rejected: %s", key, reason)
		}
	}
	return nil
}

// handlerKeys is the closed set of keys a [[hooks.handlers]] table may carry.
var handlerKeys = map[string]bool{"type": true, "argv": true, "timeout": true, "on_timeout": true}

func parseHandlers(value any, event Event) ([]Handler, error) {
	rows, err := handlerRows(value)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("at least one [[hooks.handlers]] table is required; a matcher group with nothing to run is not a hook")
	}
	handlers := make([]Handler, 0, len(rows))
	for i, row := range rows {
		handler, err := parseHandler(row, event)
		if err != nil {
			return nil, fmt.Errorf("handlers[%d]: %w", i, err)
		}
		handlers = append(handlers, handler)
	}
	return handlers, nil
}

// handlerRows normalises the two shapes go-toml produces for an array of
// tables reached through an `any`-typed parent.
func handlerRows(value any) ([]map[string]any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []map[string]any:
		return typed, nil
	case []any:
		rows := make([]map[string]any, 0, len(typed))
		for _, element := range typed {
			row, ok := element.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("handlers must be [[hooks.handlers]] tables")
			}
			rows = append(rows, row)
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("handlers must be [[hooks.handlers]] tables")
	}
}

func parseHandler(raw map[string]any, event Event) (Handler, error) {
	if err := rejectRetiredKeys(raw); err != nil {
		return Handler{}, err
	}
	for key := range raw {
		if !handlerKeys[key] {
			return Handler{}, fmt.Errorf("unknown key %q; a handler accepts type, argv, timeout and on_timeout", key)
		}
	}
	handler := Handler{Type: HandlerTypeCommand}
	if raw["type"] != nil {
		name, ok := raw["type"].(string)
		if !ok {
			return Handler{}, fmt.Errorf("type must be a string")
		}
		if name != HandlerTypeCommand {
			return Handler{}, fmt.Errorf(
				"handler type %q is not supported: v1 implements type = %q only. "+
					"prompt, agent, http and mcp_tool each add a nested call with its own "+
					"cost, timeout and injection surface", name, HandlerTypeCommand)
		}
		handler.Type = name
	}
	argv, err := parseArgv(raw["argv"])
	if err != nil {
		return Handler{}, err
	}
	handler.Argv = argv
	defaults := eventDefaults[event]
	handler.Timeout, err = parseTimeout(raw["timeout"], defaults.Timeout)
	if err != nil {
		return Handler{}, err
	}
	handler.OnTimeout, err = parseOnTimeout(raw["on_timeout"], defaults.OnTimeout)
	if err != nil {
		return Handler{}, err
	}
	return handler, nil
}

func parseArgv(value any) ([]string, error) {
	const wanted = "argv is required and must be a non-empty array of strings whose first element is a path to the hook program"
	elements, ok := value.([]any)
	if !ok {
		if typed, isStrings := value.([]string); isStrings {
			elements = make([]any, len(typed))
			for i, s := range typed {
				elements[i] = s
			}
		} else {
			return nil, fmt.Errorf("%s", wanted)
		}
	}
	if len(elements) == 0 {
		return nil, fmt.Errorf("%s", wanted)
	}
	argv := make([]string, 0, len(elements))
	for _, element := range elements {
		text, isString := element.(string)
		if !isString {
			return nil, fmt.Errorf("%s", wanted)
		}
		argv = append(argv, text)
	}
	if strings.TrimSpace(argv[0]) == "" {
		return nil, fmt.Errorf("%s", wanted)
	}
	return argv, nil
}

func parseTimeout(value any, fallback time.Duration) (time.Duration, error) {
	if value == nil {
		return fallback, nil
	}
	seconds, ok := value.(int64)
	if !ok {
		return 0, timeoutRangeError(value)
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout < MinTimeout || timeout > MaxTimeout {
		return 0, timeoutRangeError(value)
	}
	return timeout, nil
}

func timeoutRangeError(value any) error {
	return fmt.Errorf("timeout %v is out of range: it is a whole number of seconds between %d and %d",
		value, int(MinTimeout/time.Second), int(MaxTimeout/time.Second))
}

func parseOnTimeout(value any, fallback TimeoutVerdict) (TimeoutVerdict, error) {
	if value == nil {
		return fallback, nil
	}
	name, ok := value.(string)
	if !ok {
		return "", onTimeoutError(value)
	}
	switch TimeoutVerdict(name) {
	case OnTimeoutBlock:
		return OnTimeoutBlock, nil
	case OnTimeoutAllow:
		return OnTimeoutAllow, nil
	default:
		return "", onTimeoutError(name)
	}
}

// onTimeoutError never falls back to a default. An unrecognised verdict that
// resolved to "allow" would turn a typo into a disabled gate.
func onTimeoutError(value any) error {
	return fmt.Errorf("on_timeout %v is not recognised; it is %q or %q, and is never defaulted from an unrecognised value",
		value, OnTimeoutBlock, OnTimeoutAllow)
}
