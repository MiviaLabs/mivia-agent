package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/blockedpath"
)

// workflowBlockedInputAdmission is the fail-fast admission guard for fresh
// workflow runs. If any string input instructs a write to a host
// write-blocklisted path (one no workflow agent can ever write because the
// write tools refuse), the run is refused before a single agent step starts.
//
// Without this guard the run would start, the implement step would be blocked
// at write time, review would demand the same impossible edit, and the loop
// would burn into a misattributed "review made no progress" failure (or a
// timeout) instead of an attributable blocked cause. The guard is deliberately
// conservative: only a path token plus a demand verb ("edit X", "write Y")
// refuses; a read-only mention ("audit whether X ...") is admitted.
func workflowBlockedInputAdmission(denylist []string, workflowName string, inputs map[string]any) error {
	if len(denylist) == 0 {
		return nil
	}
	for name, value := range inputs {
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		for _, demanded := range blockedpath.PathsDemandedInText(text, denylist) {
			return fmt.Errorf("workflow %q input %q instructs a write to %q, which is write-blocklisted for workflow agents (host policy); route this change through the root session or a host-owned process", workflowName, name, demanded)
		}
	}
	return nil
}
