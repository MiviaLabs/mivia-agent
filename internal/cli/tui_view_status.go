package cli

import (
	"fmt"
)

// AppendCtxSuffix implements append ctx suffix.
func AppendCtxSuffix(detail string, percent int) string {
	suffix := fmt.Sprintf("ctx %d%%", percent)
	if detail == "" {
		return suffix
	}
	return detail + " · " + suffix
}
