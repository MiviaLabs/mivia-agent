// Package processservices owns workflow resources shared in one process.
package processservices

import (
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

var (
	panelLimiterOnce sync.Once
	panelLimiter     *controller.PanelActorLimiter
)

// PanelLimiter returns the process-wide limiter for workflow panel actors.
func PanelLimiter() *controller.PanelActorLimiter {
	panelLimiterOnce.Do(func() {
		panelLimiter = controller.NewPanelActorLimiter()
	})
	return panelLimiter
}
