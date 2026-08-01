package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// The tools below are SYNTHETIC and carry FABRICATED budgets on purpose. The
// defect is about how one tool's budget interacts with another's ceiling, and
// pinning it against whatever max_read_bytes happens to be today would make
// these tests re-fail every time a real tool's budget moves.

// budgetedSynthTool declares a result budget and returns a run of bytes whose
// length is either its configured size or the {"size":N} the call asks for.
type budgetedSynthTool struct {
	name   string
	budget int
	size   int
}

func (t *budgetedSynthTool) Name() string               { return t.name }
func (t *budgetedSynthTool) Description() string        { return "synthetic ceiling probe" }
func (t *budgetedSynthTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *budgetedSynthTool) ResultBudgetBytes() int     { return t.budget }
func (t *budgetedSynthTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	return strings.Repeat("x", synthSize(args, t.size)), nil
}

// unbudgetedSynthTool deliberately does NOT implement tools.ResultBudgetTool.
type unbudgetedSynthTool struct {
	name string
	size int
}

func (t *unbudgetedSynthTool) Name() string               { return t.name }
func (t *unbudgetedSynthTool) Description() string        { return "synthetic ceiling probe" }
func (t *unbudgetedSynthTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *unbudgetedSynthTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	return strings.Repeat("x", synthSize(args, t.size)), nil
}

func synthSize(args json.RawMessage, fallback int) int {
	var req struct {
		Size *int `json:"size"`
	}
	if json.Unmarshal(args, &req) == nil && req.Size != nil {
		return *req.Size
	}
	return fallback
}

func synthDispatch(t *testing.T, d *Dispatcher, id, name string, size int) Result {
	t.Helper()
	return d.Invoke(context.Background(), Request{
		ID: id, Kind: Tool, Name: name,
		Input: json.RawMessage(fmt.Sprintf(`{"size":%d}`, size)),
	})
}

func destroyed(res Result) bool {
	return strings.Contains(string(res.Output), "output budget exceeded")
}

// floorDerivedCeiling is what a tool with no declared budget - and a tool
// whose budget sits at or below the historical floor - is bound by:
// 262144 + 65536 + 4096.
const floorDerivedCeiling = outputCeilingFloor + defaultInputAllowance + outputCeilingSlack

// TestPerToolCeilingIsNotRaisedByAnotherToolsBudget is the defect test. The
// dispatcher used to compute ONE backstop - the max over every registered
// budget - and apply it to every tool. A single generous budget therefore
// bought every other tool the same slack, so a runaway tool could emit
// megabytes and stay under the backstop that its OWN declaration said should
// have stopped it. Each tool must be bound by the budget IT declared.
func TestPerToolCeilingIsNotRaisedByAnotherToolsBudget(t *testing.T) {
	const bigBudget = 8 << 20
	const smallBudget = 262144
	reg := tools.NewRegistry()
	reg.Register(&budgetedSynthTool{name: "big", budget: bigBudget})
	reg.Register(&budgetedSynthTool{name: "small", budget: smallBudget})

	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	wantBig := bigBudget + defaultInputAllowance + outputCeilingSlack
	if got := d.Policy().MaxOutputBytes; got != wantBig {
		t.Fatalf("global cap = %d, want the largest declared budget's derivation %d", got, wantBig)
	}
	if got := d.OutputCeiling(Tool, "big"); got != wantBig {
		t.Errorf("big tool ceiling = %d, want %d", got, wantBig)
	}
	if got := d.OutputCeiling(Tool, "small"); got != floorDerivedCeiling {
		t.Errorf("small tool ceiling = %d, want %d - the big tool's budget must not raise it",
			got, floorDerivedCeiling)
	}

	// A 1MiB result from a tool that declared 256KiB is a runaway and must be
	// stopped, even though a sibling tool is allowed 8MiB.
	if res := synthDispatch(t, d, "runaway", "small", 1<<20); !destroyed(res) {
		t.Errorf("small tool's 1MiB runaway survived its own %d-byte budget: %s",
			smallBudget, string(res.Output)[:min(len(res.Output), 160)])
	}
	// The big tool's honest 5MiB result still passes.
	res := synthDispatch(t, d, "honest", "big", 5<<20)
	if destroyed(res) || res.Err != nil {
		t.Errorf("big tool's honest 5MiB result was destroyed: err=%v body=%q",
			res.Err, string(res.Output)[:min(len(res.Output), 160)])
	} else if len(res.Output) != 5<<20 {
		t.Errorf("big tool result = %d bytes, want %d", len(res.Output), 5<<20)
	}
}

