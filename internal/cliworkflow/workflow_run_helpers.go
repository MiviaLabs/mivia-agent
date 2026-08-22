package cliworkflow

import (
	"fmt"
	"strconv"
	"strings"
)

// parseWorkflowBoolFlag parses a boolean workflow flag in bare (--name) or
// --name=true|false form, removing it from args. It reports an error for a
// malformed value or a duplicate occurrence. It does not reuse FlagValueFunc
// because that helper only handles string-valued flags.
func parseWorkflowBoolFlag(args []string, name string) (bool, []string, error) {
	rest := make([]string, 0, len(args))
	value := false
	found := false
	for _, arg := range args {
		switch {
		case arg == name:
			if found {
				return false, nil, fmt.Errorf("workflow flag %s may only be given once", name)
			}
			value, found = true, true
		case strings.HasPrefix(arg, name+"="):
			if found {
				return false, nil, fmt.Errorf("workflow flag %s may only be given once", name)
			}
			raw := strings.TrimPrefix(arg, name+"=")
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return false, nil, fmt.Errorf("workflow flag %s expects true or false, got %q", name, raw)
			}
			value, found = parsed, true
		default:
			rest = append(rest, arg)
		}
	}
	return value, rest, nil
}
