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

// outputCeilingFloor is the historical dispatcher output backstop. The
// derived ceiling never goes below it, so tools without a declared result
// budget keep exactly the bound they always had.
const outputCeilingFloor = 256 << 10

// outputCeilingSlack is headroom added on top of the largest tool-declared
// result budget when deriving the dispatcher's output backstop. Budgets bound
// tool CONTENT; the wire output also carries fixed-size tool framing —
// read_file's "… lines X–Y" window header and "... truncated at N bytes"
// notice (~111 bytes worst case), run_command's cwd/exit-status lines. 4 KiB
// covers any such framing by a wide margin while staying negligible next to
// the budgets themselves. Input-derived framing (run_command's argv echo) is
// covered separately by the policy's MaxInputBytes allowance.
const outputCeilingSlack = 4096

// DeriveOutputCeiling returns the runaway-tool output backstop for a
// dispatcher serving the given registry: the largest tool-declared result
// budget (tools.ResultBudgetTool) plus an input allowance plus slack, floored
// at 256KiB. maxInputBytes is the dispatcher's input cap; <= 0 means the
// Policy default (64KiB). It is added because tool results may echo request
// input verbatim (run_command's argv header), so an honest result can exceed
// its content budget by up to the input size.
//
// The dispatcher hard-fails (never truncates) any output above
// Policy.MaxOutputBytes. That is intentional for runaway tools, but the
// audit of commit 0f6e524 showed the fixed 256KiB default colliding with the
// default max_read_bytes (also 262144): a config-compliant read_file window
// — content at budget plus header — was destroyed whole. Deriving the
// ceiling from the budgets the registry actually granted guarantees the
// backstop can never bind below an honest tool result, whatever the config.
func DeriveOutputCeiling(r *tools.Registry, maxInputBytes int) int {
	if maxInputBytes <= 0 {
		maxInputBytes = 64 << 10
	}
	ceiling := outputCeilingFloor
	if r == nil {
		return ceiling
	}
	for _, t := range r.List() {
		budgeted, ok := t.(tools.ResultBudgetTool)
		if !ok {
			continue
		}
		budget := budgeted.ResultBudgetBytes()
		if budget <= 0 {
			continue
		}
		if derived := budget + maxInputBytes + outputCeilingSlack; derived > ceiling {
			ceiling = derived
		}
	}
	return ceiling
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
	return d.Register(Tool, t.Name(), toolHandler{r: r})
}