// ceilingConfigMatrix is the config surface the per-tool derivation must hold
// across: the shipped defaults and each knob that moves a declared budget.
var ceilingConfigMatrix = map[string]tools.DefaultOptions{
	"defaults":                    {},
	"raised max_read_bytes":       {MaxReadBytes: 2 << 20},
	"raised max_output_bytes":     {MaxOutputBytes: 1 << 20},
	"large max_tool_result_bytes": {MaxToolResultBytes: 8 << 20},
	"small max_tool_result_bytes": {MaxToolResultBytes: 4096},
}

// TestPerToolCeilingClearsEveryDeclaredBudget: tightening per tool is only
// safe if no tool's ceiling can bind below the budget that tool declared -
// otherwise the dispatcher destroys a config-compliant result, which is the
// exact defect class INV-AG-25 exists to prevent.
func TestPerToolCeilingClearsEveryDeclaredBudget(t *testing.T) {
	for name, opts := range ceilingConfigMatrix {
		t.Run(name, func(t *testing.T) {
			reg := newCeilingRegistry(t, opts)
			d, err := NewToolDispatcher(reg, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			for _, tool := range reg.List() {
				budgeted, ok := tool.(tools.ResultBudgetTool)
				if !ok {
					continue
				}
				budget := budgeted.ResultBudgetBytes()
				if budget <= 0 {
					continue
				}
				if got := d.OutputCeiling(Tool, tool.Name()); got < budget+outputCeilingSlack {
					t.Errorf("%s: ceiling %d binds below its declared budget %d plus framing slack %d",
						tool.Name(), got, budget, outputCeilingSlack)
				}
			}
		})
	}
}

// TestPerToolCeilingNeverExceedsTheGlobalCeiling is the monotone-tightening
// half of the safety argument: a per-tool ceiling may only ever LOWER the
// bound the dispatcher would otherwise enforce, so this change cannot widen
// any existing backstop.
func TestPerToolCeilingNeverExceedsTheGlobalCeiling(t *testing.T) {
	for name, opts := range ceilingConfigMatrix {
		t.Run(name, func(t *testing.T) {
			reg := newCeilingRegistry(t, opts)
			d, err := NewToolDispatcher(reg, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			global := d.Policy().MaxOutputBytes
			for _, tool := range reg.List() {
				if got := d.OutputCeiling(Tool, tool.Name()); got > global {
					t.Errorf("%s: ceiling %d exceeds the policy cap %d", tool.Name(), got, global)
				}
			}
			for _, k := range []Kind{Skill, Subagent} {
				if got := d.OutputCeiling(k, "anything"); got > global {
					t.Errorf("%s ceiling %d exceeds the policy cap %d", k, got, global)
				}
			}
		})
	}
}

// TestUndeclaredToolGetsFloorDerivedCeiling: a tool that declares nothing is
// bound at max(nothing, floor) + input allowance + slack - the value the old
// global backstop gave it on the default config. Using the bare 256KiB floor
// instead would be a silent 69632-byte tightening for every undeclared tool
// (dispatch_tasks, delegate, search_replace, the ledger tools).
func TestUndeclaredToolGetsFloorDerivedCeiling(t *testing.T) {
	// A budgeted neighbour lifts the GLOBAL cap far above the floor, so the
	// value observed below is the undeclared tool's own, not the global one.
	reg := tools.NewRegistry()
	reg.Register(&budgetedSynthTool{name: "neighbour", budget: 4 << 20})
	reg.Register(&unbudgetedSynthTool{name: "plain"})

	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if got := d.OutputCeiling(Tool, "plain"); got != floorDerivedCeiling {
		t.Fatalf("undeclared tool ceiling = %d, want floor-derived %d", got, floorDerivedCeiling)
	}
	if res := synthDispatch(t, d, "over", "plain", floorDerivedCeiling+1); !destroyed(res) {
		t.Errorf("one byte over the floor-derived ceiling survived")
	}
	res := synthDispatch(t, d, "under", "plain", floorDerivedCeiling)
	if destroyed(res) || res.Err != nil {
		t.Errorf("a result exactly at the floor-derived ceiling was destroyed: err=%v", res.Err)
	}
}

// TestExplicitPolicyCapsEveryPerToolCeiling: an explicit Policy.MaxOutputBytes
// is a deliberate operator bound and stays a HARD global cap. A declared tool
// budget can only tighten below it, never raise past it.
func TestExplicitPolicyCapsEveryPerToolCeiling(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{})
	d, err := NewToolDispatcher(reg, Policy{MaxOutputBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := d.Policy().MaxOutputBytes; got != 512 {
		t.Fatalf("policy MaxOutputBytes = %d, want the explicit 512", got)
	}
	if got := d.OutputCeiling(Tool, "read_file"); got != 512 {
		t.Fatalf("read_file ceiling = %d, want the explicit cap 512", got)
	}
	for _, tool := range reg.List() {
		if got := d.OutputCeiling(Tool, tool.Name()); got != 512 {
			t.Errorf("%s ceiling = %d, want the explicit cap 512", tool.Name(), got)
		}
	}
}

// TestNonToolKindsUseThePolicyCeiling: skills and subagents declare no result
// budget, so there is nothing to derive from and the policy value stands.
func TestNonToolKindsUseThePolicyCeiling(t *testing.T) {
	d := New(Policy{MaxOutputBytes: 4096})
	defer d.Close()
	body := handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	})
	if err := d.Register(Skill, "s", body); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(Subagent, "a", body); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind Kind
		name string
	}{{Skill, "s"}, {Subagent, "a"}, {Skill, "unregistered"}, {Tool, "unregistered"}} {
		if got := d.OutputCeiling(c.kind, c.name); got != 4096 {
			t.Errorf("%s %q ceiling = %d, want the policy value 4096", c.kind, c.name, got)
		}
	}
}

