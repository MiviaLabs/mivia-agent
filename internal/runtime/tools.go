package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type toolHandler struct{ r *tools.Registry }

func (h toolHandler) Invoke(ctx context.Context, req Request) (json.RawMessage, error) {
	out, err := h.r.Execute(ctx, req.Name, req.Input)
	return json.RawMessage(out), err
}

func NewToolDispatcher(r *tools.Registry, p Policy) (*Dispatcher, error) {
	if r == nil {
		return nil, fmt.Errorf("nil tool registry")
	}
	// A zero MaxOutputBytes asks for the default ceiling; derive it from the
	// registry's declared budgets so the backstop always clears them. An
	// explicit value is a deliberate bound and is honored as-is.
	if p.MaxOutputBytes <= 0 {
		p.MaxOutputBytes = DeriveOutputCeiling(r, p.MaxInputBytes)
	}
	d := New(p)
	for _, t := range r.List() {
		if err := d.RegisterTool(r, t); err != nil {
			return nil, fmt.Errorf("register tool %q: %w", t.Name(), err)
		}
	}
	return d, nil
}

// RegisterTool adds a registry-backed tool to an existing dispatcher. This is
// the supported path for tools that need the dispatcher during construction
// (for example delegation tools); it keeps the model-visible registry and the
// executable dispatcher in sync.
func (d *Dispatcher) RegisterTool(r *tools.Registry, t tools.Tool) error {
	if r == nil || t == nil {
		return fmt.Errorf("invalid tool registration")
	}
	// The installed handler executes r.Execute(name, ...) — that is, whatever
	// tool the REGISTRY resolves for this name. Derive the ceiling from that
	// same object so a tool can never be bound by a budget it did not declare.
	// registerSessionTool and registerLedgerTools deliberately call RegisterTool
	// BEFORE reg.Register, so a miss here is normal and t is the tool that will
	// be resolved a moment later.
	source := t
	if resolved, ok := r.Get(t.Name()); ok {
		source = resolved
	}
	ceiling := toolOutputCeiling(source, d.Policy().MaxInputBytes)
	return d.register(Tool, t.Name(), toolHandler{r: r}, ceiling)
}
