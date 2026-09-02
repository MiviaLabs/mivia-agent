package clichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Two paths execute a model's tool call, and they must agree.
//
// `dispatcherShim.Run` (internal/agent) serves a tool the model may already
// call. `serveUnadmittedTool` -> `runDeferredToolNow` (internal/chat) serves
// one that is advertised but not yet admitted. They share no Go interface -
// one is an sdktools.Tool method, the other an eight-parameter Session method
// - so nothing named them siblings, and no compiler or existing gate could
// see them drift.
//
// They drifted badly. The deferred path was written as a second
// implementation and honoured FOUR of the nine contracts the admitted path
// had accumulated. Each missing one shipped as its own bug with its own
// symptom: a turn that hung with no prompt, a call that ran and was reported
// as never having run (so the model retried it and the side effect happened
// twice), a refusal rendered as a green success, an unbounded call, a tool
// deduped against its own capability, a denylisted tool executing.
//
// Nine fixes, one defect. `.agents/memories/sibling-implementations-drift.md`
// already names this as the repository's most expensive recurring class, and
// the gate-authoring skill already prescribes the cure - "when an interface
// has more than one implementation, the gate is a conformance suite, not
// another per-implementation test". The rule did not fire here only because
// the two paths are not an interface with two implementations; they are two
// functions nobody had written down as related.
//
// This is that table. It drives both paths through the REAL attach path and
// a real session turn, and asserts on what an operator or the model can
// actually observe, never on internals. A third path must join it or its
// absence is visible.

// execPath selects which of the two implementations a case exercises.
//
// The selector is the TIER, because that is what actually routes the call: a
// core tool is admitted and reaches dispatcherShim.Run, a deferred one is not
// and reaches serveUnadmittedTool. Nothing else about the two cases differs,
// which is what makes a disagreement between them a real finding rather than
// a difference in setup.
type execPath struct {
	name string
	core bool
}

var execPaths = []execPath{
	{name: "admitted", core: true},
	{name: "deferred", core: false},
}

// tiersFor puts the probe in the core tier or leaves it deferred. read_file is
// always core so the tier feature is active in both cases (a plan with nothing
// deferred is inert by design, and would silently make both cases the same
// path).
func (p execPath) tiersFor(probe string) (core []string, effective []string) {
	if p.core {
		return []string{"read_file", probe}, []string{"read_file", "grep", probe}
	}
	return []string{"read_file"}, []string{"read_file", "grep", probe}
}

// --- the contracts -------------------------------------------------------

// C1: every tool call is bounded by a deadline.
//
// The admitted path arms one from the tool's Capability.Timeout; the deferred
// path armed nothing, so the FIRST call to a deferred run_command ran
// unbounded while the identical call one step later was bounded.
func TestEveryPathBoundsAToolCall(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &deadlineProbe{name: "probe_tool"}
			runProbeTurn(t, path, probe, `{}`)
			if !probe.ran.Load() {
				t.Fatal("the probe never executed, so this case proves nothing")
			}
			checkContract(t, "bounded-call", path.name, probe.hadDeadline.Load(),
				"this path handed the tool a context with NO deadline: a tool "+
					"that hangs hangs the turn, and the timeout the tool declared "+
					"for itself was dropped")
		})
	}
}

// C2: a tool's dedup declaration is honoured.
//
// ExecutionRead calls always execute fresh; Write/External dedup so a
// duplicate delivery cannot repeat a side effect. The deferred path sent
// neither SkipDedup nor Step, so a read-class tool deduped against its own
// capability and the model got a stale body back.
func TestEveryPathHonoursTheDedupDeclaration(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &countingProbe{name: "probe_tool", class: tools.ExecutionRead}
			runProbeTurnCalls(t, path, probe,
				namedCall("c1", "probe_tool", `{}`),
				namedCall("c2", "probe_tool", `{}`))
			checkContract(t, "fresh-read-executes-twice", path.name,
				probe.runs.Load() == 2,
				fmt.Sprintf("a READ-class tool executed %d time(s) for two identical "+
					"calls; its capability says reads always run fresh, so the "+
					"second was answered from a record of the first", probe.runs.Load()))
		})
	}
}

