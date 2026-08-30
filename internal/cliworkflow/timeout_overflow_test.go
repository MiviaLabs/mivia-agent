package cliworkflow

import (
	"math"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestPanelDeadlineSaturatesInsteadOfOverflow pins saturation on an absurd
// [workflows.panels] member_deadline_default_seconds value. A bare multiply
// by time.Second overflows to a negative Duration, which would arm every
// panel member with an already-expired deadline.
func TestPanelDeadlineSaturatesInsteadOfOverflow(t *testing.T) {
	huge := int(math.MaxInt64)
	res := &config.Resolved{}
	res.Workflows.Panels.MemberDeadlineDefaultSeconds = &huge
	limits := effectiveWorkflowPanelLimits(res)
	if limits.MemberDeadlineDefault <= 0 {
		t.Fatalf("MemberDeadlineDefault overflowed to %v; want a positive saturated deadline", limits.MemberDeadlineDefault)
	}
}
