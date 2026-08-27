package clichat

import (
	"strings"
)

// wantDialRefused accepts the refused-dial wording of every platform: Unix
// reports "connection refused", while Windows Winsock dials report
// "connectex: No connection could be made because the target machine
// actively refused it." - never the Unix phrase. Matching on bare "refused"
// covers both without coupling the assertions to one kernel's text.
func wantDialRefused(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "refused")
}
