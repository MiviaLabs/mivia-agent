package clichat

import (
	"fmt"
	"strings"
)

// sliceErrors is duplicated from internal/cli/errors.go for the workflow
// contract tests that moved into this package.
func sliceErrors(context string, errs []string) error {
	if len(errs) > 0 {
		return fmt.Errorf("%s: %s", context, strings.Join(errs, "; "))
	}
	return nil
}