// C3: a call that RAN and failed is reported as failed, and never as one that
// did not happen.
//
// The deferred path answered every error with "authorized but was not yet
// loaded ... retry the call on your next step", so the model retried a call
// whose side effects had already landed.
func TestEveryPathReportsAFailureAsAFailure(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &failingProbe{name: "probe_tool"}
			body := runProbeTurn(t, path, probe, `{}`)
			if !probe.ran.Load() {
				t.Fatal("the probe never executed, so this case proves nothing")
			}
			ranAndSaidSo := !strings.Contains(body, "not yet loaded") &&
				!strings.Contains(body, "retry the call")
			checkContract(t, "failure-reported-as-failure", path.name, ranAndSaidSo,
				fmt.Sprintf("a tool that RAN and failed was reported to the model "+
					"as never having run, with an instruction to retry - every "+
					"side effect it already landed happens twice: %q", body))
			checkContract(t, "failure-names-its-cause", path.name,
				strings.Contains(body, "probe failed"),
				fmt.Sprintf("the model was not told why the call failed: %q", body))
		})
	}
}

// C4: an approval policy of "deny" stops the call on every path.
//
// The deferred path invoked the dispatcher directly, underneath the wrapper
// where approval lived, and executed a write tool under a deny policy.
func TestEveryPathRefusesUnderADenyPolicy(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &countingProbe{name: "probe_tool", class: tools.ExecutionWrite}
			runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.ApprovalPolicy = config.ApprovalPolicyDeny
			}, namedCall("c1", "probe_tool", `{}`))
			checkContract(t, "deny-policy-refuses", path.name, probe.runs.Load() == 0,
				"a write tool EXECUTED under a \"deny\" approval policy")
		})
	}
}

// C6: the model-facing result is capped on every path.
//
// The deferred path capped its success bodies and, for a while, not its
// failures - a caller-authored reason with no bound of its own. Capping now
// belongs to the shim both paths share, and this holds that true.
func TestEveryPathCapsTheResult(t *testing.T) {
	const cap = 200
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &bulkyProbe{name: "probe_tool", size: 8000}
			body := runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.MaxToolResultChars = cap
			}, namedCall("c1", "probe_tool", `{}`))
			checkContract(t, "result-capped", path.name, len(body) < 4000,
				fmt.Sprintf("the model received %d bytes for a tool whose result "+
					"cap is %d; an uncapped body blows the context window the cap "+
					"exists to protect", len(body), cap))
		})
	}
}

// C7: a tool named in ref_only_tools never inlines its body.
//
// RefOnlyTools is applied by wrapping tools IN THE SDK REGISTRY. A deferred
// tool is deliberately absent from that registry, so the wrapper never saw it
// and an operator who named one in ref_only_tools silently got the full body
// inline - the setting applying to a tool after it loaded and not before.
func TestEveryPathHonoursRefOnlyTools(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &bulkyProbe{name: "probe_tool", size: 40000}
			body := runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
				f.sess.RefOnlyTools = []string{"probe_tool"}
			}, namedCall("c1", "probe_tool", `{}`))
			checkContract(t, "ref-only-is-not-inlined", path.name,
				strings.Contains(body, "elided"),
				fmt.Sprintf("the operator named this tool in ref_only_tools and "+
					"the model received its body inline anyway (%d bytes, no "+
					"elision notice)", len(body)))
		})
	}
}

