package cli

import (
	"fmt"
)

func appendCtxSuffix(detail string, percent int) string {
	suffix := fmt.Sprintf("ctx %d%%", percent)
	if detail == "" {
		return suffix
	}
	return detail + " · " + suffix
}
