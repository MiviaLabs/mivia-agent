package controller

import (
	"fmt"
	"time"
)

// SetTimeSource sets the immutable controller clock before Start.
func (c *LinearController) SetTimeSource(now func() time.Time) error {
	if now == nil {
		return fmt.Errorf("workflow controller clock is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.now = now
	return nil
}