// TestBareRegisterKeepsThePolicyCeiling pins the deliberate fallback: a Tool
// handler installed through the plain Register path carries no tools.Tool the
// derivation could read, so it keeps Policy.MaxOutputBytes exactly. Tests that
// register raw handlers (fail_output_test.go, dispatcher_test.go) depend on
// this staying untouched.
func TestBareRegisterKeepsThePolicyCeiling(t *testing.T) {
	d := New(Policy{})
	defer d.Close()
	if err := d.Register(Tool, "raw", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(strings.Repeat("x", outputCeilingFloor)), nil
	})); err != nil {
		t.Fatal(err)
	}
	if got := d.OutputCeiling(Tool, "raw"); got != outputCeilingFloor {
		t.Fatalf("bare-registered tool ceiling = %d, want the policy default %d", got, outputCeilingFloor)
	}
	res := d.Invoke(context.Background(), Request{ID: "raw", Kind: Tool, Name: "raw", Input: json.RawMessage(`{}`)})
	if destroyed(res) || res.Err != nil {
		t.Fatalf("a result exactly at the policy ceiling was destroyed: err=%v", res.Err)
	}
}

// TestConcurrentRegisterToolAndInvoke: ceilings are written during
// registration and read on the invocation hot path. Run under -race.
func TestConcurrentRegisterToolAndInvoke(t *testing.T) {
	const late = 16
	reg := tools.NewRegistry()
	reg.Register(&budgetedSynthTool{name: "base", budget: 1 << 20, size: 16})
	names := make([]string, 0, late)
	for i := 0; i < late; i++ {
		name := fmt.Sprintf("late%02d", i)
		names = append(names, name)
		reg.Register(&budgetedSynthTool{name: name, budget: 262144, size: 16})
	}
	// The registry is fully built, and every tool resolved, before any
	// goroutine starts: the goroutines then only READ the registry, and the
	// contended state is the dispatcher's alone.
	d := New(Policy{MaxOutputBytes: DeriveOutputCeiling(reg, 0)})
	defer d.Close()
	resolved := make([]tools.Tool, 0, late)
	for _, name := range names {
		resolved = append(resolved, mustGet(t, reg, name))
	}
	if err := d.RegisterTool(reg, mustGet(t, reg, "base")); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for _, tool := range resolved {
		wg.Add(1)
		go func(tool tools.Tool) {
			defer wg.Done()
			if err := d.RegisterTool(reg, tool); err != nil {
				t.Errorf("RegisterTool(%s): %v", tool.Name(), err)
			}
		}(tool)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j, name := range append([]string{"base"}, names...) {
				res := synthDispatch(t, d, fmt.Sprintf("w%d-%d", worker, j), name, 16)
				// A name whose registration has not landed yet fails as
				// unknown; that is expected. Nothing may be destroyed.
				if destroyed(res) {
					t.Errorf("%s destroyed a 16-byte result", name)
				}
			}
		}(i)
	}
	wg.Wait()

	for _, name := range names {
		if got := d.OutputCeiling(Tool, name); got != floorDerivedCeiling {
			t.Errorf("%s ceiling = %d after concurrent registration, want %d", name, got, floorDerivedCeiling)
		}
	}
}