// C8: a tool that does NOT declare a capability must still dedup.
//
// The unclassified default is ExecutionExternal (tools.CapabilityOf) - a tool
// that says nothing about itself is assumed to have side effects. The shim
// used `tools.Capability{}` instead, whose zero Class is ExecutionRead, so
// Dedups() was false and SkipDedup was TRUE: an unclassified tool never
// deduped, and a duplicate delivery re-ran its side effect.
//
// Unclassified is not hypothetical. workflow_run, workflow_deliver,
// post_message and every ledger tool ship without a Capability method.
func TestEveryPathDedupsAnUnclassifiedTool(t *testing.T) {
	for _, path := range execPaths {
		t.Run(path.name, func(t *testing.T) {
			probe := &unclassifiedProbe{name: "probe_tool"}
			runProbeTurnCalls(t, path, probe,
				namedCall("c1", "probe_tool", `{}`),
				namedCall("c2", "probe_tool", `{}`))
			checkContract(t, "unclassified-dedups", path.name, probe.runs.Load() == 1,
				fmt.Sprintf("an unclassified tool executed %d times for two "+
					"identical calls in one turn; a tool that declares nothing is "+
					"assumed to have side effects, and a duplicate delivery must "+
					"not repeat them", probe.runs.Load()))
		})
	}
}

// --- the harness ---------------------------------------------------------

func runProbeTurn(t *testing.T, path execPath, probe tools.Tool, args string) string {
	t.Helper()
	return runProbeTurnCalls(t, path, probe, namedCall("c1", probe.Name(), args))
}

func runProbeTurnCalls(t *testing.T, path execPath, probe tools.Tool, calls ...provider.ToolCall) string {
	t.Helper()
	return runProbeTurnWith(t, path, probe, nil, calls...)
}

// runProbeTurnWith drives ONE real session turn in which the model calls the
// probe, and returns the tool-result body the model was handed.
//
// It goes through attachSessionDispatcher and Session.SendUser - the same
// entry points a user session uses - so a contract honoured only by a test
// seam cannot pass here.
func runProbeTurnWith(t *testing.T, path execPath, probe tools.Tool, tune func(*deferredFixture), calls ...provider.ToolCall) string {
	t.Helper()
	completer := &scriptedCompleter{turns: []provider.Response{
		toolCallResponse(calls...),
		{Content: "done"},
	}}
	core, effective := path.tiersFor(probe.Name())
	fixture := newDeferredFixture(t, completer, core, effective, probe)
	// Auto by default so approval is not a confound in the cases that are
	// about something else. An UNSET policy is not neutral here: the two
	// paths disagree about what it means, which is its own case below.
	fixture.sess.ApprovalPolicy = config.ApprovalPolicyAuto
	if tune != nil {
		tune(fixture)
	}
	if _, err := fixture.sess.SendUser(context.Background(), "use the probe", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	return toolResultBodies(completer)
}

// toolResultBodies is every tool-role message the model received, which is
// where a path's answer about a call actually lands.
func toolResultBodies(c *scriptedCompleter) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, msgs := range c.messages {
		for _, m := range msgs {
			if m.Role == provider.RoleTool {
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// --- probes --------------------------------------------------------------

type deadlineProbe struct {
	name        string
	ran         atomic.Bool
	hadDeadline atomic.Bool
}

func (p *deadlineProbe) Name() string               { return p.name }
func (p *deadlineProbe) Description() string        { return "records its deadline" }
func (p *deadlineProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *deadlineProbe) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionExternal, Timeout: 30 * time.Second}
}
func (p *deadlineProbe) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	p.ran.Store(true)
	_, ok := ctx.Deadline()
	p.hadDeadline.Store(ok)
	return "probe ok", nil
}

type countingProbe struct {
	name  string
	class tools.ExecutionClass
	runs  atomic.Int32
}

func (p *countingProbe) Name() string               { return p.name }
func (p *countingProbe) Description() string        { return "counts its runs" }
func (p *countingProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *countingProbe) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: p.class}
}
func (p *countingProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return fmt.Sprintf("run %d", p.runs.Add(1)), nil
}

type failingProbe struct {
	name string
	ran  atomic.Bool
}

func (p *failingProbe) Name() string               { return p.name }
func (p *failingProbe) Description() string        { return "always fails" }
func (p *failingProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *failingProbe) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionExternal}
}
func (p *failingProbe) Execute(context.Context, json.RawMessage) (string, error) {
	p.ran.Store(true)
	return "", fmt.Errorf("probe failed")
}

