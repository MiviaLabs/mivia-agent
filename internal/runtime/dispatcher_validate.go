package runtime

import "fmt"

// RequestValidator permits a handler to reject stale or unauthorized work
// before a coordinator mutates durable state for a retry or resume.
type RequestValidator interface {
	ValidateRequest(Request) error
}

// Validate checks a request without reserving an invocation or calling a
// handler. It is used by resume preflight, where a routing failure must leave
// the durable run untouched.
func (d *Dispatcher) Validate(req Request) error {
	if err := d.validateRequest(req); err != nil {
		return err
	}
	d.mu.Lock()
	handler := d.handlers[req.Kind][req.Name]
	allowed := d.policy.Allow[req.Kind][req.Name]
	d.mu.Unlock()
	if handler == nil {
		return fmt.Errorf("unknown %s %q", req.Kind, req.Name)
	}
	if !allowed {
		return fmt.Errorf("permission denied for %q", req.Name)
	}
	if validator, ok := handler.(RequestValidator); ok {
		return validator.ValidateRequest(req)
	}
	return nil
}