func mustGet(t *testing.T, reg *tools.Registry, name string) tools.Tool {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	return tool
}

// TestOutputCeilingFailureNamesTheToolAndTheCeiling: a destroyed result is the
// most confusing failure the dispatcher produces, because the tool did nothing
// wrong from its own point of view. The message must say which tool, how big
// its result was, and what bound it broke. It must NOT contain "canceled" or
// "deadline exceeded": internal/cli/dispatch.go's statusFromErr substring-
// matches those and would misreport an over-budget result as a timeout.
func TestOutputCeilingFailureNamesTheToolAndTheCeiling(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&budgetedSynthTool{name: "big", budget: 4 << 20})
	reg.Register(&budgetedSynthTool{name: "grep", budget: 262144})
	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	res := synthDispatch(t, d, "msg", "grep", 1<<20)
	if res.Err == nil {
		t.Fatal("expected an over-ceiling failure")
	}
	reason := res.Err.Error()
	for _, want := range []string{"output budget exceeded", "grep", "1048576", fmt.Sprint(floorDerivedCeiling)} {
		if !strings.Contains(reason, want) {
			t.Errorf("error %q does not mention %q", reason, want)
		}
	}
	for _, forbidden := range []string{"canceled", "deadline exceeded"} {
		if strings.Contains(reason, forbidden) {
			t.Errorf("error %q contains %q, which statusFromErr would misread", reason, forbidden)
		}
	}
	body := string(res.Output)
	if !strings.Contains(body, "output budget exceeded") || !strings.Contains(body, "grep") {
		t.Errorf("model-facing payload lost the reason: %q", body)
	}
}

// TestRegisteredToolCeilingMatchesTheRegistryResolvedTool guards a sharp edge:
// RegisterTool takes a tools.Tool, but the handler it installs executes
// r.Execute(name, ...) - that is, whatever tool the REGISTRY resolves for that
// name. Deriving the ceiling from the passed object while executing the
// registry's would bound a tool by a budget it never declared.
func TestRegisteredToolCeilingMatchesTheRegistryResolvedTool(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{MaxReadBytes: 1 << 20})
	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	global := d.Policy().MaxOutputBytes
	for _, tool := range reg.List() {
		want := min(toolOutputCeiling(mustGet(t, reg, tool.Name()), d.Policy().MaxInputBytes), global)
		if got := d.OutputCeiling(Tool, tool.Name()); got != want {
			t.Errorf("%s ceiling = %d, want %d", tool.Name(), got, want)
		}
	}

	// A decoy object whose Name() collides with a registry entry must not be
	// able to install a budget the executed tool never declared.
	decoy := New(Policy{MaxOutputBytes: global})
	defer decoy.Close()
	if err := decoy.RegisterTool(reg, &budgetedSynthTool{name: "read_file", budget: 8 << 20}); err != nil {
		t.Fatal(err)
	}
	want := min(toolOutputCeiling(mustGet(t, reg, "read_file"), decoy.Policy().MaxInputBytes), global)
	if got := decoy.OutputCeiling(Tool, "read_file"); got != want {
		t.Fatalf("ceiling came from the passed object (%d), not the registry-resolved tool (%d)", got, want)
	}
}

// TestFindReferencesCeilingClearsItsDeclaredBudget covers the one budgeted
// default tool the worst-case harness records as out of scope (it needs a
// type-checkable module), so its per-tool ceiling is pinned here instead.
func TestFindReferencesCeilingClearsItsDeclaredBudget(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{})
	budgeted, ok := mustGet(t, reg, "find_references").(tools.ResultBudgetTool)
	if !ok {
		t.Fatal("find_references does not implement tools.ResultBudgetTool")
	}
	budget := budgeted.ResultBudgetBytes()
	if budget <= 0 {
		t.Fatalf("find_references declares budget %d", budget)
	}
	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := d.OutputCeiling(Tool, "find_references"); got < budget+outputCeilingSlack {
		t.Fatalf("find_references ceiling %d binds below its declared budget %d plus slack %d",
			got, budget, outputCeilingSlack)
	}
}
