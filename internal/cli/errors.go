package cli

import (
	"fmt"
	"strings"
)

// sliceErrors converts a []string of error messages to a single error
// if non-empty, or nil.
func sliceErrors(context string, errs []string) error {
	if len(errs) > 0 {
		return fmt.Errorf("%s: %s", context, strings.Join(errs, "; "))
	}
	return nil
}