// C5: the two paths must agree about what an UNSET approval policy means.
//
// They do not. sdkadapter.NeedsApprovalLayer treats an empty policy as "this
// caller opted out of the approval layer entirely", so the admitted path runs
// the call. decideDeferredApproval defaults an empty policy to write-only, so
// the deferred path demands approval, finds no gate, and refuses.
//
// The operator sees a tool that works once it has been loaded and is denied
// before that, with a message about an approver they never configured. Both
// behaviours are defensible on their own; having both in one session is not,
// and no per-path test could see it.
func TestEveryPathAgreesOnAnUnsetApprovalPolicy(t *testing.T) {
	var ran []string
	for _, path := range execPaths {
		probe := &countingProbe{name: "probe_tool", class: tools.ExecutionExternal}
		runProbeTurnWith(t, path, probe, func(f *deferredFixture) {
			f.sess.ApprovalPolicy = "" // exactly as an unconfigured session arrives
		}, namedCall("c1", "probe_tool", `{}`))
		if probe.runs.Load() > 0 {
			ran = append(ran, path.name)
		}
	}
	checkContract(t, "unset-approval-policy-agrees", "all",
		len(ran) == 0 || len(ran) == len(execPaths),
		fmt.Sprintf("with no approval policy configured, the call executed on %v "+
			"and not on the other path(s); the same session then answers the "+
			"same tool call two different ways depending only on whether the "+
			"tool had been loaded yet", ran))
}

const conformancePolicyPath = "../../.mivia/policy/tool-execution-conformance.json"

// checkContract records one contract result against the declared divergences.
//
// It fails BOTH ways on purpose. An undeclared divergence fails, which is the
// gate. A declared one that now HOLDS also fails, because a stale entry is
// how an exemption list stops describing the code and starts hiding the next
// real divergence - the same rule the viewer-surface table uses.
func checkContract(t *testing.T, contract, path string, holds bool, failure string) {
	t.Helper()
	reason, declared := conformanceExemptions(t)[contract+"/"+path]
	switch {
	case holds && declared:
		t.Errorf("%s/%s is declared as a divergence in %s, but it HOLDS now. "+
			"Delete the entry: a stale exemption hides the next real one.\n"+
			"The declared reason was: %s", contract, path, conformancePolicyPath, reason)
	case !holds && declared:
		t.Logf("%s/%s: declared divergence, accepted. %s", contract, path, reason)
	case !holds:
		t.Errorf("%s/%s: %s\n\nThe two tool-execution paths must agree. If this "+
			"one genuinely may not, declare it in %s WITH A REASON - an entry "+
			"with no reason is a bypass with extra steps.",
			contract, path, failure, conformancePolicyPath)
	}
}

func conformanceExemptions(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(conformancePolicyPath)
	if err != nil {
		t.Fatalf("read %s: %v", conformancePolicyPath, err)
	}
	var policy struct {
		Exempt map[string]string `json:"exempt"`
	}
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatalf("parse %s: %v", conformancePolicyPath, err)
	}
	for key, reason := range policy.Exempt {
		if len(strings.TrimSpace(reason)) < 40 {
			t.Errorf("%s declares %s with a reason too short to be one (%q); an "+
				"exemption that says nothing is a bypass with extra steps",
				conformancePolicyPath, key, reason)
		}
	}
	return policy.Exempt
}

type bulkyProbe struct {
	name string
	size int
}

func (p *bulkyProbe) Name() string               { return p.name }
func (p *bulkyProbe) Description() string        { return "returns a lot" }
func (p *bulkyProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *bulkyProbe) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (p *bulkyProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return strings.Repeat("x", p.size), nil
}

// unclassifiedProbe implements no Capability method at all, like every
// workflow ledger tool and the messaging tools.
type unclassifiedProbe struct {
	name string
	runs atomic.Int32
}

func (p *unclassifiedProbe) Name() string               { return p.name }
func (p *unclassifiedProbe) Description() string        { return "declares nothing" }
func (p *unclassifiedProbe) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *unclassifiedProbe) Execute(context.Context, json.RawMessage) (string, error) {
	return fmt.Sprintf("run %d", p.runs.Add(1)), nil
}
